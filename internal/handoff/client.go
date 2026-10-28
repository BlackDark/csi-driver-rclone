package handoff

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"golang.org/x/sys/unix"
)

// Conn is the Mount Pod side of one handoff connection.
type Conn struct {
	c *net.UnixConn
}

// Dial connects to the node plugin's socket.
func Dial(ctx context.Context, sockPath string) (*Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("handoff: dial: %w", err)
	}
	return &Conn{c: c.(*net.UnixConn)}, nil
}

// ReceiveFD sends hello and returns the FUSE fd passed by the server. The caller owns it.
func (c *Conn) ReceiveFD() (int, error) {
	if err := c.Send(MsgHello); err != nil {
		return -1, err
	}
	m, arg, fds, err := c.readReply()
	if err != nil {
		return -1, err
	}
	if m != MsgFD || len(fds) != 1 {
		closeFDs(fds)
		return -1, replyErr(m, arg)
	}
	return fds[0], nil
}

// Adopt tells a restarted server that this Mount Pod already serves the mount.
func (c *Conn) Adopt() error {
	if err := c.Send(MsgAdopt); err != nil {
		return err
	}
	m, arg, fds, err := c.readReply()
	closeFDs(fds)
	if err != nil {
		return err
	}
	if m != MsgOK {
		return replyErr(m, arg)
	}
	return nil
}

// Send writes one message line.
func (c *Conn) Send(m Message) error { return c.send(m, "") }

func (c *Conn) send(m Message, arg string) error {
	if _, err := c.c.Write(encode(m, arg)); err != nil {
		return fmt.Errorf("handoff: send %s: %w", m, err)
	}
	return nil
}

// readLoop passes every server line to f until the server closes the connection;
// nil on clean EOF. Only for after the handshake: it buffers past the first line.
func (c *Conn) readLoop(f func(Message, string)) error {
	r := bufio.NewReaderSize(c.c, maxLine)
	for {
		line, err := readLine(r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if m, arg, perr := parseLine(line); perr == nil {
			f(m, arg)
		}
	}
}

// Close closes the connection.
func (c *Conn) Close() error { return c.c.Close() }

func replyErr(m Message, arg string) error {
	if m == MsgReject {
		return &RejectError{Reason: arg}
	}
	return fmt.Errorf("handoff: unexpected reply %q", m)
}

// readReply reads exactly one line with any passed fds. ReadMsgUnix, not a buffered
// reader, so the SCM_RIGHTS control message attached to the reply is not lost.
func (c *Conn) readReply() (Message, string, []int, error) {
	var line []byte
	var fds []int
	buf := make([]byte, maxLine)
	oob := make([]byte, unix.CmsgSpace(4*4))
	for !bytes.HasSuffix(line, []byte("\n")) {
		n, oobn, _, _, err := c.c.ReadMsgUnix(buf, oob)
		got, perr := parseRights(oob[:oobn])
		fds = append(fds, got...)
		if perr != nil {
			closeFDs(fds)
			return "", "", nil, perr
		}
		line = append(line, buf[:n]...)
		if err != nil || n == 0 || len(line) > maxLine {
			closeFDs(fds)
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return "", "", nil, fmt.Errorf("handoff: read reply: %w", err)
		}
	}
	m, arg, err := parseLine(string(line))
	if err != nil {
		closeFDs(fds)
		return "", "", nil, err
	}
	return m, arg, fds, nil
}

func parseRights(oob []byte) ([]int, error) {
	if len(oob) == 0 {
		return nil, nil
	}
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, fmt.Errorf("handoff: control message: %w", err)
	}
	var fds []int
	for i := range msgs {
		got, err := unix.ParseUnixRights(&msgs[i])
		if err != nil {
			continue
		}
		fds = append(fds, got...)
	}
	return fds, nil
}

func closeFDs(fds []int) {
	for _, fd := range fds {
		_ = unix.Close(fd)
	}
}
