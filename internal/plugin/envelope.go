package plugin

import (
	"encoding/json"
	"net/http"
	"strings"
)

// OKEnvelope marshals a successful RPC result.
func OKEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(Envelope{OK: true, Result: raw})
}

// ErrorEnvelope marshals a failed RPC result. It never fails: a marshalling
// error degrades to a hand-written envelope so the host always sees valid JSON.
func ErrorEnvelope(code, message string, httpStatus int) []byte {
	raw, errMarshal := json.Marshal(Envelope{
		OK: false,
		Error: &EnvelopeError{
			Code:       strings.TrimSpace(code),
			Message:    strings.TrimSpace(message),
			HTTPStatus: httpStatus,
		},
	})
	if errMarshal != nil {
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"无法编码插件错误"}}`)
	}
	return raw
}

// JSONResponse builds a Management API response carrying a JSON payload.
func JSONResponse(status int, payload any) ManagementResponse {
	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return JSONError(http.StatusInternalServerError, "json_error", errMarshal.Error())
	}
	if status <= 0 {
		status = http.StatusOK
	}
	return ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	}
}

// JSONError builds a Management API error response.
func JSONError(status int, code, message string) ManagementResponse {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	body, errMarshal := json.Marshal(map[string]any{
		"error": map[string]string{
			"code":    strings.TrimSpace(code),
			"message": strings.TrimSpace(message),
		},
	})
	if errMarshal != nil {
		body = []byte(`{"error":{"code":"json_error","message":"无法编码错误信息"}}`)
	}
	return ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	}
}
