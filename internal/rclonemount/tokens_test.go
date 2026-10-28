package rclonemount

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/BlackDark/csi-driver-rclone/internal/mountspec"
)

type fakeLocker struct {
	mu      sync.Mutex
	newest  string
	err     error
	locks   int
	unlocks int
	saved   []string
}

func (f *fakeLocker) LockToken(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.locks++
	return f.newest, f.err
}

func (f *fakeLocker) UnlockToken(string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unlocks++
	return nil
}

func (f *fakeLocker) SaveToken(section, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, section+"="+token)
	return nil
}

var hookNow = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

func oauthTok(access string, expiry time.Time) string {
	return `{"access_token":"` + access + `","refresh_token":"r","expiry":"` + expiry.Format(time.RFC3339) + `"}`
}

// testHook is a tokenHook over a map config holding section box_1 (user name box).
func testHook(token string) (*tokenHook, map[string]string) {
	cfg := map[string]string{"box_1/token": token, "box_1/type": "box"}
	h := newTokenHook([]mountspec.TokenSection{{Section: "box", Isolated: "box_1", SeedHash: "x"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	var mu sync.Mutex
	h.get = func(s, k string) (string, bool) { mu.Lock(); defer mu.Unlock(); v, ok := cfg[s+"/"+k]; return v, ok }
	h.store = func(s, k, v string) { mu.Lock(); defer mu.Unlock(); cfg[s+"/"+k] = v }
	h.set = func(s, k, v string) error { h.store(s, k, v); return nil }
	h.now = func() time.Time { return hookNow }
	return h, cfg
}

func TestHookValidTokenNeedsNoLock(t *testing.T) {
	h, _ := testHook(oauthTok("a", hookNow.Add(time.Hour)))
	l := &fakeLocker{}
	h.attach(l)
	h.getValue("box_1", "token")
	h.getValue("box_1", "type")
	if l.locks != 0 {
		t.Fatalf("locks %d", l.locks)
	}
}

func TestHookExpiredTokenRefreshedElsewhere(t *testing.T) {
	h, _ := testHook(oauthTok("old", hookNow.Add(-time.Hour)))
	fresh := oauthTok("fresh", hookNow.Add(time.Hour))
	l := &fakeLocker{newest: fresh}
	h.attach(l)
	if v, _ := h.getValue("box_1", "token"); v != fresh {
		t.Fatalf("got %q", v)
	}
	if l.locks != 1 || l.unlocks != 1 || len(h.held) != 0 {
		t.Fatalf("locks %d unlocks %d held %d", l.locks, l.unlocks, len(h.held))
	}
}

func TestHookHoldsLockUntilSave(t *testing.T) {
	old := oauthTok("old", hookNow.Add(-time.Hour))
	h, cfg := testHook(old)
	l := &fakeLocker{newest: old}
	h.attach(l)
	h.getValue("box_1", "token")
	h.getValue("box_1", "token") // rclone retries: no second lock while holding
	if l.locks != 1 || len(h.held) != 1 {
		t.Fatalf("locks %d held %d", l.locks, len(h.held))
	}
	fresh := oauthTok("fresh", hookNow.Add(time.Hour))
	if err := h.setValue("box_1", "token", fresh); err != nil {
		t.Fatal(err)
	}
	if len(l.saved) != 1 || l.saved[0] != "box="+fresh || len(h.held) != 0 || cfg["box_1/token"] != fresh {
		t.Fatalf("saved %v held %d", l.saved, len(h.held))
	}
}

func TestHookReleasesAfterHoldTimeout(t *testing.T) {
	old := oauthTok("old", hookNow.Add(-time.Hour))
	h, _ := testHook(old)
	h.holdTimeout = 10 * time.Millisecond
	l := &fakeLocker{newest: old}
	h.attach(l)
	h.getValue("box_1", "token")
	time.Sleep(50 * time.Millisecond)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.unlocks != 1 {
		t.Fatalf("unlocks %d", l.unlocks)
	}
}

func TestHookDegradesWithoutPlugin(t *testing.T) {
	old := oauthTok("old", hookNow.Add(-time.Hour))
	h, _ := testHook(old)
	if v, _ := h.getValue("box_1", "token"); v != old {
		t.Fatal("changed token without a plugin")
	}
	if err := h.setValue("box_1", "token", "new"); err != nil {
		t.Fatal(err)
	}
	l := &fakeLocker{err: errors.New("denied")}
	h.attach(l)
	if v, _ := h.getValue("box_1", "token"); v != "new" || len(h.held) != 0 {
		t.Fatalf("got %q held %d", v, len(h.held))
	}
}

func TestHookPushAndUnmanaged(t *testing.T) {
	h, cfg := testHook(oauthTok("old", hookNow.Add(-time.Hour)))
	l := &fakeLocker{}
	h.attach(l)
	fresh := oauthTok("fresh", hookNow.Add(time.Hour))
	h.push("box", fresh)
	h.push("other", "x")
	if cfg["box_1/token"] != fresh || len(cfg) != 2 {
		t.Fatalf("cfg %v", cfg)
	}
	if v, _ := h.getValue("box_1", "token"); v != fresh || l.locks != 0 {
		t.Fatal("pushed token not used")
	}
	_ = h.setValue("other_1", "token", "x")
	if len(l.saved) != 0 {
		t.Fatal("saved an unmanaged section")
	}
}

func TestHookSkipsLockRightAfterAFailure(t *testing.T) {
	h, _ := testHook(oauthTok("old", hookNow.Add(-time.Hour)))
	l := &fakeLocker{err: errors.New("denied")}
	h.attach(l)
	h.getValue("box_1", "token")
	h.getValue("box_1", "token")
	if l.locks != 1 {
		t.Fatalf("locks %d, want 1 within lockTimeout of a failure", l.locks)
	}
	h.now = func() time.Time { return hookNow.Add(2 * time.Minute) }
	h.getValue("box_1", "token")
	if l.locks != 2 {
		t.Fatalf("locks %d after the window", l.locks)
	}
}
