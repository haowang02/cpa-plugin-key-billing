package plugin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// maxFailureReason bounds what one error contributes to the plugin log, which
// is held in memory and travels to the panel whole. An upstream answering with
// a page of prose would otherwise crowd out the entries around it.
const maxFailureReason = 300

// responseFailure reports what a response says went wrong, in whichever shape
// the client's format states it, and "" when it carries no error. It reads the
// objects a body was already parsed into, so watching for errors costs nothing
// beyond the walk.
func responseFailure(objects []map[string]any) string {
	reason := ""
	for _, object := range objects {
		// A stream ends at its last error, and that is the one that ended the
		// request; an earlier frame may only have reported a retried attempt.
		if message := objectFailure(object); message != "" {
			reason = message
		}
	}
	return truncateReason(reason)
}

// The formats state an error in three ways: an error member on an ordinary
// frame (OpenAI, Gemini), an error member on a frame that also declares itself
// one (Anthropic), and a frame that is nothing but the error (the proxy's own
// stream errors). All three are read here rather than by format, because the
// body reaching a client has already been translated into its format and the
// proxy may have replaced it with an error of its own making.
func objectFailure(object map[string]any) string {
	// The message and the code that qualifies it are read from one node, so a
	// nested error is never described by an unrelated code beside it.
	if node := firstObjectMap(object, []string{"error"}, []string{"response", "error"}); node != nil {
		if message := valueString(node["message"]); message != "" {
			return qualify(message, firstString(node, []string{"type"}, []string{"status"}, []string{"code"}))
		}
	}
	// An error stated as nothing but its text.
	if message := valueString(object["error"]); message != "" {
		return message
	}
	// A frame that is the error rather than one carrying it.
	if valueString(object["type"]) == "error" {
		if message := valueString(object["message"]); message != "" {
			return qualify(message, valueString(object["code"]))
		}
	}
	return ""
}

// completionFailure describes a failure by what the host reported the request
// ended as. The response hooks only ever see a body that reached the client, so
// a request that ended without one — an upstream that closed the connection, a
// transport error the proxy raised on its own — has this as its sole account.
func completionFailure(statusCode int, errorText string) string {
	message := hostErrorMessage(errorText)
	status := ""
	// The host reports no status for a canceled request, and 200 for one that
	// failed after the upstream had already answered.
	if statusCode >= http.StatusBadRequest && statusCode <= 599 {
		status = "HTTP " + strconv.Itoa(statusCode)
	}
	switch {
	case message == "":
		return status
	case status == "" || strings.Contains(message, status):
		return truncateReason(message)
	default:
		return truncateReason(status + "：" + message)
	}
}

// The host states an error as the upstream's body, that body behind a prefix of
// the proxy's own, or plain prose when there was no body at all. The first two
// are read the way a response is, so a failure reads the same whether the client
// was shown it or not.
func hostErrorMessage(errorText string) string {
	errorText = strings.TrimSpace(errorText)
	if errorText == "" {
		return ""
	}
	if message := responseFailure(responseObjects([]byte(errorText))); message != "" {
		return message
	}
	if start := strings.IndexByte(errorText, '{'); start > 0 {
		if message := responseFailure(responseObjects([]byte(errorText[start:]))); message != "" {
			return message
		}
	}
	return errorText
}

// A code says which error this is, which the message usually does not. One the
// message already states is left out rather than repeated.
func qualify(message, code string) string {
	if code == "" || strings.Contains(message, code) {
		return message
	}
	return message + "（" + code + "）"
}

func firstString(root map[string]any, paths ...[]string) string {
	for _, path := range paths {
		value, exists := objectPath(root, path...)
		if !exists {
			continue
		}
		if text := valueString(value); text != "" {
			return text
		}
	}
	return ""
}

// Bodies are decoded with UseNumber, so a numeric error code arrives as one.
// Anything structured is not a message and is left to the paths that reach into
// it.
func valueString(value any) string {
	switch text := value.(type) {
	case string:
		return strings.TrimSpace(text)
	case json.Number:
		return text.String()
	default:
		return ""
	}
}

func truncateReason(reason string) string {
	runes := []rune(reason)
	if len(runes) <= maxFailureReason {
		return reason
	}
	return strings.TrimSpace(string(runes[:maxFailureReason])) + "…"
}
