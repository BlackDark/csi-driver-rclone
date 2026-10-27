package node

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/mount-utils"

	"github.com/BlackDark/csi-driver-rclone/internal/fusemount"
	"github.com/BlackDark/csi-driver-rclone/internal/handoff"
	"github.com/BlackDark/csi-driver-rclone/internal/mountpod"
)

func mountPodObj(id string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: mountpod.Name(id), Namespace: "drv", UID: types.UID("uid-x" + id),
			Labels: mountpod.Labels("node-a", id)},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func TestScanNodeMatchesBindsByDevice(t *testing.T) {
	kd := "/k"
	infos := []mount.MountInfo{
		{Major: 0, Minor: 50, MountPoint: mountpod.FuseDir(kd, "aaa")},
		{Major: 0, Minor: 50, MountPoint: "/k/pods/u1/volumes/kubernetes.io~csi/pv/mount"},
		{Major: 0, Minor: 51, MountPoint: mountpod.FuseDir(kd, "bbb")},
		{Major: 0, Minor: 52, MountPoint: "/k/pods/u2/volumes/kubernetes.io~csi/other/mount"},
		{Major: 0, Minor: 50, MountPoint: "/elsewhere"},
	}
	pods := []corev1.Pod{*mountPodObj("aaa", corev1.PodRunning), *mountPodObj("ccc", corev1.PodRunning)}
	got := scanNode(pods, infos, kd)
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	a, b, c := got[0], got[1], got[2]
	if !a.fuse || a.pod == nil || len(a.targets) != 1 || !a.adoptable() {
		t.Errorf("aaa: %+v", a)
	}
	if !b.fuse || b.pod != nil || b.adoptable() {
		t.Errorf("bbb: %+v", b)
	}
	if c.fuse || c.pod == nil || c.adoptable() {
		t.Errorf("ccc: %+v", c)
	}
	failed := mountPodObj("aaa", corev1.PodFailed)
	if (nodeMount{fuse: true, pod: failed}).adoptable() {
		t.Error("failed pod adoptable")
	}
}

func (h *harness) createPod(t *testing.T, p *corev1.Pod) {
	t.Helper()
	if _, err := h.cs.CoreV1().Pods("drv").Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptAfterRestart(t *testing.T) {
	h := newHarness(t)
	h.srv.cfg.Linger = time.Minute
	kd := h.dir
	live, idle, orphanFuse, deadPod := "1live", "2idle", "3orph", "4dead"
	target := filepath.Join(kd, "pods", "u1", "volumes", "kubernetes.io~csi", "pv", "mount")
	deadTarget := filepath.Join(kd, "pods", "u2", "volumes", "kubernetes.io~csi", "pv2", "mount")
	h.infos = []mount.MountInfo{
		{Minor: 50, MountPoint: mountpod.FuseDir(kd, live)},
		{Minor: 50, MountPoint: target},
		{Minor: 51, MountPoint: mountpod.FuseDir(kd, idle)},
		{Minor: 52, MountPoint: mountpod.FuseDir(kd, orphanFuse)},
		{Minor: 52, MountPoint: deadTarget},
	}
	for _, id := range []string{idle, orphanFuse} {
		h.fuse.mounted[mountpod.FuseDir(kd, id)] = fusemount.Opts{}
	}
	h.createPod(t, mountPodObj(live, corev1.PodRunning))
	h.createPod(t, mountPodObj(idle, corev1.PodRunning))
	h.createPod(t, mountPodObj(deadPod, corev1.PodFailed))

	if err := h.srv.Adopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.srv.getMount(live) == nil || h.srv.getMount(idle) == nil || len(h.srv.mounts) != 2 {
		t.Fatalf("adopted: %v", h.srv.mounts)
	}
	eventually(t, "two handoff servers", func() bool { return h.ho.serveCount() == 2 })
	h.ho.mu.Lock()
	modes := h.ho.modes
	h.ho.mu.Unlock()
	for _, m := range modes {
		if m != handoff.ModeAdopt {
			t.Fatalf("modes %v", modes)
		}
	}
	if id, _ := h.srv.deps.Users.Lookup(target); id != live {
		t.Errorf("target user not rebuilt: %q", id)
	}
	if _, ok := h.fuse.mounted[mountpod.FuseDir(kd, orphanFuse)]; ok {
		t.Error("orphan fuse mount kept")
	}
	if len(h.health(t, deadTarget)) != 1 || len(h.health(t, target)) != 0 {
		t.Error("health after adopt wrong")
	}
	if p, _ := h.srv.deps.Kube.GetPod(context.Background(), mountpod.Name(deadPod)); p != nil {
		t.Error("failed Mount Pod not deleted")
	}

	// the unused adopted mount lingers, then goes
	h.clk.Step(2 * time.Minute)
	eventually(t, "idle Mount Pod released", func() bool {
		p, _ := h.srv.deps.Kube.GetPod(context.Background(), mountpod.Name(idle))
		return p == nil
	})
	// unpublish of the adopted target releases the live Mount Pod
	h.srv.cfg.Linger = 0
	h.unpublish(t, target)
	if p, _ := h.srv.deps.Kube.GetPod(context.Background(), mountpod.Name(live)); p != nil {
		t.Error("live Mount Pod not released after last unpublish")
	}
}

func TestAdoptSkipsUnservedFuse(t *testing.T) {
	h := newHarness(t)
	id := "1live"
	fuse := mountpod.FuseDir(h.dir, id)
	h.infos = []mount.MountInfo{{Minor: 50, MountPoint: fuse}}
	h.fuse.mounted[fuse] = fusemount.Opts{}
	h.statErr[fuse] = syscall.ENOTCONN
	h.createPod(t, mountPodObj(id, corev1.PodRunning))
	if err := h.srv.Adopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.srv.mounts) != 0 || h.fuse.count() != 0 || h.pods(t) != 0 {
		t.Fatalf("dead mount adopted: mounts=%d fuse=%d pods=%d", len(h.srv.mounts), h.fuse.count(), h.pods(t))
	}
}
