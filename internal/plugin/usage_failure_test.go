package plugin

import "testing"

func TestUsageFailureDetailsRecordedErrors(t *testing.T) {
	for _, test := range []struct {
		name       string
		statusCode int
		body       string
		wantType   string
	}{
		{
			name:       "message too big",
			statusCode: 413,
			body:       `{"error":{"message":"upstream websocket message too big","type":"invalid_request_error","code":"message_too_big","status":413}}`,
			wantType:   "message_too_big",
		},
		{
			name:       "unsupported model without code",
			statusCode: 400,
			body:       `{"error":{"message":"The 'gpt-5.4' model is not supported when using Codex with a ChatGPT account.","type":"invalid_request_error","status":400}}`,
			wantType:   "invalid_request_error",
		},
		{
			name: "websocket close 1012 without classification",
			body: `{"error":{"message":"websocket: close 1012"}}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			details := usageFailureDetails(UsageFailure{StatusCode: test.statusCode, Body: test.body})
			if details.ErrorType != test.wantType {
				t.Errorf("error type = %q, want %q", details.ErrorType, test.wantType)
			}
			if details.StatusCode != test.statusCode {
				t.Errorf("status = %d, want %d", details.StatusCode, test.statusCode)
			}
			if details.Body != test.body {
				t.Errorf("body = %q, want %q", details.Body, test.body)
			}
		})
	}
}

func TestUsageFailureDetailsPreserveStructuredErrorFields(t *testing.T) {
	details := usageFailureDetails(UsageFailure{
		StatusCode: 426,
		Body: `{"error":{"message":"upstream transport requires full HTTP replay",` +
			`"type":"server_error","code":"upstream_http_replay_required"}}`,
	})
	if details.StatusCode != 426 || details.ErrorType != "upstream_http_replay_required" {
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
