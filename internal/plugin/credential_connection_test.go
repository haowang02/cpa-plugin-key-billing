package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestConnectionTestPinsTheCredentialAndChoosesItsModel(t *testing.T) {
	app := newConfiguredApp(t)
	var executed []hostModelExecution
	app.SetHostCaller(func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case hostAuthModels:
			return json.RawMessage(`{"credentials":[{"id":"codex-1.json","auth_index":"idx-1","provider":"codex","models":["gpt-5.5","gpt-5.4"]}]}`), nil
		case hostModelExecute:
			executed = append(executed, payload.(hostModelExecution))
			return json.RawMessage(`{"status_code":200}`), nil
		}
		t.Fatalf("unexpected host method %q", method)
		return nil, nil
	})
	test := func() (int, string) {
		raw, err := app.HandleMethod(MethodManagementHandle, mustMarshal(t, ManagementRequest{
			Method: http.MethodPost, Path: managementBase + routeCredentialTest, Body: []byte(`{"auth_index":"idx-1"}`), HostCallbackID: "callback-1",
		}))
		if err != nil {
			t.Fatal(err)
		}
		var response ManagementResponse
		decodeResult(t, raw, &response)
		return response.StatusCode, string(response.Body)
	}
	setModel := func(model string) {
		callOK(t, app, http.MethodPost, routeCredentialTestModel, nil, map[string]string{"auth_index": "idx-1", "model": model}, http.StatusOK, nil)
	}

	if status, body := test(); status != http.StatusOK || !strings.Contains(body, `"model":"gpt-5.5"`) {
		t.Fatalf("default test = %d %s", status, body)
	}
	if request := executed[0]; request.AuthID != "codex-1.json" || request.HostCallbackID != "callback-1" || request.EntryProtocol != "openai" ||
		!strings.Contains(string(request.Body), `"model":"gpt-5.5"`) {
		t.Fatalf("execution = %+v", request)
	}

	setModel("gpt-5.4")
	var saved struct {
		Model  string   `json:"model"`
		Models []string `json:"models"`
	}
	callOK(t, app, http.MethodGet, routeCredentialTestModel, url.Values{"auth_index": {"idx-1"}}, nil, http.StatusOK, &saved)
	if saved.Model != "gpt-5.4" || len(saved.Models) != 2 {
		t.Fatalf("saved test model = %+v", saved)
	}
	if status, _ := test(); status != http.StatusOK || executed[1].Model != "gpt-5.4" {
		t.Fatalf("configured test = %d, execution = %+v", status, executed[1])
	}

	setModel("retired-model")
	if status, body := test(); status != http.StatusUnprocessableEntity || !strings.Contains(body, "retired-model") || len(executed) != 2 {
		t.Fatalf("unserved model = %d %s", status, body)
	}
}
