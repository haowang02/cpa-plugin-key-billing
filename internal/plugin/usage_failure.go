package plugin

import (
	"encoding/json"
	"strconv"
	"strings"
)

const maxFailureReason = 300

func usageFailureReason(failure UsageFailure) string {
	message := failureMessage(failure.Body)
	status := ""
	if failure.StatusCode >= 400 && failure.StatusCode <= 599 {
		status = "HTTP " + strconv.Itoa(failure.StatusCode)
	}
	switch {
	case message == "":
		return status
	case status == "" || strings.Contains(message, status):
		return truncateFailureReason(message)
	default:
		return truncateFailureReason(status + "：" + message)
	}
}

func failureMessage(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	if message := jsonFailureMessage(body); message != "" {
		return message
	}
	if start := strings.IndexByte(body, '{'); start > 0 {
		if message := jsonFailureMessage(body[start:]); message != "" {
			return message
		}
	}
	return body
}

func jsonFailureMessage(raw string) string {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var root map[string]any
	if decoder.Decode(&root) != nil || root == nil {
		return ""
	}
	for _, path := range [][]string{{"error"}, {"response", "error"}, {"body", "error"}} {
		if node := failureObjectAt(root, path...); node != nil {
			if message := failureString(node["message"]); message != "" {
				return qualifyFailure(message, firstFailureString(node, "type", "status", "code"))
			}
		}
	}
	if message := failureString(root["error"]); message != "" {
		return message
	}
	if strings.EqualFold(failureString(root["type"]), "error") {
		return qualifyFailure(failureString(root["message"]), failureString(root["code"]))
	}
	if message := failureString(root["message"]); message != "" {
		return qualifyFailure(message, firstFailureString(root, "type", "status", "code"))
	}
	return ""
}

func failureObjectAt(root map[string]any, path ...string) map[string]any {
	var current any = root
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[key]
	}
	object, _ := current.(map[string]any)
	return object
}

func firstFailureString(node map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := failureString(node[key]); value != "" {
			return value
		}
	}
	return ""
}

func failureString(value any) string {
	switch value := value.(type) {
	case string:
		return strings.TrimSpace(value)
	case json.Number:
		return value.String()
	default:
		return ""
	}
}

func qualifyFailure(message, code string) string {
	if message == "" || code == "" || strings.Contains(message, code) {
		return message
	}
	return message + "（" + code + "）"
}

func truncateFailureReason(reason string) string {
	reason = strings.TrimSpace(reason)
	runes := []rune(reason)
	if len(runes) <= maxFailureReason {
		return reason
	}
	return strings.TrimSpace(string(runes[:maxFailureReason])) + "…"
}
