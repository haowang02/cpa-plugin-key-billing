package billing

import (
	"errors"
	"fmt"
)

type ErrorKind string

const (
	KindInvalid  ErrorKind = "invalid"
	KindNotFound ErrorKind = "not_found"
	KindConflict ErrorKind = "conflict"
)

// Msg is operator-facing; callers classify the error with KindOf.
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
