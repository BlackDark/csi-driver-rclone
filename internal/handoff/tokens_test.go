package handoff

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeTokens struct {
	mu       sync.Mutex
	token    string
	denied   bool
	unlocks  []string
	saved    []string
	pushes   []func(string, string)
	gotLock  chan struct{}
	lockWait chan struct{}
}

func (f *fakeTokens) Lock(_ context.Context, section string) (string, error) {
	f.mu.Lock()
	wait, denied := f.lockWait, f.denied
	f.mu.Unlock()
	if f.gotLock != nil {
		f.gotLock <- struct{}{}
	}
	if wait != nil {
		<-wait
	}
	if denied {
		return "", errors.New("no lease")
	}
	return f.token + "@" + section, nil
}

func (f *fakeTokens) Unlock(section string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unlocks = append(f.unlocks, section)
}

func (f *fakeTokens) Save(_ context.Context, section, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, section+"="+token)
	return nil
}

func (f *fakeTokens) Subscribe(push func(string, string)) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushes = append(f.pushes, push)
	return func() {}
}

func (f *fakeTokens) snapshot() (unlocks, saved []string, pushes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.unlocks...), append([]string(nil), f.saved...), len(f.pushes)
}

// tokenSession hands off an fd and returns a running Session over a server with h.
func tokenSession(t *testing.T, h TokenHandler, l TokenListener) (*Session, chan Event) {
	t.Helper()
	r, _ := pipe(t)
	path := sockPath(t)
	events, _, _ := startServer(t, ServerConfig{SockPath: path, FD: int(r.Fd()), Mode: ModeFresh, Tokens: h})
	c := dial(t, path)
	fd, err := c.ReceiveFD()
	if err != nil {
		t.Fatal(err)
	}
	_ = os.NewFile(uintptr(fd), "fd").Close()
	next(t, events, EventFDSent)
	s := NewSession(path, c, 20*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if l != nil {
		s.WatchTokens(l)
	}
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	go s.Run(ctx)
	return s, events
}

func TestTokenLockSaveAndPush(t *testing.T) {
	h := &fakeTokens{token: `{"refresh_token":"r 1"}`}
	got := make(chan string, 1)
	s, events := tokenSession(t, h, func(section, token string) { got <- section + "=" + token })

	tok, err := s.LockToken(context.Background(), "my box")
	if err != nil || tok != `{"refresh_token":"r 1"}@my box` {
		t.Fatalf("lock %q %v", tok, err)
	}
	if err := s.SaveToken("my box", "new"); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(MsgReady); err != nil {
		t.Fatal(err)
	}
	next(t, events, EventReady) // status messages still flow beside token messages
	waitFor(t, func() bool { _, saved, pushes := h.snapshot(); return len(saved) == 1 && pushes == 1 })
	if unlocks, saved, _ := h.snapshot(); saved[0] != "my box=new" || len(unlocks) != 0 {
		t.Fatalf("saved %v unlocks %v", saved, unlocks)
	}
	big := strings.Repeat("x", 8000)
	h.pushes[0]("my box", big)
	select {
	case p := <-got:
		if p != "my box="+big {
			t.Fatalf("push %q", p[:20])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no push")
	}
}

func TestTokenLockDeniedAndUnlock(t *testing.T) {
	h := &fakeTokens{denied: true}
	s, _ := tokenSession(t, h, nil)
	if _, err := s.LockToken(context.Background(), "a"); !errors.Is(err, ErrTokenDenied) {
		t.Fatalf("err %v", err)
	}
	h.mu.Lock()
	h.denied = false
	h.mu.Unlock()
	if _, err := s.LockToken(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.UnlockToken("a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { unlocks, _, _ := h.snapshot(); return len(unlocks) == 1 })
}

func TestTokenLockWithoutHandlerIsDenied(t *testing.T) {
	s, _ := tokenSession(t, nil, nil)
	if _, err := s.LockToken(context.Background(), "a"); !errors.Is(err, ErrTokenDenied) {
		t.Fatalf("err %v", err)
	}
}

func TestTokenLockCanceledIsReleased(t *testing.T) {
	h := &fakeTokens{gotLock: make(chan struct{}, 1), lockWait: make(chan struct{})}
	s, _ := tokenSession(t, h, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-h.gotLock; cancel() }()
	if _, err := s.LockToken(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	time.Sleep(50 * time.Millisecond) // the unlock reaches the server before the grant
	close(h.lockWait)
	waitFor(t, func() bool { unlocks, _, _ := h.snapshot(); return len(unlocks) == 1 })
}

func TestTokenLocksReleasedOnDisconnect(t *testing.T) {
	h := &fakeTokens{}
	s, events := tokenSession(t, h, nil)
	if _, err := s.LockToken(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	next(t, events, EventClosed)
	waitFor(t, func() bool { unlocks, _, _ := h.snapshot(); return len(unlocks) == 1 && unlocks[0] == "a" })
}

func TestParseTokenArg(t *testing.T) {
	sec, tok, err := parseTokenArg(tokenArg("s", "t"), true)
	if err != nil || sec != "s" || tok != "t" {
		t.Fatalf("%q %q %v", sec, tok, err)
	}
	for _, bad := range []string{"", "!!", tokenArg("s"), tokenArg("s", "t") + " x"} {
		if _, _, err := parseTokenArg(bad, true); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
