package plugin

import "testing"

func TestUsageFailureDetailsPreserveStructuredErrorFields(t *testing.T) {
	details := usageFailureDetails(UsageFailure{
		StatusCode: 426,
		Body: `{"error":{"message":"upstream transport requires full HTTP replay",` +
			`"type":"server_error","code":"upstream_http_replay_required"}}`,
	})
	if details.StatusCode != 426 || details.ErrorType != "server_error" {
		t.Fatalf("details = %+v", details)
	}
	wantBody := `{"error":{"message":"upstream transport requires full HTTP replay",` +
		`"type":"server_error","code":"upstream_http_replay_required","status":426}}`
	if details.Body != wantBody {
		t.Fatalf("body = %q, want %q", details.Body, wantBody)
	}
}

func TestUsageFailureDetailsWrapPlainExecutorMessageWithoutInventingType(t *testing.T) {
	details := usageFailureDetails(UsageFailure{
		Body: "websocket: close 1006 (abnormal closure): unexpected EOF",
	})
	if details.StatusCode != 0 || details.ErrorType != "" {
		t.Fatalf("details = %+v", details)
	}
	wantBody := `{"error":{"message":"websocket: close 1006 (abnormal closure): unexpected EOF"}}`
	if details.Body != wantBody {
		t.Fatalf("body = %q, want %q", details.Body, wantBody)
	}
}

func TestUsageFailureDetailsPreserveInformationalStatus(t *testing.T) {
	details := usageFailureDetails(UsageFailure{StatusCode: 308, Body: "redirect failed"})
	if details.StatusCode != 308 {
		t.Fatalf("status = %d, want 308", details.StatusCode)
	}
}
