// Package messages attaches translation metadata at creation and never interprets user data.
package messages

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed catalog.json
var catalogJSON []byte

type catalogEntry struct {
	Key     string   `json:"key"`
	Formats []string `json:"formats"`
}

var catalog = func() map[string]catalogEntry {
	var entries map[string]catalogEntry
	if err := json.Unmarshal(catalogJSON, &entries); err != nil {
		panic(err)
	}
	return entries
}()

type Message struct {
	Key    string            `json:"message_key,omitempty"`
	Params map[string]string `json:"message_params,omitempty"`
	Text   string            `json:"-"`
}

func (m Message) IsZero() bool { return m.Key == "" }

func New(format string, args ...any) Message {
	text := format
	if len(args) > 0 {
		text = fmt.Sprintf(strings.ReplaceAll(format, "%w", "%v"), args...)
	}
	result := Message{Text: text}
	entry, ok := catalog[format]
	if !ok || len(entry.Formats) != len(args) {
		return result
	}
	result.Key = entry.Key
	if len(args) > 0 {
		result.Params = make(map[string]string, len(args))
		for i, arg := range args {
			result.Params[fmt.Sprintf("v%d", i)] = fmt.Sprintf(entry.Formats[i], arg)
		}
	}
	return result
}

// Literal keeps percent signs in raw diagnostics from being read as format verbs.
func Literal(text string) Message { return New(text, []any{}...) }

type describedError struct {
	error
	detail Message
}

func (e *describedError) MessageDetail() Message { return e.detail }
func (e *describedError) Unwrap() error          { return e.error }

func Errorf(format string, args ...any) error {
	err := fmt.Errorf(format, args...)
	detail := New(format, args...)
	detail.Text = err.Error()
	return &describedError{error: err, detail: detail}
}

func FromError(err error) Message {
	if err == nil {
		return Message{}
	}
	// Only the error owning the full message may translate it; matching raw upstream text could translate user content.
	if described, ok := err.(interface{ MessageDetail() Message }); ok {
		return described.MessageDetail()
	}
	return Message{Text: err.Error()}
}
