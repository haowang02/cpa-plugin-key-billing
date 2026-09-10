package catalogproxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func proxyServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *httptest.Server) {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	proxy, err := New(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	return server, upstream
}

func catalogRequest(t *testing.T, server, path, key string) (int, http.Header, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("If-None-Match", "old-catalog")
	request.Header.Set("If-Modified-Since", "yesterday")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := readBody(response.Body, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, response.Header, string(body)
}

func TestPerKeyCatalogsAndLiveRouting(t *testing.T) {
	for _, path := range []string{"/v1/models", "/models", "/backend-api/codex/models", "/codex/models"} {
		t.Run(path, func(t *testing.T) {
			field, idField := "data", "id"
			if strings.Contains(path, "codex") {
				field, idField = "models", "slug"
			}
			var allowed atomic.Value
			allowed.Store("alpha")
			var routingCalls atomic.Int32
			entries := []map[string]any{
				{idField: "alpha", "owned_by": "openai", "context_window": 12345, "future": map[string]any{"enabled": true}},
				{idField: "beta", "owned_by": "openai", "service_tiers": []string{"priority"}},
			}
			server, _ := proxyServer(t, func(w http.ResponseWriter, r *http.Request) {
				key := r.Header.Get("Authorization")
				if key != "Bearer dummy-a" && key != "Bearer dummy-b" {
					w.WriteHeader(401)
					return
				}
				if r.URL.Path == routingPath {
					routingCalls.Add(1)
					model := allowed.Load().(string)
					if key == "Bearer dummy-b" {
						model = "beta"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"routing_valid": true, "models": []string{model}, "credentials": []any{}})
					return
				}
				if r.URL.Path != path || r.URL.Query().Get("client_version") != "test" || r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" || r.Header.Get("Accept-Encoding") != "identity" {
					t.Error("catalog query or cache controls changed")
				}
				w.Header().Set("ETag", "shared")
				w.Header().Set("Last-Modified", "old")
				_ = json.NewEncoder(w).Encode(map[string]any{field: entries, "extra": "preserved"})
			})
			check := func(key, model string) {
				status, headers, body := catalogRequest(t, server.URL, path+"?client_version=test", key)
				var result map[string]json.RawMessage
				if status != 200 || json.Unmarshal([]byte(body), &result) != nil {
					t.Errorf("catalog status=%d body=%s", status, body)
					return
				}
				var models []map[string]any
				if json.Unmarshal(result[field], &models) != nil || len(models) != 1 || models[0][idField] != model {
					t.Errorf("wrong key catalog: %s", body)
					return
				}
				index := 0
				if model == "beta" {
					index = 1
				}
				wantJSON, _ := json.Marshal(entries[index])
				var want map[string]any
				_ = json.Unmarshal(wantJSON, &want)
				if !reflect.DeepEqual(models[0], want) || string(result["extra"]) != `"preserved"` {
					t.Error("catalog metadata changed")
				}
				if headers.Get("Cache-Control") != "private, no-store" || headers.Get("ETag") != "" || headers.Get("Last-Modified") != "" {
					t.Error("catalog can be cached across callers")
				}
			}
			var concurrent sync.WaitGroup
			for range 6 {
				concurrent.Add(2)
				go func() { defer concurrent.Done(); check("dummy-a", "alpha") }()
				go func() { defer concurrent.Done(); check("dummy-b", "beta") }()
			}
			concurrent.Wait()
			allowed.Store("beta")
			check("dummy-a", "beta")
			if routingCalls.Load() != 13 {
				t.Fatal("routing permissions were cached")
			}
		})
	}
}

func TestCredentialProviderIntersection(t *testing.T) {
	for _, test := range []struct {
		name, routing, catalog string
		want                   []string
	}{
		{"codex", `{"routing_valid":true,"models":[],"credentials":[{"provider":"codex","status":"active"}]}`, `{"data":[{"id":"alpha","owned_by":"openai"},{"id":"beta","owned_by":"antigravity"},{"id":"unknown"}]}`, []string{"alpha"}},
		{"intersection", `{"routing_valid":true,"models":["BETA"],"credentials":[{"provider":"codex"}]}`, `{"data":[{"id":"alpha","owned_by":"openai"},{"id":"beta","owned_by":"antigravity"}]}`, []string{}},
		{"native-codex", `{"routing_valid":true,"models":[],"credentials":[{"provider":"codex"}]}`, `{"models":[{"slug":"alpha"}]}`, []string{"alpha"}},
		{"missing", `{"routing_valid":true,"models":[],"credentials":[{"provider":"codex","status":"missing"}]}`, `{"data":[{"id":"alpha","owned_by":"openai"}]}`, []string{}},
		{"disabled", `{"routing_valid":true,"models":[],"credentials":[{"provider":"codex","status":"disabled"}]}`, `{"data":[{"id":"alpha","owned_by":"openai"}]}`, []string{}},
		{"unrestricted", `{"routing_valid":true,"models":[],"credentials":[]}`, `{"data":[{"id":"alpha"},{"id":"beta"}]}`, []string{"alpha", "beta"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, _ := proxyServer(t, func(w http.ResponseWriter, r *http.Request) {
				body := test.catalog
				if r.URL.Path == routingPath {
					body = test.routing
				}
				_, _ = io.WriteString(w, body)
			})
			status, _, body := catalogRequest(t, server.URL, "/v1/models", "dummy")
			var result map[string][]map[string]json.RawMessage
			if status != 200 || json.Unmarshal([]byte(body), &result) != nil {
				t.Fatalf("status=%d body=%s", status, body)
			}
			got := []string{}
			for _, field := range []string{"data", "models"} {
				for _, model := range result[field] {
					got = append(got, modelID(model))
				}
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("models=%v, want %v", got, test.want)
			}
		})
	}
}

func TestPermissionFailuresDoNotExposeCatalog(t *testing.T) {
	for _, body := range []string{
		`{}`, `not-json`, `{"routing_valid":false,"models":[],"credentials":[]}`,
		`{"routing_valid":true,"models":null,"credentials":[]}`,
		`{"routing_valid":true,"models":[],"credentials":null}`,
		`{"routing_valid":true,"models":[""],"credentials":[]}`,
	} {
		server, _ := proxyServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == routingPath {
				_, _ = io.WriteString(w, body)
				return
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"hidden-model"}]}`)
		})
		status, headers, result := catalogRequest(t, server.URL, "/v1/models", "dummy")
		if status != 502 || strings.Contains(result, "hidden-model") || headers.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("permission failure exposed catalog: %d %s", status, result)
		}
	}
}

func TestRoutingRedirectIsNotFollowed(t *testing.T) {
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { redirected.Add(1); w.WriteHeader(200) }))
	defer destination.Close()
	server, _ := proxyServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == routingPath {
			http.Redirect(w, r, destination.URL, 302)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"hidden"}]}`)
	})
	status, _, _ := catalogRequest(t, server.URL, "/v1/models", "dummy")
	if status != 502 || redirected.Load() != 0 {
		t.Fatal("routing redirect received caller credentials")
	}
}

func TestInferenceAndAuthenticationErrorsPassThrough(t *testing.T) {
	var routingCalls atomic.Int32
	server, _ := proxyServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == routingPath {
			routingCalls.Add(1)
			return
		}
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, "dummy authentication error")
			return
		}
		if r.Header.Get("Authorization") != "Bearer dummy" {
			t.Error("inference credential changed")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.Copy(w, r.Body)
	})
	status, _, body := catalogRequest(t, server.URL, "/v1/models", "dummy")
	if status != 401 || body != "dummy authentication error" {
		t.Fatal("CPA authentication error changed")
	}
	payload := "event: response.completed\ndata: {\"model\":\"fast-alias\",\"service_tier\":\"priority\"}\n\n"
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(payload))
	request.Header.Set("Authorization", "Bearer dummy")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := readBody(response.Body, 1<<20)
	if err != nil || string(data) != payload || response.Header.Get("Content-Type") != "text/event-stream" || routingCalls.Load() != 0 {
		t.Fatal("non-catalog response changed")
	}
}

func TestMalformedCatalogsAndBodyLimits(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"data":null}`, `{"data":[{}]}`, `{"data":[{"id":3}]}`} {
		response := &http.Response{Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
		if err := filterCatalog(response, &permissions{}); err == nil {
			t.Errorf("accepted malformed catalog %s", body)
		}
	}
	if _, err := readBody(io.NopCloser(strings.NewReader("12345")), 4); err == nil {
		t.Fatal("body limit ignored")
	}
	if body, err := readBody(io.NopCloser(strings.NewReader("1234")), 4); err != nil || string(body) != "1234" {
		t.Fatal("exact body limit rejected")
	}
}

func TestOriginValidation(t *testing.T) {
	for _, origin := range []string{"", "file:///tmp", "ftp://host", "http://user:pass@host", "http://host/v1", "http://host?key=dummy", "http://host#fragment"} {
		if _, err := New(origin); err == nil {
			t.Errorf("accepted origin %q", origin)
		}
	}
	for _, origin := range []string{"http://127.0.0.1:8317", "https://example.test/"} {
		if _, err := New(origin); err != nil {
			t.Errorf("rejected origin %q", origin)
		}
	}
}

func TestCallerCredentialFormsAndAmbiguity(t *testing.T) {
	for _, header := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "query"} {
		t.Run(header, func(t *testing.T) {
			server, _ := proxyServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == routingPath {
					if r.Header.Get("Authorization") != "Bearer dummy" || r.URL.RawQuery != "" {
						t.Error("routing lookup did not use canonical caller credential")
					}
					_, _ = io.WriteString(w, `{"routing_valid":true,"models":[],"credentials":[]}`)
					return
				}
				_, _ = io.WriteString(w, `{"data":[]}`)
			})
			request, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/models", nil)
			switch header {
			case "Authorization":
				request.Header.Set(header, "Bearer dummy")
			case "query":
				request.URL.RawQuery = "key=dummy"
			default:
				request.Header.Set(header, "dummy")
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = readBody(response.Body, 1024)
			if response.StatusCode != 200 {
				t.Fatalf("credential form rejected: %d", response.StatusCode)
			}
		})
	}
	for _, alternate := range []string{"X-Api-Key", "X-Goog-Api-Key", "query"} {
		request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		request.Header.Set("Authorization", "Bearer dummy-a")
		if alternate == "query" {
			request.URL.RawQuery = "key=dummy-b"
		} else {
			request.Header.Set(alternate, "dummy-b")
		}
		if _, err := callerKey(request); err == nil {
			t.Fatal("conflicting keys could mix authentication and route scopes")
		}
	}
}
