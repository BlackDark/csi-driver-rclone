package handoff

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Session keeps a Mount Pod connected to the node plugin after the fd handoff. When
// the plugin restarts it redials and sends adopt, then repeats the last drain result.
type Session struct {
	sockPath string
	backoff  time.Duration
	log      *slog.Logger

	mu     sync.Mutex
	conn   *Conn
	last   Message // last drain result, resent after adopt
	closed bool
}

// NewSession takes over conn, which already finished its handshake.
func NewSession(sockPath string, conn *Conn, backoff time.Duration, log *slog.Logger) *Session {
	return &Session{sockPath: sockPath, conn: conn, backoff: backoff, log: log}
}

// Send sends m on the current connection. A drain result is remembered and resent
// after a reconnect, so an error here is not fatal.
func (s *Session) Send(m Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m == MsgDrained || m == MsgDrainTimeout {
		s.last = m
	}
	if s.conn == nil {
		return nil
	}
	return s.conn.Send(m)
}

// Run holds the connection open and reconnects until ctx is done or Close is called.
func (s *Session) Run(ctx context.Context) {
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	for {
		conn := s.current()
		if conn == nil {
			return
		}
		err := conn.Wait()
		if s.drop(conn) {
			return
		}
		s.log.Warn("handoff connection lost, reconnecting", "err", err)
		if !s.reconnect(ctx) {
			return
		}
	}
}

// Close ends the session and its connection.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

func (s *Session) current() *Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn
}

// drop forgets a dead connection; true means the session is closed.
func (s *Session) drop(conn *Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = conn.Close()
	if s.conn == conn {
		s.conn = nil
	}
	return s.closed
}

func (s *Session) reconnect(ctx context.Context) bool {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
		conn, err := s.adopt(ctx)
		if err == nil {
			return s.install(conn)
		}
		s.log.Debug("handoff adopt failed", "err", err)
		t.Reset(s.backoff)
	}
}

func (s *Session) adopt(ctx context.Context) (*Conn, error) {
	conn, err := Dial(ctx, s.sockPath)
	if err != nil {
		return nil, err
	}
	if err := conn.Adopt(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (s *Session) install(conn *Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		_ = conn.Close()
		return false
	}
	s.conn = conn
	s.log.Info("handoff adopted")
	if s.last != "" {
		if err := conn.Send(s.last); err != nil {
			s.log.Warn("resend drain result", "err", err)
		}
	}
	return true
}
