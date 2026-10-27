package handoff

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// sockPath stays short: darwin caps unix socket paths at 104 bytes.
func sockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ho")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "h.sock")
}

func startServer(t *testing.T, cfg ServerConfig) (chan Event, context.CancelFunc, chan error) {
	t.Helper()
	cfg.UID, cfg.GID = os.Getuid(), os.Getgid()
	events := make(chan Event, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, events) }()
	t.Cleanup(cancel)
	waitFor(t, func() bool { _, err := os.Stat(cfg.SockPath); return err == nil })
	return events, cancel, done
}

func pipe(t *testing.T) (r, w *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.Close(), w.Close() })
	return r, w
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func next(t *testing.T, events chan Event, want EventKind) Event {
	t.Helper()
	select {
	case e := <-events:
		if e.Kind != want {
			t.Fatalf("got event %s (%v), want %s", e.Kind, e.Err, want)
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatalf("no event, want %s", want)
	}
	return Event{}
}

func dial(t *testing.T, path string) *Conn {
	t.Helper()
	c, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestFreshHandoffPassesWorkingFD(t *testing.T) {
	r, w := pipe(t)
	path := sockPath(t)
	events, cancel, done := startServer(t, ServerConfig{SockPath: path, FD: int(r.Fd()), Mode: ModeFresh})

	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != SocketPerm {
		t.Fatalf("socket perm: %v %v", st.Mode(), err)
	}

	c := dial(t, path)
	fd, err := c.ReceiveFD()
	if err != nil {
		t.Fatal(err)
	}
	next(t, events, EventFDSent)
	got := os.NewFile(uintptr(fd), "received")
	defer func() { _ = got.Close() }()
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if _, err := io.ReadFull(got, buf); err != nil || buf[0] != 'x' {
		t.Fatalf("read through passed fd: %q %v", buf, err)
	}

	for _, m := range []Message{MsgReady, MsgDrained, MsgDrainTimeout} {
		if err := c.Send(m); err != nil {
			t.Fatal(err)
		}
	}
	next(t, events, EventReady)
	next(t, events, EventDrained)
	next(t, events, EventDrainTimeout)

	// The fd is handed out once: a second hello is rejected.
	_, err = dial(t, path).ReceiveFD()
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("second hello: %v", err)
	}
	next(t, events, EventRejected)

	_ = c.Close()
	if e := next(t, events, EventClosed); e.Err != nil {
		t.Fatalf("clean close: %v", e.Err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket not removed: %v", err)
	}
}

func TestHandshakeRejects(t *testing.T) {
	path := sockPath(t)
	events, _, _ := startServer(t, ServerConfig{SockPath: path, Mode: ModeAdopt})
	_, err := dial(t, path).ReceiveFD()
	var rej *RejectError
	if !errors.As(err, &rej) || rej.Reason != ReasonMountExists {
		t.Fatalf("hello in adopt mode: %v", err)
	}
	next(t, events, EventRejected)
	if err := dial(t, path).Adopt(); err != nil {
		t.Fatal(err)
	}
	next(t, events, EventAdopted)

	path2 := sockPath(t)
	r, _ := pipe(t)
	_, _, _ = startServer(t, ServerConfig{SockPath: path2, FD: int(r.Fd()), Mode: ModeFresh})
	if err := dial(t, path2).Adopt(); !errors.As(err, &rej) || rej.Reason != ReasonNoMount {
		t.Fatalf("adopt in fresh mode: %v", err)
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		mode   ServerMode
		msg    Message
		kind   EventKind
		reason string
	}{
		{ModeFresh, MsgHello, EventFDSent, ""},
		{ModeAdopt, MsgAdopt, EventAdopted, ""},
		{ModeAdopt, MsgHello, EventRejected, ReasonMountExists},
		{ModeFresh, MsgAdopt, EventRejected, ReasonNoMount},
		{ModeFresh, MsgReady, EventRejected, ReasonProtocol},
	}
	for _, c := range cases {
		_, kind, reason := decide(c.mode, c.msg)
		if kind != c.kind || reason != c.reason {
			t.Errorf("%v/%s: got %s %q", c.mode, c.msg, kind, reason)
		}
	}
}

func TestParseLine(t *testing.T) {
	if m, arg, err := parseLine("reject no-mount\n"); err != nil || m != MsgReject || arg != ReasonNoMount {
		t.Fatalf("got %q %q %v", m, arg, err)
	}
	if _, _, err := parseLine("bogus\n"); err == nil {
		t.Fatal("want error")
	}
}

func TestSessionAdoptsAfterServerRestart(t *testing.T) {
	r, _ := pipe(t)
	path := sockPath(t)
	events, cancel, done := startServer(t, ServerConfig{SockPath: path, FD: int(r.Fd()), Mode: ModeFresh})
	c := dial(t, path)
	fd, err := c.ReceiveFD()
	if err != nil {
		t.Fatal(err)
	}
	_ = os.NewFile(uintptr(fd), "fd").Close()
	next(t, events, EventFDSent)

	s := NewSession(path, c, 20*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	ran := make(chan struct{})
	go func() { s.Run(ctx); close(ran) }()

	cancel() // plugin restart
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.Send(MsgDrained); err == nil {
		t.Log("send raced the close; still remembered")
	}

	events2, _, _ := startServer(t, ServerConfig{SockPath: path, Mode: ModeAdopt})
	next(t, events2, EventAdopted)
	next(t, events2, EventDrained)

	stop()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	next(t, events2, EventClosed)
}
