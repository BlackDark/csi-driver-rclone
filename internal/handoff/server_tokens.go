package handoff

import (
	"context"
	"net"
	"sync"
)

// TokenHandler is the node plugin's side of OAuth token write-back for one mount.
// Sections are the names in the user's configData.
type TokenHandler interface {
	// Lock blocks until this mount may refresh section, then returns the newest token.
	Lock(ctx context.Context, section string) (string, error)
	// Unlock releases a lock taken by Lock.
	Unlock(section string)
	// Save stores a refreshed token and releases the lock, if held.
	Save(ctx context.Context, section, token string) error
	// Subscribe calls push for every token change until cancel.
	Subscribe(push func(section, token string)) (cancel func())
}

// tokenConn is the token state of one connection.
type tokenConn struct {
	h   TokenHandler
	c   net.Conn
	wmu sync.Mutex

	mu       sync.Mutex
	held     map[string]bool
	pending  map[string]bool // Lock running
	canceled map[string]bool // unlocked while Lock was running
	cancel   func()
	closed   bool
}

func newTokenConn(h TokenHandler, c net.Conn) *tokenConn {
	return &tokenConn{h: h, c: c, held: map[string]bool{}, pending: map[string]bool{}, canceled: map[string]bool{}}
}

func (t *tokenConn) write(m Message, arg string) {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	_, _ = t.c.Write(encode(m, arg))
}

// handle runs one token message; lock and save run in the background so status
// messages are never stuck behind a Lease wait.
func (t *tokenConn) handle(ctx context.Context, m Message, arg string) error {
	if m == MsgTokenSubscribe {
		return t.subscribe()
	}
	section, token, err := parseTokenArg(arg, m == MsgTokenSave)
	if err != nil {
		return err
	}
	if t.h == nil {
		switch m {
		case MsgTokenLock:
			t.write(MsgTokenDeny, tokenArg(section))
		case MsgTokenSave:
			// No write-back here: confirm so the Mount Pod does not retry forever.
			t.write(MsgTokenSaved, tokenArg(section, tokenHash(token)))
		}
		return nil
	}
	switch m {
	case MsgTokenLock:
		t.mu.Lock()
		busy := t.pending[section]
		t.pending[section] = true
		t.mu.Unlock()
		if busy {
			// A canceled request still waits; its grant would answer this one.
			t.write(MsgTokenDeny, tokenArg(section))
			return nil
		}
		go t.lock(ctx, section)
	case MsgTokenUnlock:
		if t.release(section) {
			t.h.Unlock(section)
		}
	case MsgTokenSave:
		t.release(section)
		// Not the connection's ctx: a closing Mount Pod must not lose its refreshed token.
		// The reply lets the Mount Pod keep the token until the Token Secret has it.
		go func() {
			if err := t.h.Save(context.WithoutCancel(ctx), section, token); err != nil {
				t.write(MsgTokenSaveFailed, tokenArg(section))
				return
			}
			t.write(MsgTokenSaved, tokenArg(section, tokenHash(token)))
		}()
	}
	return nil
}

func (t *tokenConn) subscribe() error {
	if t.h == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel == nil && !t.closed {
		t.cancel = t.h.Subscribe(func(section, token string) { t.write(MsgToken, tokenArg(section, token)) })
	}
	return nil
}

func (t *tokenConn) lock(ctx context.Context, section string) {
	token, err := t.h.Lock(ctx, section)
	if err != nil {
		t.mu.Lock()
		delete(t.pending, section)
		delete(t.canceled, section)
		t.mu.Unlock()
		t.write(MsgTokenDeny, tokenArg(section))
		return
	}
	t.mu.Lock()
	delete(t.pending, section)
	drop := t.closed || t.canceled[section]
	delete(t.canceled, section)
	if !drop {
		t.held[section] = true
	}
	t.mu.Unlock()
	if drop {
		t.h.Unlock(section)
		return
	}
	t.write(MsgTokenGrant, tokenArg(section, token))
}

// release forgets a held lock and reports whether it was held.
func (t *tokenConn) release(section string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	held := t.held[section]
	delete(t.held, section)
	if !held && t.pending[section] {
		t.canceled[section] = true
	}
	return held
}

// close releases every lock this connection holds and ends its subscription.
func (t *tokenConn) close() {
	t.mu.Lock()
	t.closed = true
	held := t.held
	t.held = map[string]bool{}
	cancel := t.cancel
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for section := range held {
		t.h.Unlock(section)
	}
}
