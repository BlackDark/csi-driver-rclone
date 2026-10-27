// Package handoff is the unix-socket protocol between the node plugin (server) and a
// Mount Pod (client): FUSE fd passing via SCM_RIGHTS, status messages and liveness.
//
// Framing: one message per line, "\n"-terminated, ASCII. Sequence:
//
//	client: hello            server: fd (+SCM_RIGHTS)   fresh mount
//	client: adopt            server: ok                 server already has the mount
//	                         server: reject <reason>    then closes
//	client: ready | drained | drain-timeout             any time after the handshake
//
// The open connection is the Mount Pod's liveness signal; EOF means it is gone.
package handoff

import (
	"errors"
	"fmt"
	"strings"
)

// Message is one protocol line without its newline.
type Message string

// Protocol messages.
const (
	MsgHello        Message = "hello"
	MsgAdopt        Message = "adopt"
	MsgFD           Message = "fd"
	MsgOK           Message = "ok"
	MsgReject       Message = "reject"
	MsgReady        Message = "ready"
	MsgDrained      Message = "drained"
	MsgDrainTimeout Message = "drain-timeout"
)

// Reject reasons sent by the server.
const (
	ReasonMountExists = "mount-exists" // hello, but the fd was already handed out
	ReasonNoMount     = "no-mount"     // adopt, but the server has no mount to adopt
	ReasonProtocol    = "protocol"     // unexpected first message
)

// ErrRejected matches every *RejectError.
var ErrRejected = errors.New("handoff rejected")

// RejectError carries the server's reject reason.
type RejectError struct{ Reason string }

func (e *RejectError) Error() string { return "handoff rejected: " + e.Reason }

// Is makes errors.Is(err, ErrRejected) true for every *RejectError.
func (e *RejectError) Is(target error) bool { return target == ErrRejected }

const maxLine = 256

var statusMessages = map[Message]bool{MsgReady: true, MsgDrained: true, MsgDrainTimeout: true}

func encode(m Message, arg string) []byte {
	if arg != "" {
		return []byte(string(m) + " " + arg + "\n")
	}
	return []byte(string(m) + "\n")
}

// parseLine splits "msg [arg]" and rejects anything not in the protocol.
func parseLine(line string) (Message, string, error) {
	line = strings.TrimRight(line, "\r\n")
	head, arg, _ := strings.Cut(line, " ")
	m := Message(head)
	switch m {
	case MsgHello, MsgAdopt, MsgFD, MsgOK, MsgReject, MsgReady, MsgDrained, MsgDrainTimeout:
		return m, arg, nil
	}
	return "", "", fmt.Errorf("handoff: unknown message %q", truncate(head))
}

func truncate(s string) string {
	if len(s) > 32 {
		return s[:32]
	}
	return s
}
