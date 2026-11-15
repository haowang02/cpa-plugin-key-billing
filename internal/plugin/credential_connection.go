package plugin

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"cpa-key-billing/internal/messages"
)

const hostModelExecute = "host.model.execute"

type hostModelExecution struct {
	HostCallbackID string      `json:"host_callback_id,omitempty"`
	EntryProtocol  string      `json:"entry_protocol"`
	Model          string      `json:"model"`
	Body           []byte      `json:"body"`
	Headers        http.Header `json:"headers"`
	AuthID         string      `json:"auth_id"`
}

func credentialAuthIndex(raw string) (string, bool) {
	authIndex := strings.TrimSpace(raw)
	return authIndex, authIndex != "" && len(authIndex) <= 512
}

func (a *App) resolveHostCredential(raw string) (*hostCredential, ManagementResponse) {
	authIndex, ok := credentialAuthIndex(raw)
	if !ok {
		return nil, JSONError(http.StatusBadRequest, "invalid", "Invalid credential identifier")
	}
	credential, err := a.hostCredential(authIndex)
	if err != nil {
		return nil, jsonMessageError(http.StatusBadGateway, "host_unavailable", messages.New("Failed to load upstream credentials: %s", err.Error()))
	}
	return &credential, ManagementResponse{}
}

func (a *App) credentialTestModel(req ManagementRequest) ManagementResponse {
	credential, failure := a.resolveHostCredential(req.Query.Get("auth_index"))
	if credential == nil {
		return failure
	}
	model, err := a.store.CredentialTestModel(credential.AuthIndex)
	if err != nil {
		return errorResponse(err)
	}
	return JSONResponse(http.StatusOK, map[string]any{"model": model, "models": credential.Models})
}

func (a *App) setCredentialTestModel(req ManagementRequest) ManagementResponse {
	var body struct {
		AuthIndex string `json:"auth_index"`
		Model     string `json:"model"`
	}
	if err := decodeStrict(req.Body, &body); err != nil {
		return errorResponse(err)
	}
	authIndex, ok := credentialAuthIndex(body.AuthIndex)
	if !ok {
		return JSONError(http.StatusBadRequest, "invalid", "Invalid credential identifier")
	}
	model := cleanText(body.Model)
	if len(model) > 1024 {
		return JSONError(http.StatusBadRequest, "invalid", "Model ID is too long")
	}
	if err := a.store.SetCredentialTestModel(authIndex, model); err != nil {
		return errorResponse(err)
	}
	return JSONResponse(http.StatusOK, struct{}{})
}

// A real upstream request pinned to the credential; usage records it without an API key.
func (a *App) testCredential(req ManagementRequest) ManagementResponse {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	if err := decodeStrict(req.Body, &body); err != nil {
		return errorResponse(err)
	}
	credential, failure := a.resolveHostCredential(body.AuthIndex)
	if credential == nil {
		return failure
	}
	model, err := a.store.CredentialTestModel(credential.AuthIndex)
	if err != nil {
		return errorResponse(err)
	}
	switch {
	case model == "" && len(credential.Models) == 0:
		return JSONError(http.StatusUnprocessableEntity, "unsupported", "This credential serves no models")
	case model == "":
		model = credential.Models[0]
	case !slices.Contains(credential.Models, model):
		return jsonMessageError(http.StatusUnprocessableEntity, "unsupported", messages.New("Test model %s is not served by this credential", model))
	}
	// Every executor translates from the OpenAI chat format.
	payload, _ := json.Marshal(map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": "Hi"}}, "stream": false})
	started := time.Now()
	if _, err := a.hostCaller(hostModelExecute, hostModelExecution{
		HostCallbackID: req.HostCallbackID, EntryProtocol: "openai", Model: model, Body: payload,
		Headers: http.Header{"Content-Type": []string{"application/json"}}, AuthID: credential.ID,
	}); err != nil {
		return jsonMessageError(http.StatusBadGateway, "connection_failed", messages.New("Connection test with %s failed: %s", model, err.Error()))
	}
	return JSONResponse(http.StatusOK, map[string]any{"model": model, "latency_ms": time.Since(started).Milliseconds()})
}
