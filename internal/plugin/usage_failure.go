package plugin

import (
	"encoding/json"
	"strconv"
	"strings"
)

const maxFailureReason = 300

type usageFailureView struct {
	StatusCode int
	ErrorType  string
	Reason     string
	Body       string
}

type normalizedFailureEnvelope struct {
	Error normalizedFailure `json:"error"`
}

type normalizedFailure struct {
	Message string `json:"message,omitempty"`
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
	Status  int    `json:"status,omitempty"`
}

// Keep only the error scalars exposed by usage.handle; unrelated response
// fields must not enter the stored error event.
func usageFailureDetails(failure UsageFailure) usageFailureView {
	statusCode := validFailureStatus(failure.StatusCode)
	raw := strings.TrimSpace(failure.Body)
	normalized, qualifier := parseJSONFailure(raw)
	if normalized == (normalizedFailure{}) {
		normalized.Message = raw
	}
	reason := formatFailureReason(statusCode, qualifyFailure(normalized.Message, qualifier))
	normalized.Message = truncateFailureReason(stripFailureStatus(normalized.Message, statusCode))
	if statusCode != 0 {
		normalized.Status = statusCode
	}
	return usageFailureView{
		StatusCode: statusCode,
		ErrorType:  normalized.Type,
		Reason:     reason,
		Body:       marshalNormalizedFailure(normalized),
	}
}

func parseJSONFailure(raw string) (normalizedFailure, string) {
	if raw == "" {
		return normalizedFailure{}, ""
	}
	if start := strings.IndexByte(raw, '{'); start > 0 {
		raw = raw[start:]
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var root map[string]any
	if decoder.Decode(&root) != nil || root == nil {
		return normalizedFailure{}, ""
	}

	var node map[string]any
	for _, path := range [][]string{{"error"}, {"response", "error"}, {"body", "error"}} {
		if node = failureObjectAt(root, path...); node != nil {
			break
		}
	}
	rootNode := false
	message := ""
	if node != nil {
		message = failureString(node["message"])
	} else if value := failureString(root["error"]); value != "" {
		message = value
		node = root
		rootNode = true
	} else if value := failureString(root["message"]); value != "" {
		message = value
		node = root
		rootNode = true
	}
	if node == nil {
		return normalizedFailure{}, ""
	}

	errorType := failureString(node["type"])
	qualifier := errorType
	if rootNode && strings.EqualFold(errorType, "error") {
		errorType = ""
		qualifier = failureString(node["code"])
	} else if qualifier == "" {
		qualifier = failureString(node["status"])
		if qualifier == "" {
			qualifier = failureString(node["code"])
		}
	}
	return normalizedFailure{
		Message: message,
		Type:    errorType,
		Code:    failureString(node["code"]),
		Status:  failureStatus(node["status"]),
	}, qualifier
}

func marshalNormalizedFailure(failure normalizedFailure) string {
	if failure == (normalizedFailure{}) {
		return ""
	}
	raw, _ := json.Marshal(normalizedFailureEnvelope{Error: failure})
	return string(raw)
}

func validFailureStatus(status int) int {
	if status < 100 || status > 599 {
		return 0
	}
	return status
}

func failureStatus(value any) int {
	var raw string
	switch value := value.(type) {
	case json.Number:
		raw = value.String()
	case string:
		raw = strings.TrimSpace(value)
	default:
		return 0
	}
	status, errStatus := strconv.Atoi(raw)
	if errStatus != nil {
		return 0
	}
	return validFailureStatus(status)
}

func stripFailureStatus(message string, statusCode int) string {
	if statusCode == 0 {
		return message
	}
	for _, separator := range []string{"：", ":"} {
		prefix := "HTTP " + strconv.Itoa(statusCode) + separator
		if strings.HasPrefix(message, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(message, prefix))
		}
	}
	return message
}

func formatFailureReason(statusCode int, message string) string {
	status := ""
	if statusCode != 0 {
		status = "HTTP " + strconv.Itoa(statusCode)
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
