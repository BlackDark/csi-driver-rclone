package mountspec

import (
	"errors"
	"fmt"
)

// ErrInvalid matches every Resolve error via errors.Is; callers map it to gRPC InvalidArgument.
var ErrInvalid = errors.New("invalid mount spec")

// Reason classifies why an input was rejected.
type Reason string

// Reasons returned in Error.
const (
	ReasonCredentialInContext Reason = "credentials are accepted only from secrets"
	ReasonUnknownOption       Reason = "option is not allowlisted"
	ReasonRejectedOption      Reason = "option is always rejected"
	ReasonNotForEphemeral     Reason = "not allowed for inline volumes"
	ReasonInvalidValue        Reason = "invalid value"
	ReasonMissing             Reason = "required key missing"
	ReasonConflict            Reason = "conflicting keys"
	ReasonInvalidConfig       Reason = "invalid rclone config"
)

// Error names the offending key, never its value, so secrets cannot leak through messages.
type Error struct {
	Key    string
	Reason Reason
	Detail string // never holds input values
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("mountspec: key %q: %s", e.Key, e.Reason)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Is makes errors.Is(err, ErrInvalid) true for every *Error.
func (e *Error) Is(target error) bool { return target == ErrInvalid }

func newErr(key string, r Reason, detail string) error {
	return &Error{Key: key, Reason: r, Detail: detail}
}
