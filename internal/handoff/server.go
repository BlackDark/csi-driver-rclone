package handoff

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// ServerMode says whether the server still holds an fd to hand out.
type ServerMode int

// Server modes.
const (
	// ModeFresh: the plugin just mounted; the first hello gets the fd, then the server switches to ModeAdopt.
	ModeFresh ServerMode = iota
	// ModeAdopt: the mount already exists (plugin restart); only adopt is accepted, no fd is sent.
	ModeAdopt
)

// EventKind classifies what happened on a connection.
type EventKind int

// Event kinds.
const (
	EventFDSent EventKind = iota
	EventAdopted
	EventRejected
	EventReady
	EventDrained
	EventDrainTimeout
	EventClosed // connection gone; Err is nil on clean EOF
)

func (k EventKind) String() string {
	return [...]string{"fd-sent", "adopted", "rejected", "ready", "drained", "drain-timeout", "closed"}[k]
}

// Event is reported for every protocol step. Conn identifies the connection, so a
// late Closed of a replaced connection can be told apart from the current one.
type Event struct {
	Kind EventKind
	Conn uint64
	Err  error
}

// ServerConfig configures Serve.
type ServerConfig struct {
	SockPath string
	FD       int // FUSE fd, used in ModeFresh; the caller keeps ownership
	Mode     ServerMode
	UID, GID int // socket owner (the Mount Pod's uid/gid); -1 leaves it unchanged
	// Tokens serves OAuth token write-back; nil denies every token lock.
	Tokens TokenHandler
}

// SocketPerm lets owner and group connect, nobody else.
const SocketPerm = 0o660

// Serve listens on cfg.SockPath until ctx is done, reporting to events. It removes a
// stale socket first and the socket on return. Every send on events respects ctx.
func Serve(ctx context.Context, cfg ServerConfig, events chan<- Event) error {
	l, err := listen(cfg)
	if err != nil {
		return err
	}
	s := &server{mode: cfg.Mode, fd: cfg.FD, tokens: cfg.Tokens, events: events, conns: map[uint64]net.Conn{}}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = l.Close()
		s.closeAll()
	}()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := l.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("handoff: accept: %w", err)
		}
		id := s.track(c)
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handle(ctx, id, c)
		}()
	}
}

func listen(cfg ServerConfig) (*net.UnixListener, error) {
	if err := os.Remove(cfg.SockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("handoff: remove stale socket: %w", err)
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: cfg.SockPath, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("handoff: listen: %w", err)
	}
	l.SetUnlinkOnClose(true)
	if err := os.Chmod(cfg.SockPath, SocketPerm); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("handoff: chmod socket: %w", err)
	}
	if err := os.Lchown(cfg.SockPath, cfg.UID, cfg.GID); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("handoff: chown socket: %w", err)
	}
	return l, nil
}

type server struct {
	mu     sync.Mutex
	mode   ServerMode
	fd     int
	nextID uint64
	conns  map[uint64]net.Conn
	tokens TokenHandler
	events chan<- Event
}

func (s *server) track(c net.Conn) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	s.conns[s.nextID] = c
	return s.nextID
}

func (s *server) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		_ = c.Close()
	}
}

func (s *server) emit(ctx context.Context, e Event) {
	select {
	case s.events <- e:
	case <-ctx.Done():
	}
}

func (s *server) handle(ctx context.Context, id uint64, c *net.UnixConn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, id)
		s.mu.Unlock()
		_ = c.Close()
	}()
	r := bufio.NewReaderSize(c, maxLine)
	if !s.handshake(ctx, id, c, r) {
		return
	}
	tc := newTokenConn(s.tokens, c)
	defer tc.close()
	for {
		line, err := readLine(r)
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				err = nil
			}
			s.emit(ctx, Event{Kind: EventClosed, Conn: id, Err: err})
			return
		}
		m, arg, err := parseLine(line)
		switch {
		case err == nil && statusMessages[m]:
			s.emit(ctx, Event{Kind: statusKind[m], Conn: id})
			continue
		case err == nil && clientTokenMessages[m]:
			if err = tc.handle(ctx, m, arg); err == nil {
				continue
			}
		}
		s.emit(ctx, Event{Kind: EventClosed, Conn: id, Err: fmt.Errorf("handoff: unexpected %q", truncate(line))})
		return
	}
}

var clientTokenMessages = map[Message]bool{
	MsgTokenSubscribe: true, MsgTokenLock: true, MsgTokenUnlock: true, MsgTokenSave: true,
}

// readLine reads one line of at most maxTokenLine bytes.
func readLine(r *bufio.Reader) (string, error) {
	var b []byte
	for {
		chunk, err := r.ReadSlice('\n')
		b = append(b, chunk...)
		if len(b) > maxTokenLine {
			return "", errors.New("handoff: line too long")
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return string(b), err
		}
	}
}

var statusKind = map[Message]EventKind{MsgReady: EventReady, MsgDrained: EventDrained, MsgDrainTimeout: EventDrainTimeout}

// handshake answers the first client line; false means the connection is done.
func (s *server) handshake(ctx context.Context, id uint64, c *net.UnixConn, r *bufio.Reader) bool {
	line, err := r.ReadString('\n')
	if err != nil {
		s.emit(ctx, Event{Kind: EventClosed, Conn: id, Err: fmt.Errorf("handoff: read hello: %w", err)})
		return false
	}
	m, _, _ := parseLine(line)
	s.mu.Lock()
	reply, kind, reason := decide(s.mode, m)
	if kind == EventFDSent {
		_, _, err = c.WriteMsgUnix(encode(MsgFD, ""), unix.UnixRights(s.fd), nil)
		if err == nil {
			s.mode = ModeAdopt
		}
	} else {
		_, err = c.Write(encode(reply, reason))
	}
	s.mu.Unlock()
	if err != nil {
		s.emit(ctx, Event{Kind: EventClosed, Conn: id, Err: fmt.Errorf("handoff: reply: %w", err)})
		return false
	}
	if kind == EventRejected {
		s.emit(ctx, Event{Kind: EventRejected, Conn: id, Err: &RejectError{Reason: reason}})
		return false
	}
	s.emit(ctx, Event{Kind: kind, Conn: id})
	return true
}

// decide is the handshake table: server mode × first client message.
func decide(mode ServerMode, m Message) (reply Message, kind EventKind, reason string) {
	switch {
	case m == MsgHello && mode == ModeFresh:
		return MsgFD, EventFDSent, ""
	case m == MsgAdopt && mode == ModeAdopt:
		return MsgOK, EventAdopted, ""
	case m == MsgHello:
		return MsgReject, EventRejected, ReasonMountExists
	case m == MsgAdopt:
		return MsgReject, EventRejected, ReasonNoMount
	}
	return MsgReject, EventRejected, ReasonProtocol
}
