package handoff

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrTokenDenied means the node plugin could not grant the token lock.
var ErrTokenDenied = errors.New("handoff: token lock denied")

// ErrNotConnected means there is no connection to the node plugin right now.
var ErrNotConnected = errors.New("handoff: not connected")

// TokenListener receives a token pushed by the node plugin.
type TokenListener func(section, token string)

type lockReply struct {
	token string
	err   error
}

// saveRetry is the wait before resending a token whose Token Secret write failed.
const saveRetry = 5 * time.Second

// tokenState is the Session's token write-back state; its own mutex, so a pushed
// token is never stuck behind a send.
type tokenState struct {
	mu       sync.Mutex
	listener TokenListener
	pending  map[string]chan lockReply
	// unsaved holds refreshed tokens until the node plugin confirms the Token Secret
	// write (token-saved): a token lost here is a spent refresh token for the next
	// Mount Pod. Resent after a failed write and after every reconnect.
	unsaved map[string]string
	// settled is closed and replaced whenever unsaved becomes empty.
	settled chan struct{}
}

func (t *tokenState) failPending() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for section, ch := range t.pending {
		ch <- lockReply{err: ErrNotConnected}
		delete(t.pending, section)
	}
}

func (t *tokenState) deliver(section string, r lockReply) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ch, ok := t.pending[section]; ok {
		ch <- r
		delete(t.pending, section)
	}
}

// keep records token as the newest unconfirmed one of section.
func (t *tokenState) keep(section, token string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.unsaved == nil {
		t.unsaved = map[string]string{}
	}
	t.unsaved[section] = token
}

// confirm drops section's unsaved token if hash matches it; an older confirmation
// leaves a newer unsaved token queued.
func (t *tokenState) confirm(section, hash string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if tok, ok := t.unsaved[section]; ok && tokenHash(tok) == hash {
		delete(t.unsaved, section)
	}
	if len(t.unsaved) == 0 && t.settled != nil {
		close(t.settled)
		t.settled = nil
	}
}

func (t *tokenState) unsavedToken(section string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tok, ok := t.unsaved[section]
	return tok, ok
}

func (t *tokenState) snapshot() map[string]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]string, len(t.unsaved))
	for s, tok := range t.unsaved {
		out[s] = tok
	}
	return out
}

// WatchTokens sets the listener for pushed tokens; call before Run. Every connection
// subscribes, so a restarted node plugin keeps pushing.
func (s *Session) WatchTokens(l TokenListener) {
	s.tokens.mu.Lock()
	defer s.tokens.mu.Unlock()
	s.tokens.listener = l
}

// LockToken asks the node plugin for the refresh lock of section and returns the
// newest token. Errors mean "refresh without the lock".
func (s *Session) LockToken(ctx context.Context, section string) (string, error) {
	ch := make(chan lockReply, 1)
	s.tokens.mu.Lock()
	if s.tokens.pending == nil {
		s.tokens.pending = map[string]chan lockReply{}
	}
	if _, busy := s.tokens.pending[section]; busy {
		s.tokens.mu.Unlock()
		return "", errors.New("handoff: token lock already pending")
	}
	s.tokens.pending[section] = ch
	s.tokens.mu.Unlock()
	if err := s.sendArg(MsgTokenLock, tokenArg(section)); err != nil {
		s.tokens.deliver(section, lockReply{})
		return "", err
	}
	select {
	case r := <-ch:
		return r.token, r.err
	case <-ctx.Done():
		s.tokens.deliver(section, lockReply{})
		// The grant may still come; release it so other mounts are not blocked.
		_ = s.UnlockToken(section)
		return "", ctx.Err()
	}
}

// UnlockToken releases the lock of section without a new token.
func (s *Session) UnlockToken(section string) error {
	return s.sendArg(MsgTokenUnlock, tokenArg(section))
}

// SaveToken reports a refreshed token; it also releases the lock. The token stays
// queued until the node plugin confirms the write, and is resent until it does.
func (s *Session) SaveToken(section, token string) error {
	s.tokens.keep(section, token)
	return s.sendArg(MsgTokenSave, tokenArg(section, token))
}

// WaitTokensSaved blocks until every refreshed token is confirmed or ctx is done;
// it reports whether all were confirmed. A stopping Mount Pod calls it last.
func (s *Session) WaitTokensSaved(ctx context.Context) bool {
	s.tokens.mu.Lock()
	if len(s.tokens.unsaved) == 0 {
		s.tokens.mu.Unlock()
		return true
	}
	if s.tokens.settled == nil {
		s.tokens.settled = make(chan struct{})
	}
	ch := s.tokens.settled
	s.tokens.mu.Unlock()
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Session) sendArg(m Message, arg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return ErrNotConnected
	}
	return s.conn.send(m, arg)
}

// resendLater resends section's unsaved token after saveRetry, if still unsaved.
func (s *Session) resendLater(section string) {
	time.AfterFunc(saveRetry, func() {
		if tok, ok := s.tokens.unsavedToken(section); ok {
			if err := s.sendArg(MsgTokenSave, tokenArg(section, tok)); err != nil {
				s.log.Warn("resend refreshed token", "section", section, "err", err)
			}
		}
	})
}

// subscribeTokens starts token pushes on a new connection and resends unsaved tokens.
func (s *Session) subscribeTokens(conn *Conn) {
	s.tokens.mu.Lock()
	watching := s.tokens.listener != nil
	s.tokens.mu.Unlock()
	unsaved := s.tokens.snapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
	if watching {
		if err := conn.send(MsgTokenSubscribe, ""); err != nil {
			s.log.Warn("subscribe to token updates", "err", err)
		}
	}
	for section, token := range unsaved {
		if err := conn.send(MsgTokenSave, tokenArg(section, token)); err != nil {
			s.log.Warn("resend refreshed token", "section", section, "err", err)
		}
	}
}

// dispatch handles one server line after the handshake.
func (s *Session) dispatch(m Message, arg string) {
	switch m {
	case MsgTokenGrant, MsgToken:
		section, token, err := parseTokenArg(arg, true)
		if err != nil {
			return
		}
		if m == MsgTokenGrant {
			s.tokens.deliver(section, lockReply{token: token})
			return
		}
		s.tokens.mu.Lock()
		l := s.tokens.listener
		s.tokens.mu.Unlock()
		if l != nil {
			l(section, token)
		}
	case MsgTokenDeny:
		if section, _, err := parseTokenArg(arg, false); err == nil {
			s.tokens.deliver(section, lockReply{err: ErrTokenDenied})
		}
	case MsgTokenSaved:
		if section, hash, err := parseTokenArg(arg, true); err == nil {
			s.tokens.confirm(section, hash)
		}
	case MsgTokenSaveFailed:
		if section, _, err := parseTokenArg(arg, false); err == nil {
			s.log.Warn("token write-back: node plugin could not store the refreshed token, retrying", "section", section)
			s.resendLater(section)
		}
	}
}
