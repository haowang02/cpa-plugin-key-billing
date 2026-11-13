package billing

import (
	"errors"

	"cpa-key-billing/internal/messages"
)

type ErrorKind string

const (
	KindInvalid  ErrorKind = "invalid"
	KindNotFound ErrorKind = "not_found"
	KindConflict ErrorKind = "conflict"
)

type Error struct {
	Kind   ErrorKind
	Msg    string
	Detail messages.Message
}

func (e *Error) MessageDetail() messages.Message {
	if e.Detail.Key != "" {
		return e.Detail
	}
	return messages.Literal(e.Msg)
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
	detail := messages.New(format, args...)
	return &Error{Kind: KindInvalid, Msg: detail.Text, Detail: detail}
}

func notFoundf(format string, args ...any) error {
	detail := messages.New(format, args...)
	return &Error{Kind: KindNotFound, Msg: detail.Text, Detail: detail}
}

func conflictf(format string, args ...any) error {
	detail := messages.New(format, args...)
	return &Error{Kind: KindConflict, Msg: detail.Text, Detail: detail}
}
