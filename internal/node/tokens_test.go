package node

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/BlackDark/csi-driver-rclone/internal/mountpod"
	"github.com/BlackDark/csi-driver-rclone/internal/mountspec"
	"github.com/BlackDark/csi-driver-rclone/internal/oauthtoken"
)

var oauthCreds = map[string]string{mountspec.KeyConfigData: "[box]\ntype = box\ntoken = seed\n"}

func withTokens(h *harness) *oauthtoken.Manager {
	m := &oauthtoken.Manager{
		Store: oauthtoken.Store{CS: h.cs, Namespace: "drv"},
		Locker: oauthtoken.Locker{CS: h.cs, Namespace: "drv", Clock: clocktesting.NewFakeClock(time.Unix(0, 0)),
			Duration: 30 * time.Second, MaxHold: time.Minute, Poll: time.Second},
		Node: "node-a", LockWait: time.Second, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h.srv.deps.Tokens = m
	return m
}

func mountSpecFile(t *testing.T, h *harness, id string) mountspec.Spec {
	t.Helper()
	sec, err := h.cs.CoreV1().Secrets("drv").Get(context.Background(), mountpod.Name(id), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := mountspec.Load(sec.Data[mountpod.SpecKey])
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestPublishSeedsAndSubstitutesToken(t *testing.T) {
	h := newHarness(t)
	m := withTokens(h)
	seedHash := mountspec.HashToken("seed")
	refreshed := `{"refresh_token":"r2","expiry":"2030-01-01T00:00:00Z"}`
	if _, err := m.Store.Seed(context.Background(), "box", seedHash, "seed"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Store.Save(context.Background(), "box", seedHash, refreshed); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(h.dir, "t")
	if _, err := h.srv.NodePublishVolume(context.Background(), publishReq(target, oauthCreds)); err != nil {
		t.Fatal(err)
	}
	id := h.srv.idOf(t, target)
	spec := mountSpecFile(t, h, id)
	if len(spec.Tokens) != 1 || mountspec.TokenOf(spec.Remote.ConfigData, spec.Tokens[0].Isolated) != refreshed {
		t.Fatalf("mount pod did not get the stored token: %+v", spec.Tokens)
	}
	if h.ho.tokens[0] == nil {
		t.Fatal("handoff server has no token handler")
	}
}

func TestPublishWithoutWriteBackHasNoHandler(t *testing.T) {
	h := newHarness(t)
	withTokens(h)
	req := publishReq(filepath.Join(h.dir, "t"), oauthCreds)
	req.VolumeContext[mountspec.KeyTokenWriteBack] = "false"
	if _, err := h.srv.NodePublishVolume(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if h.ho.tokens[0] != nil {
		t.Fatal("opt-out volume got a token handler")
	}
	if n := h.secrets(t); n != 1 {
		t.Fatalf("secrets %d, want only the Mount Pod Secret", n)
	}
}
