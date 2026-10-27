package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/mount-utils"

	"github.com/BlackDark/csi-driver-rclone/internal/handoff"
	"github.com/BlackDark/csi-driver-rclone/internal/mountpod"
	"github.com/BlackDark/csi-driver-rclone/internal/mountspec"
)

func inlineContext(extra map[string]string) map[string]string {
	vc := map[string]string{"remotePath": "bucket", ephemeralKey: "true", keyPodName: "web-0", keyPodNamespace: "app"}
	for k, v := range extra {
		vc[k] = v
	}
	return vc
}

func TestInlineVolumePublishes(t *testing.T) {
	h := newHarness(t)
	req := publishReq(filepath.Join(h.dir, "t"), creds)
	req.VolumeId = "csi-0123abcd"
	req.VolumeContext = inlineContext(map[string]string{"vfs-cache-mode": "full"})
	if _, err := h.srv.NodePublishVolume(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	m := h.srv.getMount(h.srv.idOf(t, req.TargetPath))
	if h.pods(t) != 1 || m.spec.Mode != mountspec.Ephemeral {
		t.Fatalf("pods=%d spec=%+v", h.pods(t), m.spec)
	}
}

func TestInlineVolumeRejects(t *testing.T) {
	for key, extra := range map[string]map[string]string{
		"allowAllOptions":        {"allowAllOptions": "true"},
		"tpslimit":               {"tpslimit": "8"},
		"cacheVolume":            {"cacheVolume": `{"emptyDir":{}}`},
		"mountPod.memoryRequest": {"mountPod.memoryRequest": "1Gi"},
	} {
		h := newHarness(t)
		req := publishReq(filepath.Join(h.dir, "t"), creds)
		req.VolumeContext = inlineContext(extra)
		_, err := h.srv.NodePublishVolume(context.Background(), req)
		if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), key) ||
			!strings.Contains(err.Error(), string(mountspec.ReasonNotForEphemeral)) {
			t.Errorf("%s: err = %v", key, err)
		}
		if h.pods(t) != 0 || len(h.events(t, "app")) != 0 {
			t.Errorf("%s: pods=%d events=%v", key, h.pods(t), h.events(t, "app"))
		}
	}
}

func TestReleasedInlineVolumeRemovesItsCache(t *testing.T) {
	h := newHarness(t)
	req := publishReq(filepath.Join(h.dir, "t"), creds)
	req.VolumeId = "csi-0123abcd"
	req.VolumeContext = inlineContext(nil)
	if _, err := h.srv.NodePublishVolume(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	cache := mountpod.CacheDir(h.srv.cfg.Pod.CacheRoot, req.VolumeId)
	if err := os.MkdirAll(cache, 0o750); err != nil {
		t.Fatal(err)
	}
	h.unpublish(t, req.TargetPath)
	h.ho.send(handoff.Event{Kind: handoff.EventDrained, Conn: 1})
	h.srv.Wait()
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Errorf("inline cache kept: %v", err)
	}
}

func TestUnrestrictedOptionsEmitEvent(t *testing.T) {
	h := newHarness(t)
	req := publishReq(filepath.Join(h.dir, "t"), creds)
	req.VolumeContext[mountspec.KeyAllowAllOptions] = "true"
	req.VolumeContext["fast-list"] = "true"
	req.VolumeContext[keyPodName], req.VolumeContext[keyPodNamespace] = "web-0", "app"
	if _, err := h.srv.NodePublishVolume(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if ev := h.events(t, "app"); len(ev) != 1 || ev[0] != ReasonUnrestrictedOptions+"/web-0" {
		t.Errorf("events: %v", ev)
	}
	plain := publishReq(filepath.Join(h.dir, "u"), creds) // allowlisted only: no Event
	plain.VolumeId = "vol-2"
	plain.VolumeContext[keyPodName], plain.VolumeContext[keyPodNamespace] = "web-1", "app"
	if _, err := h.srv.NodePublishVolume(context.Background(), plain); err != nil {
		t.Fatal(err)
	}
	if ev := h.events(t, "app"); len(ev) != 1 {
		t.Errorf("events: %v", ev)
	}
}

func TestSecondIDOfVolumeIsRefusedUntilReleased(t *testing.T) {
	h := newHarness(t)
	h.srv.cfg.Linger = time.Minute
	a, b := filepath.Join(h.dir, "a"), filepath.Join(h.dir, "b")
	h.publish(t, a)
	changed := publishReq(b, creds)
	changed.VolumeContext["vfs-cache-mode"] = "full"
	_, err := h.srv.NodePublishVolume(context.Background(), changed)
	if status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), "Cache") {
		t.Fatalf("err = %v", err)
	}
	if h.pods(t) != 1 || h.fuse.count() != 1 {
		t.Fatalf("pods=%d fuse=%d", h.pods(t), h.fuse.count())
	}
	h.unpublish(t, a) // lingers: still owns the Cache
	if _, err := h.srv.NodePublishVolume(context.Background(), changed); status.Code(err) != codes.Unavailable {
		t.Fatalf("during linger: err = %v", err)
	}
	h.clk.Step(2 * time.Minute)
	eventually(t, "old Mount Pod deleted", func() bool { return h.pods(t) == 0 })
	h.ho.send(handoff.Event{Kind: handoff.EventDrained, Conn: 1})
	h.srv.Wait()
	if _, err := h.srv.NodePublishVolume(context.Background(), changed); err != nil {
		t.Fatalf("after release: %v", err)
	}
}

func (s *Server) idOf(t *testing.T, target string) string {
	t.Helper()
	id, ok := s.deps.Users.Lookup(target)
	if !ok {
		t.Fatalf("no user %s", target)
	}
	return id
}

func (h *harness) usersAnnotation(t *testing.T, id string) string {
	t.Helper()
	p, err := h.srv.deps.Kube.GetPod(context.Background(), mountpod.Name(id))
	if err != nil || p == nil {
		t.Fatalf("pod: %v %v", p, err)
	}
	return p.Annotations[mountpod.AnnotationUsers]
}

func TestUsersAnnotationFollowsPublishes(t *testing.T) {
	h := newHarness(t)
	a, b := filepath.Join(h.dir, "a"), filepath.Join(h.dir, "b")
	h.publishAs(t, a, "web-0")
	h.publishAs(t, b, "web-1")
	id := h.srv.idOf(t, a)
	got := decodeUsers(h.usersAnnotation(t, id))
	if len(got) != 2 || got[a].Name != "web-0" || got[b].Name != "web-1" || got[a].Namespace != "app" {
		t.Fatalf("users: %v", got)
	}
	h.unpublish(t, a)
	if got := decodeUsers(h.usersAnnotation(t, id)); len(got) != 1 || got[b] == nil {
		t.Fatalf("after unpublish: %v", got)
	}
}

// After a restart, the adopted mount knows its Mount Spec and workload pods: a crash
// is recreated at once and reported to the pod.
func TestAdoptedMountRecoversWithSpecAndRefs(t *testing.T) {
	h := newHarness(t)
	target := filepath.Join(h.dir, "pods", "u1", "volumes", "kubernetes.io~csi", "pv", "mount")
	h.publishAs(t, target, "web-0")
	id := h.srv.idOf(t, target)
	h.infos = []mount.MountInfo{
		{Minor: 50, MountPoint: mountpod.FuseDir(h.dir, id)},
		{Minor: 50, MountPoint: target},
	}

	deps := h.srv.deps
	deps.Users = NewUsers()
	h.srv = New(h.srv.cfg, deps) // the restarted plugin
	if err := h.srv.Adopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := h.srv.getMount(id)
	if m == nil || m.spec == nil || m.spec.Identity == "" || m.lastPod == nil || m.lastPod.Name != "web-0" {
		t.Fatalf("adopted: %+v", m)
	}
	eventually(t, "adopt served", func() bool { return h.ho.serveCount() == 2 })
	h.ho.send(handoff.Event{Kind: handoff.EventClosed, Conn: 1})
	eventually(t, "recreated Mount Pod", func() bool { return h.ho.serveCount() == 3 })
	h.srv.Wait()
	if app := h.events(t, "app"); len(app) != 1 || app[0] != ReasonMountLost+"/web-0" {
		t.Errorf("workload events: %v", app)
	}
	if h.pods(t) != 1 || decodeUsers(h.usersAnnotation(t, id))[target] == nil {
		t.Errorf("recreated pod: pods=%d users=%q", h.pods(t), h.usersAnnotation(t, id))
	}
}
