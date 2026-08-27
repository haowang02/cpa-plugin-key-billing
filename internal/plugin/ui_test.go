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

func TestUIKeepsCPAMCAndCPAMPHostStylesSeparate(t *testing.T) {
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
	body := string(resp.Body)

	for _, required := range []string{
		"background-image:var(--app-bg-gradient,none)",
		"--table-header-bg:rgb(from var(--bg-primary) r g b / 1)",
		":root[data-cpamp-plugin-host=true] thead,",
		":root[data-cpamp-plugin-host=true] thead th",
		":root[data-cpamp-plugin-host=true] button:not(.link)",
		":root[data-cpamp-plugin-host=true] .tabs",
		".tag.info{background:var(--info-badge-bg)",
		":root:not([data-cpamp-plugin-host=true]) .wrap{padding-top:90px}",
		`info: { label: "信息", class: "tag info" }`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("UI is missing host compatibility rule %q", required)
		}
	}
	if strings.Contains(body, ":root[data-cpamp-plugin-host=true] .log-table-region thead th{\n  position:static") {
		t.Fatal("CPAMP host styles must preserve the billing log sticky header")
	}
}
