package plugin

import (
	"net/http"
	"strings"
	"testing"
)

func concurrencyApp(t *testing.T, limit int) *App {
	t.Helper()
	app := newConfiguredApp(t)
	if _, errSync := app.store.SyncKeys([]string{testAPIKey}, false); errSync != nil {
		t.Fatalf("SyncKeys error = %v", errSync)
	}
	if errSet := app.store.SetConcurrencyLimit(flowScope(), limit); errSet != nil {
		t.Fatalf("SetConcurrencyLimit error = %v", errSet)
	}
	return app
}

func interceptWithID(t *testing.T, app *App, requestID string, generate bool) RequestInterceptResponse {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodRequestInterceptBefore, mustMarshal(t, RequestInterceptRequest{
		RequestID: requestID, SourceFormat: "openai", Model: flowModel, RequestedModel: flowModel,
		Metadata: map[string]any{
			MetadataCallerScope: flowScope(), MetadataRequestPath: "/v1/responses", MetadataGenerate: generate,
		},
	}))
	if errHandle != nil {
		t.Fatalf("request.intercept_before error = %v", errHandle)
	}
	var response RequestInterceptResponse
	decodeResult(t, raw, &response)
	return response
}

func completeRequest(t *testing.T, app *App, requestID string) {
	t.Helper()
	raw, errHandle := app.HandleMethod(MethodRequestComplete, mustMarshal(t, map[string]string{"RequestID": requestID}))
	if errHandle != nil {
		t.Fatalf("request.complete error = %v", errHandle)
	}
	decodeResult(t, raw, nil)
}

func TestConcurrencyLimitRejectsAndCompletionReleases(t *testing.T) {
	app := concurrencyApp(t, 1)
	if first := interceptWithID(t, app, "request-1", true); first.Terminate {
		t.Fatalf("first response = %+v", first)
	}
	blocked := interceptWithID(t, app, "request-2", true)
	if !blocked.Terminate || blocked.StatusCode != http.StatusTooManyRequests || blocked.ResponseHeaders.Get("Retry-After") != "1" {
		t.Fatalf("blocked response = %+v", blocked)
	}
	if !strings.Contains(string(blocked.ResponseBody), "concurrency limit reached") {
		t.Fatalf("blocked body = %s", blocked.ResponseBody)
	}
	views := app.store.KeyViews()
	if len(views) != 1 || views[0].CurrentConcurrency != 1 || views[0].ConcurrencyLimit != 1 {
		t.Fatalf("key views = %+v", views)
	}

	completeRequest(t, app, "request-1")
	completeRequest(t, app, "request-1")
	if retry := interceptWithID(t, app, "request-3", true); retry.Terminate {
		t.Fatalf("retry response = %+v", retry)
	}
}

func TestNonGeneratingRequestDoesNotOccupySlot(t *testing.T) {
	app := concurrencyApp(t, 1)
	if prewarm := interceptWithID(t, app, "prewarm", false); prewarm.Terminate {
		t.Fatalf("prewarm response = %+v", prewarm)
	}
	if first := interceptWithID(t, app, "request-1", true); first.Terminate {
		t.Fatalf("generation response = %+v", first)
	}
	if current := app.store.KeyViews()[0].CurrentConcurrency; current != 1 {
		t.Fatalf("CurrentConcurrency = %d, want 1", current)
	}
}

func TestQuotaRejectionRollsBackConcurrencySlot(t *testing.T) {
	app := exhaustedApp(t, 0)
	if errSet := app.store.SetConcurrencyLimit(flowScope(), 1); errSet != nil {
		t.Fatal(errSet)
	}
	response := interceptWithID(t, app, "quota-rejected", true)
	if !response.Terminate || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("response = %+v, want quota rejection", response)
	}
	if current := app.store.KeyViews()[0].CurrentConcurrency; current != 0 {
		t.Fatalf("CurrentConcurrency = %d, want the refused request released", current)
	}
}

func TestFiniteLimitRequiresRequestID(t *testing.T) {
	app := concurrencyApp(t, 1)
	response := interceptWithID(t, app, "", true)
	if !response.Terminate || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("response = %+v, want fail-closed admission without RequestID", response)
	}
	if current := app.store.KeyViews()[0].CurrentConcurrency; current != 0 {
		t.Fatalf("CurrentConcurrency = %d, want no leaked slot", current)
	}
}
