package plugin

import (
	"encoding/json"
	"net/http"
	"strings"
)

func OKEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(Envelope{OK: true, Result: raw})
}

func ErrorEnvelope(code, message string, httpStatus int) []byte {
	raw, _ := json.Marshal(Envelope{
		OK: false,
		Error: &EnvelopeError{
			Code:       strings.TrimSpace(code),
			Message:    strings.TrimSpace(message),
			HTTPStatus: httpStatus,
		},
	})
	return raw
}

func JSONResponse(status int, payload any) ManagementResponse {
	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return JSONError(http.StatusInternalServerError, "json_error", errMarshal.Error())
	}
	return ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	}
}

func JSONError(status int, code, message string) ManagementResponse {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]string{
			"code":    strings.TrimSpace(code),
			"message": strings.TrimSpace(message),
		},
	})
	return ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	}
}
