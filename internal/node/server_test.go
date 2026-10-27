package node

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/BlackDark/csi-driver-rclone/internal/handoff"
)

func (h *harness) pods(t *testing.T) int {
	t.Helper()
	l, err := h.cs.CoreV1().Pods("drv").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return len(l.Items)
}

func (h *harness) secrets(t *testing.T) int {
	t.Helper()
	l, err := h.cs.CoreV1().Secrets("drv").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return len(l.Items)
}

func (h *harness) publish(t *testing.T, target string) {
	t.Helper()
	if _, err := h.srv.NodePublishVolume(context.Background(), publishReq(target, creds)); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) bindMounts(t *testing.T) int {
	t.Helper()
	l, _ := h.mounter.List()
	return len(l)
}

func TestPublishHappyPath(t *testing.T) {
	h := newHarness(t)
	target := filepath.Join(h.dir, "pods", "a", "mount")
	h.publish(t, target)
	if h.pods(t) != 1 || h.secrets(t) != 1 || h.fuse.count() != 1 || h.bindMounts(t) != 1 {
		t.Fatalf("pods=%d secrets=%d fuse=%d binds=%d", h.pods(t), h.secrets(t), h.fuse.count(), h.bindMounts(t))
	}
	s, _ := h.cs.CoreV1().Secrets("drv").List(context.Background(), metav1.ListOptions{})
	ref := s.Items[0].OwnerReferences
	if len(ref) != 1 || ref[0].Kind != "Pod" || ref[0].UID == "" {
		t.Errorf("secret owner: %+v", ref)
	}
	log := h.mounter.GetLog()
	if log[0].Target != target || !strings.HasSuffix(log[0].Source, "/fuse") {
		t.Errorf("bind: %+v", log[0])
	}
}

func TestRepublishIsIdempotent(t *testing.T) {
	h := newHarness(t)
	target := filepath.Join(h.dir, "t")
	h.publish(t, target)
	h.publish(t, target)
	if h.pods(t) != 1 || h.bindMounts(t) != 1 || h.ho.serves != 1 {
		t.Fatalf("pods=%d binds=%d serves=%d", h.pods(t), h.bindMounts(t), h.ho.serves)
	}
}

func TestCredentialInContextRejectedWithoutLeak(t *testing.T) {
	h := newHarness(t)
	req := publishReq(filepath.Join(h.dir, "t"), creds)
	req.VolumeContext["secret_access_key"] = "LEAKME"
	_, err := h.srv.NodePublishVolume(context.Background(), req)
	if status.Code(err) != codes.InvalidArgument || strings.Contains(err.Error(), "LEAKME") {
		t.Fatalf("err = %v", err)
	}
	if h.pods(t) != 0 {
		t.Error("pod created")
	}
}

func TestValidation(t *testing.T) {
	h := newHarness(t)
	block := publishReq(filepath.Join(h.dir, "t"), creds)
	block.VolumeCapability.AccessType = &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
	for _, req := range []*csi.NodePublishVolumeRequest{{}, block} {
		if _, err := h.srv.NodePublishVolume(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("err = %v", err)
		}
	}
}

func TestReadOnlyPublishRemountsBind(t *testing.T) {
	h := newHarness(t)
	req := publishReq(filepath.Join(h.dir, "t"), creds)
	req.Readonly = true
	if _, err := h.srv.NodePublishVolume(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	l, _ := h.mounter.List()
	if len(l) != 1 || !strings.Contains(strings.Join(l[0].Opts, ","), "ro") {
		t.Errorf("mounts: %+v", l)
	}
}

func TestTwoTargetsShareOneMountPodAndLastUnpublishDeletesIt(t *testing.T) {
	h := newHarness(t)
	a, b := filepath.Join(h.dir, "a"), filepath.Join(h.dir, "b")
	h.publish(t, a)
	h.publish(t, b)
	if h.pods(t) != 1 || h.bindMounts(t) != 2 || h.ho.serves != 1 {
		t.Fatalf("pods=%d binds=%d serves=%d", h.pods(t), h.bindMounts(t), h.ho.serves)
	}
	ctx := context.Background()
	if _, err := h.srv.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: "vol-1", TargetPath: a}); err != nil {
		t.Fatal(err)
	}
	if h.pods(t) != 1 || h.fuse.count() != 1 {
		t.Fatal("mount pod removed while still in use")
	}
	if _, err := h.srv.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: "vol-1", TargetPath: b}); err != nil {
		t.Fatal(err)
	}
	if h.pods(t) != 0 {
		t.Fatal("mount pod not deleted")
	}
	h.ho.send(handoff.Event{Kind: handoff.EventDrained, Conn: 1})
	h.srv.Wait()
	if h.fuse.count() != 0 || h.secrets(t) != 0 || h.bindMounts(t) != 0 || h.srv.getMount(h.firstID()) != nil {
		t.Errorf("teardown incomplete: fuse=%d secrets=%d binds=%d", h.fuse.count(), h.secrets(t), h.bindMounts(t))
	}
}

func (h *harness) firstID() string {
	for _, u := range h.fuse.unmounted {
		return filepath.Base(filepath.Dir(u))
	}
	return ""
}

func TestUnpublishUnknownTargetIsOK(t *testing.T) {
	h := newHarness(t)
	_, err := h.srv.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{VolumeId: "v", TargetPath: filepath.Join(h.dir, "none")})
	if err != nil {
		t.Fatal(err)
	}
}
