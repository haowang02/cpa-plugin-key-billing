package plugin

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"cpa-key-billing/internal/billing"
)

func TestRequestOriginsRespectScopeAndCapacity(t *testing.T) {
	app := newApp(nil)
	var group sync.WaitGroup
	for worker := range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			for i := worker; i < maxRequestOrigins; i += 8 {
				trace, endpoint := fmt.Sprint(i), fmt.Sprintf("/endpoint/%d", i)
				app.rememberRequestOrigin(RequestInterceptRequest{TraceID: trace}, "scope", endpoint)
				if got := app.findRequestOrigin(trace, "scope"); got.Endpoint != endpoint {
					t.Errorf("concurrent origin crossed traces: %+v", got)
				}
			}
		}()
	}
	group.Wait()
	keep := app.requestOriginOrder.Back().Value.(*requestOrigin).key
	app.findRequestOrigin(keep.traceID, keep.scope)
	evicted := app.requestOriginOrder.Back().Value.(*requestOrigin).key
	app.rememberRequestOrigin(RequestInterceptRequest{TraceID: "new-trace"}, "scope", "/v1/messages")
	if len(app.requestOrigins) != maxRequestOrigins || app.findRequestOrigin(evicted.traceID, evicted.scope).Endpoint != "" ||
		app.findRequestOrigin(keep.traceID, keep.scope).Endpoint == "" {
		t.Fatal("origin retention did not evict the least recently used trace")
	}

	for _, test := range []struct {
		scope, path string
		headers     http.Header
	}{
		{"scope-a", "/v1/responses", http.Header{"User-Agent": {strings.Repeat("a", maxClientHeaderBytes+1)}}},
		{"scope-b", "/v1/messages?key=dummy-secret", http.Header{
			"User-Agent": {" dummy-client/1.0 "}, "X-Forwarded-For": {"192.0.2.10", "198.51.100.20"},
		}},
	} {
		app.rememberRequestOrigin(RequestInterceptRequest{TraceID: "shared", Headers: test.headers}, test.scope, test.path)
	}
	if got := app.findRequestOrigin("shared", "scope-b"); got != (billing.RequestOrigin{
		Endpoint: "/v1/messages", Transport: "http", UserAgent: "dummy-client/1.0", XForwardedFor: "192.0.2.10, 198.51.100.20",
	}) {
		t.Fatalf("incorrect scoped origin: %+v", got)
	}
	if got := app.findRequestOrigin("shared", "scope-a"); got != (billing.RequestOrigin{Endpoint: "/v1/responses", Transport: "http"}) {
		t.Fatalf("origin crossed caller scopes or kept an oversized header: %+v", got)
	}
	for _, endpoint := range []string{"/v1/messages", "/v1/responses"} {
		app.rememberRequestOrigin(RequestInterceptRequest{TraceID: "shared"}, "scope-a", endpoint)
		if got := app.findRequestOrigin("shared", "scope-a"); got != (billing.RequestOrigin{}) {
			t.Fatalf("conflicting trace ID was guessed: %+v", got)
		}
	}
	if got := app.findRequestOrigin("missing", "scope-a"); got != (billing.RequestOrigin{}) {
		t.Fatalf("missing trace ID was guessed: %+v", got)
	}
}
