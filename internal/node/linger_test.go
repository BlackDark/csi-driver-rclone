package node

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/BlackDark/csi-driver-rclone/internal/handoff"
)

func (h *harness) unpublish(t *testing.T, target string) {
	t.Helper()
	if _, err := h.srv.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{VolumeId: "vol-1", TargetPath: target}); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) events(t *testing.T, ns string) []string {
	t.Helper()
	l, err := h.cs.CoreV1().Events(ns).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range l.Items {
		out = append(out, e.Reason+"/"+e.InvolvedObject.Name)
	}
	return out
}

func TestPublishWithinLingerReusesMountPod(t *testing.T) {
	h := newHarness(t)
	h.srv.cfg.Linger = time.Minute
	a, b := filepath.Join(h.dir, "a"), filepath.Join(h.dir, "b")
	h.publish(t, a)
	h.unpublish(t, a)
	if h.pods(t) != 1 || h.fuse.count() != 1 {
		t.Fatalf("mount pod gone before linger: pods=%d fuse=%d", h.pods(t), h.fuse.count())
	}
	h.clk.Step(30 * time.Second)
	h.publish(t, b)
	h.clk.Step(2 * time.Minute)
	if h.pods(t) != 1 || h.ho.serves != 1 || h.bindMounts(t) != 1 {
		t.Fatalf("not reused: pods=%d serves=%d binds=%d", h.pods(t), h.ho.serves, h.bindMounts(t))
	}
	if h.clk.HasWaiters() {
		t.Error("linger timer still armed after republish")
	}
}

func TestLingerExpiryTearsDown(t *testing.T) {
	h := newHarness(t)
	h.srv.cfg.Linger = time.Minute
	a := filepath.Join(h.dir, "a")
	h.publish(t, a)
	h.unpublish(t, a)
	h.clk.Step(59 * time.Second)
	if h.pods(t) != 1 {
		t.Fatal("mount pod deleted before linger expired")
	}
	h.clk.Step(time.Second)
	if h.pods(t) != 0 {
		t.Fatal("mount pod not deleted on linger expiry")
	}
	if h.fuse.count() != 1 {
		t.Fatal("fuse unmounted before the drain result")
	}
	h.ho.send(handoff.Event{Kind: handoff.EventDrained, Conn: 1})
	h.srv.Wait()
	if h.fuse.count() != 0 || h.secrets(t) != 0 || len(h.events(t, "drv")) != 0 {
		t.Errorf("fuse=%d secrets=%d events=%v", h.fuse.count(), h.secrets(t), h.events(t, "drv"))
	}
}

func TestSecondUnpublishRestartsLinger(t *testing.T) {
	h := newHarness(t)
	h.srv.cfg.Linger = time.Minute
	a, b := filepath.Join(h.dir, "a"), filepath.Join(h.dir, "b")
	h.publish(t, a)
	h.unpublish(t, a)
	h.clk.Step(50 * time.Second)
	h.publish(t, b)
	h.unpublish(t, b)
	h.clk.Step(50 * time.Second)
	if h.pods(t) != 1 {
		t.Fatal("old linger timer fired after reuse")
	}
	h.clk.Step(10 * time.Second)
	if h.pods(t) != 0 {
		t.Fatal("restarted linger did not expire")
	}
	h.ho.send(handoff.Event{Kind: handoff.EventDrained, Conn: 1})
	h.srv.Wait()
}

func TestDrainTimeoutEmitsEvents(t *testing.T) {
	h := newHarness(t)
	a := filepath.Join(h.dir, "a")
	req := publishReq(a, creds)
	req.VolumeContext[keyPodName] = "web-0"
	req.VolumeContext[keyPodNamespace] = "app"
	if _, err := h.srv.NodePublishVolume(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	h.unpublish(t, a)
	h.ho.send(handoff.Event{Kind: handoff.EventDrainTimeout, Conn: 1})
	h.srv.Wait()
	app, drv := h.events(t, "app"), h.events(t, "drv")
	if len(app) != 1 || app[0] != ReasonDrainTimeout+"/web-0" {
		t.Errorf("workload events: %v", app)
	}
	if len(drv) != 1 || drv[0][:len(ReasonDrainTimeout)] != ReasonDrainTimeout {
		t.Errorf("mount pod events: %v", drv)
	}
	if h.fuse.count() != 0 {
		t.Error("fuse not unmounted after drain timeout")
	}
}
