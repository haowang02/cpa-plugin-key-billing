package plugin

import (
	"net/http"
	"net/url"
	"strings"

	"cpa-key-billing/internal/billing"
)

const (
	maxRequestOrigins = 16384
	// Client headers are display metadata; longer values stay unknown rather
	// than widening every stored request.
	maxClientHeaderBytes = 1024
)

type requestOriginKey struct {
	traceID, scope string
}

type requestOrigin struct {
	key requestOriginKey
	billing.RequestOrigin
}

// Retain origins after request.complete: usage is asynchronous and retries or
// WebSocket turns can report more than one record for the same inbound trace.
func (a *App) rememberRequestOrigin(req RequestInterceptRequest, scope, endpoint string) {
	key := requestOriginKey{strings.TrimSpace(req.TraceID), scope}
	endpoint = requestEndpoint(endpoint)
	if key.traceID == "" || endpoint == "" {
		return
	}
	reported := billing.RequestOrigin{
		Endpoint:      endpoint,
		Transport:     "http",
		UserAgent:     clientHeader(req.Headers.Get("User-Agent")),
		XForwardedFor: clientHeader(strings.Join(req.Headers.Values("X-Forwarded-For"), ", ")),
	}
	if headerHasToken(req.Headers, "Connection", "upgrade") && headerHasToken(req.Headers, "Upgrade", "websocket") {
		reported.Transport = "websocket"
	}
	a.requestOriginsMu.Lock()
	defer a.requestOriginsMu.Unlock()
	if entry := a.requestOrigins[key]; entry != nil {
		origin := entry.Value.(*requestOrigin)
		if origin.RequestOrigin != reported {
			// Conflicting metadata stays unknown for the retained trace.
			origin.RequestOrigin = billing.RequestOrigin{}
		}
		a.requestOriginOrder.MoveToFront(entry)
		return
	}
	a.requestOrigins[key] = a.requestOriginOrder.PushFront(&requestOrigin{key, reported})
	if len(a.requestOrigins) > maxRequestOrigins {
		oldest := a.requestOriginOrder.Back()
		delete(a.requestOrigins, oldest.Value.(*requestOrigin).key)
		a.requestOriginOrder.Remove(oldest)
	}
}

func (a *App) findRequestOrigin(traceID, scope string) billing.RequestOrigin {
	a.requestOriginsMu.Lock()
	defer a.requestOriginsMu.Unlock()
	entry := a.requestOrigins[requestOriginKey{strings.TrimSpace(traceID), scope}]
	if entry == nil {
		return billing.RequestOrigin{}
	}
	a.requestOriginOrder.MoveToFront(entry)
	return entry.Value.(*requestOrigin).RequestOrigin
}

func clientHeader(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > maxClientHeaderBytes {
		return ""
	}
	return value
}

func requestEndpoint(raw string) string {
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || len(raw) > 4096 {
		return ""
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return ""
	}
	// Persist only the route path, never query parameters or headers.
	return parsed.EscapedPath()
}

func headerHasToken(headers http.Header, name, token string) bool {
	for _, value := range headers.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}
