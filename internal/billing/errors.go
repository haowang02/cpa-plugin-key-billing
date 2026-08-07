package billing

import (
	"errors"
	"fmt"
)

// ErrorKind classifies an administrative failure without pulling HTTP concerns
// into the billing domain. The Management API layer maps kinds to status codes.
type ErrorKind string

const (
	// KindInvalid means the caller sent something the domain refuses.
	KindInvalid ErrorKind = "invalid"
	// KindNotFound means the referenced record does not exist.
	KindNotFound ErrorKind = "not_found"
	// KindConflict means the record collides with one that already exists.
	KindConflict ErrorKind = "conflict"
)

// Error is an administrative failure. Msg is written for an operator to read,
// so it carries no kind prefix; callers classify with KindOf instead.
type Error struct {
	Kind ErrorKind
	Msg  string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Msg
}

// KindOf reports the kind of err, or an empty kind when it is not classified.
func KindOf(err error) ErrorKind {
	var typed *Error
	if errors.As(err, &typed) && typed != nil {
		return typed.Kind
	}
	return ""
}

func invalidf(format string, args ...any) error {
	return &Error{Kind: KindInvalid, Msg: fmt.Sprintf(format, args...)}
}

func notFoundf(format string, args ...any) error {
	return &Error{Kind: KindNotFound, Msg: fmt.Sprintf(format, args...)}
}

func conflictf(format string, args ...any) error {
	return &Error{Kind: KindConflict, Msg: fmt.Sprintf(format, args...)}
}
