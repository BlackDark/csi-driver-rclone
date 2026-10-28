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
// OAuth token write-back (docs/adr/0004-oauth-token-write-back.md), after the
// handshake; <s> and <t> are base64 section name and token JSON:
//
//	client: token-subscribe                             server: token <s> <t> on every change
//	client: token-lock <s>       server: token-grant <s> <t> | token-deny <s>
//	client: token-save <s> <t>   server: token-saved <s> <sha256 of t> | token-save-failed <s>
//	                                                    stores the token, releases the lock
//	client: token-unlock <s>                            releases the lock unchanged
//
// The open connection is the Mount Pod's liveness signal; EOF means it is gone.
package handoff

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

	MsgTokenSubscribe Message = "token-subscribe"
	MsgTokenLock      Message = "token-lock"
	MsgTokenUnlock    Message = "token-unlock"
	MsgTokenSave      Message = "token-save"
	MsgTokenGrant     Message = "token-grant"
	MsgTokenDeny      Message = "token-deny"
	MsgToken          Message = "token"
	// MsgTokenSaved confirms a stored token-save; its token part is the token's sha256.
	MsgTokenSaved Message = "token-saved"
	// MsgTokenSaveFailed: the Token Secret write failed; the Mount Pod retries.
	MsgTokenSaveFailed Message = "token-save-failed"
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

// maxTokenLine bounds lines after the handshake; OAuth tokens can be a few KiB.
const maxTokenLine = 64 << 10

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
	case MsgHello, MsgAdopt, MsgFD, MsgOK, MsgReject, MsgReady, MsgDrained, MsgDrainTimeout,
		MsgTokenSubscribe, MsgTokenLock, MsgTokenUnlock, MsgTokenSave, MsgTokenGrant, MsgTokenDeny, MsgToken,
		MsgTokenSaved, MsgTokenSaveFailed:
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

var b64 = base64.StdEncoding

// tokenArg encodes a section name and, unless omitted, a token.
func tokenArg(section string, token ...string) string {
	parts := []string{b64.EncodeToString([]byte(section))}
	for _, t := range token {
		parts = append(parts, b64.EncodeToString([]byte(t)))
	}
	return strings.Join(parts, " ")
}

// tokenHash identifies a token in token-saved without sending it back.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// parseTokenArg decodes tokenArg; withToken requires the token part.
func parseTokenArg(arg string, withToken bool) (section, token string, err error) {
	parts := strings.Split(arg, " ")
	if (withToken && len(parts) != 2) || (!withToken && len(parts) != 1) {
		return "", "", errors.New("handoff: malformed token message")
	}
	sec, err := b64.DecodeString(parts[0])
	if err != nil || len(sec) == 0 {
		return "", "", errors.New("handoff: malformed token section")
	}
	if withToken {
		tok, err := b64.DecodeString(parts[1])
		if err != nil {
			return "", "", errors.New("handoff: malformed token")
		}
		token = string(tok)
	}
	return string(sec), token, nil
}
