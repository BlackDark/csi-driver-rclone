package handoff

import (
	"context"
	"errors"
	"sync"
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

// tokenState is the Session's token write-back state; its own mutex, so a pushed
// token is never stuck behind a send.
type tokenState struct {
	mu       sync.Mutex
	listener TokenListener
	pending  map[string]chan lockReply
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

// SaveToken reports a refreshed token; it also releases the lock.
func (s *Session) SaveToken(section, token string) error {
	return s.sendArg(MsgTokenSave, tokenArg(section, token))
}

func (s *Session) sendArg(m Message, arg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return ErrNotConnected
	}
	return s.conn.send(m, arg)
}

func (s *Session) subscribeTokens(conn *Conn) {
	s.tokens.mu.Lock()
	watching := s.tokens.listener != nil
	s.tokens.mu.Unlock()
	if !watching {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := conn.send(MsgTokenSubscribe, ""); err != nil {
		s.log.Warn("subscribe to token updates", "err", err)
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
	}
}
