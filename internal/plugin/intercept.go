package plugin

import (
	"encoding/json"
	"fmt"
	"strings"

	"cpa-key-billing/internal/billing"
)

// Enforcement runs before auth so an over-quota request never occupies an
// upstream credential.
func (a *App) interceptBeforeAuth(raw []byte) ([]byte, error) {
	var req RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("Parse request interception parameters: %w", errUnmarshal)
	}
	helper := metadataString(req.Metadata, MetadataSource) == SourcePluginHostModelCallback
	if !a.store.Enabled() {
		return OKEnvelope(RequestInterceptResponse{})
	}
	scope := metadataString(req.Metadata, MetadataCallerScope)
	endpoint := metadataString(req.Metadata, MetadataRequestPath)

	// Reject disallowed models before quota checks can open a subscription period.
	if !helper {
		a.rememberRequestOrigin(req, scope, endpoint)
		routing := a.store.ResolveRouting(scope, req.Model, req.RequestedModel)
		a.beginRouteLog(req.RequestID, scope, routing)
		if routing.RestrictsModels() && !routing.AllowsModel() {
			return OKEnvelope(modelForbiddenResponse(req.SourceFormat, routing))
		}
	}

	price, model, priceErr := a.store.ResolveModelPrice(req.Model, req.RequestedModel, true)
	if priceErr != nil {
		return OKEnvelope(priceRefusal(req.SourceFormat, "price_storage_error", "Failed to load model pricing. Please try again later."))
	}
	if price.Source == billing.PriceSourceNone && !a.store.Config().AllowUnpricedModels {
		return OKEnvelope(priceRefusal(req.SourceFormat, "model_price_error", fmt.Sprintf("Model %s has no configured price", model)))
	}
	if helper {
		// Nested plugin helpers follow the same pricing policy because usage.handle
		// can attribute their usage to the client, but consume no extra admission slot.
		return OKEnvelope(RequestInterceptResponse{})
	}
	generate := true
	if value, ok := req.Metadata[MetadataGenerate].(bool); ok {
		generate = value
	}
	if generate {
		slot := a.store.AcquireSlot(scope, req.RequestID)
		if !slot.Allowed {
			return OKEnvelope(concurrencyLimitResponse(req.SourceFormat, slot))
		}
	}

	// A reference price refresh may have taken time; quota and Retry-After share the current instant.
	now := a.store.Now()
	decision := a.store.Authorize(scope, now)
	if !decision.Allowed {
		if generate {
			a.store.ReleaseSlot(req.RequestID)
		}
		a.store.ReportQuotaBlock(scope, endpoint, decision)
		return OKEnvelope(quotaExhaustedResponse(req.SourceFormat, decision, now))
	}

	return OKEnvelope(RequestInterceptResponse{})
}

func (a *App) interceptAfterAuth(raw []byte) ([]byte, error) {
	var req RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("Parse post-auth request interception parameters: %w", errUnmarshal)
	}
	result, detail := "unrestricted", ""
	var response RequestInterceptResponse
	if a.store.Enabled() && metadataString(req.Metadata, MetadataSource) != SourcePluginHostModelCallback {
		// Credential permissions are independent of model aliases. Recheck the
		// current policy even when another scheduler made the selection.
		scope := metadataString(req.Metadata, MetadataCallerScope)
		decision := a.store.ResolveRouting(scope, req.Model, req.RequestedModel)
		a.beginRouteLog(req.RequestID, scope, decision)
		if decision.RestrictsCredentials() {
			allowed, err := a.selectedCredentialAllowed(req, decision)
			switch {
			case err != nil:
				result, detail = "unverified", err.Error()
				response = routingErrorResponse(req.SourceFormat, "routing_credential_unverified", detail)
			case !allowed:
				result, detail = "deny", "The selected upstream credential is not permitted by this API key's routing rules"
				response = routingErrorResponse(req.SourceFormat, "routing_credential_mismatch", detail)
			default:
				result = "allow"
			}
		}
	}
	if id := metadataString(req.Metadata, MetadataSelectedAuth); !response.Terminate && id != "" {
		a.store.AssignSlotCredential(req.RequestID, billing.CredentialFingerprint(id))
	}
	a.observeRouteCredential(req, result, detail)
	return OKEnvelope(response)
}

func (a *App) completeRequest(raw []byte) ([]byte, error) {
	var completion RequestCompletion
	if errUnmarshal := json.Unmarshal(raw, &completion); errUnmarshal != nil {
		return nil, fmt.Errorf("Parse request completion event: %w", errUnmarshal)
	}
	a.store.ReleaseSlot(completion.RequestID)
	a.finishRouteLog(completion)
	return OKEnvelope(struct{}{})
}

// The host sanitizes metadata into JSON-native types, so a surviving value is a string or formats safely as one.
func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	raw, exists := metadata[key]
	if !exists || raw == nil {
		return ""
	}
	if value, ok := raw.(string); ok {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(fmt.Sprint(raw))
}
