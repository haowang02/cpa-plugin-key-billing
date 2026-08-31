package plugin

import (
	"net/http"
	"strings"
	"testing"
)

func TestUIIsServedAsAStandaloneDocument(t *testing.T) {
	app := newConfiguredApp(t)
	raw, err := app.HandleMethod(MethodManagementHandle, mustMarshal(t, ManagementRequest{
		Method: http.MethodGet,
		Path:   resourceBase + resourceUIPath,
	}))
	if err != nil {
		t.Fatalf("management.handle error = %v", err)
	}
	var resp ManagementResponse
	decodeResult(t, raw, &resp)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Headers.Get("Content-Type"), "text/html") {
		t.Fatalf("response = %+v, want an HTML document", resp)
	}
	if resp.Headers.Get("Cache-Control") != "private, no-store" || resp.Headers.Get("Referrer-Policy") != "no-referrer" ||
		!strings.Contains(resp.Headers.Get("Content-Security-Policy"), "connect-src 'self'") {
		t.Fatalf("security headers = %+v", resp.Headers)
	}

	body := string(resp.Body)
	if !strings.HasPrefix(body, "<!doctype html>") {
		t.Fatal("body is not an HTML document")
	}
	for _, forbidden := range []string{"<link ", "src=\"http", "src='http", "<script src"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("UI references an external asset %q", forbidden)
		}
	}
}
