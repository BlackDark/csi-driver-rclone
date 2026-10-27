package node

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/BlackDark/csi-driver-rclone/internal/handoff"
)

func (h *harness) health(t *testing.T, target string) []*csi.VolumeHealth_VolumeHealthEntry {
	t.Helper()
	r, err := h.srv.NodeGetVolumeHealth(context.Background(), &csi.NodeGetVolumeHealthRequest{VolumeId: "vol-1", VolumePublishPath: target})
	if err != nil {
		t.Fatal(err)
	}
	return r.GetVolumeHealth().GetHealthStatuses()
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *harness) publishAs(t *testing.T, target, pod string) {
	t.Helper()
	req := publishReq(target, creds)
	req.VolumeContext[keyPodName] = pod
	req.VolumeContext[keyPodNamespace] = "app"
	if _, err := h.srv.NodePublishVolume(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}

func TestPublishHungMountTimesOutWithoutLeak(t *testing.T) {
	h := newHarness(t)
	h.ho.hang = true
	h.srv.cfg.PublishTimeout = 50 * time.Millisecond
	start := time.Now()
	_, err := h.srv.NodePublishVolume(context.Background(), publishReq(filepath.Join(h.dir, "t"), creds))
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("publish took %s", d)
	}
	if h.pods(t) != 0 || h.secrets(t) != 0 || h.fuse.count() != 0 || h.bindMounts(t) != 0 || len(h.srv.mounts) != 0 {
		t.Fatalf("leak: pods=%d secrets=%d fuse=%d binds=%d mounts=%d",
			h.pods(t), h.secrets(t), h.fuse.count(), h.bindMounts(t), len(h.srv.mounts))
	}
}

func TestMountPodLostReportsAndRecreates(t *testing.T) {
	h := newHarness(t)
	a := filepath.Join(h.dir, "a")
	h.publishAs(t, a, "web-0")
	if len(h.health(t, a)) != 0 {
		t.Fatal("healthy mount reported unhealthy")
	}
	h.ho.send(handoff.Event{Kind: handoff.EventClosed, Conn: 1}) // EOF without drained
	eventually(t, "recreated Mount Pod", func() bool { return h.ho.serveCount() == 2 })
	h.srv.Wait()

	hs := h.health(t, a)
	if len(hs) != 1 || hs[0].Status != csi.VolumeHealthErrorType_INACCESSIBLE || !strings.Contains(hs[0].Message, "restart the pod") {
		t.Fatalf("health: %v", hs)
	}
	if app := h.events(t, "app"); len(app) != 1 || app[0] != ReasonMountLost+"/web-0" {
		t.Errorf("workload events: %v", app)
	}
	if drv := h.events(t, "drv"); len(drv) != 1 || !strings.HasPrefix(drv[0], ReasonMountLost+"/rclone-mount-") {
		t.Errorf("mount pod events: %v", drv)
	}
	if h.pods(t) != 1 || h.secrets(t) != 1 || h.fuse.count() != 1 || h.srv.deps.Users.Count(h.srv.firstID()) != 1 {
		t.Fatalf("after recreate: pods=%d secrets=%d fuse=%d", h.pods(t), h.secrets(t), h.fuse.count())
	}

	// kubelet republishes after the pod restarts: the new target is healthy.
	b := filepath.Join(h.dir, "b")
	h.publishAs(t, b, "web-1")
	h.unpublish(t, a)
	if len(h.health(t, b)) != 0 || h.ho.serveCount() != 2 {
		t.Fatalf("new target: health=%v serves=%d", h.health(t, b), h.ho.serveCount())
	}
}

func TestMountPodFailedIsLost(t *testing.T) {
	h := newHarness(t)
	a := filepath.Join(h.dir, "a")
	h.publish(t, a)
	pods, _ := h.cs.CoreV1().Pods("drv").List(context.Background(), metav1.ListOptions{})
	p := pods.Items[0]
	p.Status.Phase = corev1.PodFailed
	h.srv.MountPodChanged(&p, false)
	h.ho.send(handoff.Event{Kind: handoff.EventClosed, Conn: 1}) // rclone exits after the Pod stops
	eventually(t, "recreated Mount Pod", func() bool { return h.ho.serveCount() == 2 })
	h.srv.Wait()
	if len(h.health(t, a)) != 1 {
		t.Fatal("target not reported dead")
	}
}

func TestOwnReleaseIsNotLost(t *testing.T) {
	h := newHarness(t)
	a := filepath.Join(h.dir, "a")
	h.publish(t, a)
	h.unpublish(t, a) // linger 0: released at once
	pods, _ := h.cs.CoreV1().Pods("drv").List(context.Background(), metav1.ListOptions{})
	if len(pods.Items) != 0 {
		t.Fatal("pod not deleted")
	}
	h.ho.send(handoff.Event{Kind: handoff.EventClosed, Conn: 1})
	h.srv.Wait()
	if ev := h.events(t, "drv"); len(ev) != 0 || h.ho.serveCount() != 1 {
		t.Fatalf("release treated as loss: events=%v serves=%d", ev, h.ho.serveCount())
	}
}

func TestVolumeStatsAndNotConnected(t *testing.T) {
	h := newHarness(t)
	a := filepath.Join(h.dir, "a")
	r, err := h.srv.NodeGetVolumeStats(context.Background(), &csi.NodeGetVolumeStatsRequest{VolumeId: "vol-1", VolumePath: a})
	if err != nil || len(r.Usage) != 2 || r.Usage[0].Total != 100 {
		t.Fatalf("stats: %v %v", r, err)
	}
	h.statErr[a] = syscall.ENOTCONN
	if _, err := h.srv.NodeGetVolumeStats(context.Background(), &csi.NodeGetVolumeStatsRequest{VolumeId: "vol-1", VolumePath: a}); status.Code(err) != codes.Unavailable {
		t.Fatalf("dead stats err = %v", err)
	}
	if hs := h.health(t, a); len(hs) != 1 || hs[0].Reason != ReasonMountPodLost {
		t.Fatalf("health: %v", hs)
	}
}

func TestCapabilities(t *testing.T) {
	h := newHarness(t)
	r, _ := h.srv.NodeGetCapabilities(context.Background(), nil)
	got := map[csi.NodeServiceCapability_RPC_Type]bool{}
	for _, c := range r.Capabilities {
		got[c.GetRpc().GetType()] = true
	}
	if !got[csi.NodeServiceCapability_RPC_GET_VOLUME_STATS] || !got[csi.NodeServiceCapability_RPC_GET_VOLUME_HEALTH] {
		t.Fatalf("caps: %v", got)
	}
}

func (s *Server) firstID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.mounts {
		return id
	}
	return ""
}
