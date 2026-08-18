package plugin

import (
	"strings"
	"testing"
)

// The body reaching a client has already been translated into its own format,
// and the proxy may have replaced it with an error of its own, so every shape
// an error arrives in has to be read without knowing which format produced it.
func TestResponseFailureReadsEveryErrorShape(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
		want string
	}{{
		name: "openai",
		body: `{"error":{"message":"Rate limit reached","type":"insufficient_quota","code":"insufficient_quota"}}`,
		want: "Rate limit reached（insufficient_quota）",
	}, {
		name: "anthropic stream",
		body: "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n",
		want: "Overloaded（overloaded_error）",
	}, {
		name: "gemini",
		body: `{"error":{"code":429,"message":"Quota exceeded","status":"RESOURCE_EXHAUSTED"}}`,
		want: "Quota exceeded（RESOURCE_EXHAUSTED）",
	}, {
		// The proxy's own stream error: the frame is nothing but the error.
		name: "proxy stream",
		body: "event: error\ndata: {\"type\":\"error\",\"code\":\"internal_server_error\",\"message\":\"upstream stream closed before a terminal event\"}\n",
		want: "upstream stream closed before a terminal event（internal_server_error）",
	}, {
		name: "responses envelope",
		body: `{"type":"response.failed","response":{"error":{"code":"server_error","message":"model unavailable"}}}`,
		want: "model unavailable（server_error）",
	}, {
		name: "error as a bare string",
		body: `{"error":"context canceled"}`,
		want: "context canceled",
	}, {
		// A code the message already states is not worth repeating.
		name: "code inside the message",
		body: `{"error":{"message":"insufficient_quota: no credit","code":"insufficient_quota"}}`,
		want: "insufficient_quota: no credit",
	}, {
		name: "an ordinary response",
		body: `{"id":"resp-1","usage":{"prompt_tokens":10,"completion_tokens":2}}`,
		want: "",
	}, {
		name: "a stream that ended well",
		body: "data: {\"id\":\"resp-1\"}\n\ndata: [DONE]\n",
		want: "",
	}} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := responseFailure(responseObjects([]byte(testCase.body))); got != testCase.want {
				t.Fatalf("responseFailure = %q, want %q", got, testCase.want)
			}
		})
	}
}

// A failure the client was never shown is known only from the terminal event,
// and the host states it there in whatever shape it had the error in.
func TestCompletionFailureReadsWhatTheHostReported(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		statusCode int
		errorText  string
		want       string
	}{{
		// The upstream websocket refused the request and was closed rather than
		// answered, so no body ever reached the response hooks.
		name:       "upstream websocket error",
		statusCode: 413,
		errorText:  `{"error":{"message":"upstream websocket message too big","type":"invalid_request_error","code":"message_too_big"}}`,
		want:       "HTTP 413：upstream websocket message too big（invalid_request_error）",
	}, {
		name:       "body behind a prefix of the proxy's own",
		statusCode: 429,
		errorText:  `codex: upstream returned 429: {"error":{"message":"Rate limit reached","code":"rate_limit_exceeded"}}`,
		want:       "HTTP 429：Rate limit reached（rate_limit_exceeded）",
	}, {
		name:      "prose with no body behind it",
		errorText: "upstream stream closed before a terminal event",
		want:      "upstream stream closed before a terminal event",
	}, {
		// An upstream that answered with a status and nothing else still says more
		// than silence does.
		name:       "a status and nothing else",
		statusCode: 502,
		want:       "HTTP 502",
	}, {
		name:       "a status the message already states",
		statusCode: 500,
		errorText:  "HTTP 500 from upstream",
		want:       "HTTP 500 from upstream",
	}, {
		name: "nothing at all",
		want: "",
	}} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := completionFailure(testCase.statusCode, testCase.errorText); got != testCase.want {
				t.Fatalf("completionFailure = %q, want %q", got, testCase.want)
			}
		})
	}
}

// A stream reports its last error: an earlier frame may have described an
// attempt the proxy went on to retry.
func TestResponseFailureKeepsTheLastError(t *testing.T) {
	body := "data: {\"error\":{\"message\":\"first\"}}\n\ndata: {\"error\":{\"message\":\"last\"}}\n"
	if got := responseFailure(responseObjects([]byte(body))); got != "last" {
		t.Fatalf("responseFailure = %q, want the error the request ended on", got)
	}
}

// The plugin log is held in memory and travels to the panel whole, so one
// upstream answering at length cannot be allowed to crowd out the entries
// around it.
func TestResponseFailureIsTruncated(t *testing.T) {
	body := `{"error":{"message":"` + strings.Repeat("上", maxFailureReason+50) + `"}}`
	failure := responseFailure(responseObjects([]byte(body)))
	if runes := []rune(failure); len(runes) != maxFailureReason+1 || runes[len(runes)-1] != '…' {
		t.Fatalf("len = %d runes, want %d and an ellipsis", len(runes), maxFailureReason+1)
	}
}
