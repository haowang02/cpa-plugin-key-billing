package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
)

const hostHTTPDo = "host.http.do"

type hostHTTPRequest struct {
	HostCallbackID string      `json:"host_callback_id,omitempty"`
	Method         string      `json:"method"`
	URL            string      `json:"url"`
	Headers        http.Header `json:"headers,omitempty"`
	Body           []byte      `json:"body,omitempty"`
}

type hostHTTPResponse struct {
	StatusCode int    `json:"StatusCode"`
	Body       []byte `json:"Body"`
}

func callHostHTTP(caller HostCaller, request hostHTTPRequest) (hostHTTPResponse, error) {
	if caller == nil {
		return hostHTTPResponse{}, fmt.Errorf("Host HTTP transport is unavailable")
	}
	raw, err := caller(hostHTTPDo, request)
	if err != nil {
		return hostHTTPResponse{}, err
	}
	var response hostHTTPResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return hostHTTPResponse{}, fmt.Errorf("Parse host HTTP response: %w", err)
	}
	return response, nil
}
