package plugin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode"

	"cpa-key-billing/internal/billing"
)

func TestUIIncludesBothLanguagesWithoutExternalTranslationResources(t *testing.T) {
	for _, want := range []string{`<html lang="en">`, "const BILLING_MESSAGES =", "billing-language-change", `data-page-action`} {
		if !bytes.Contains(uiHTML, []byte(want)) {
			t.Fatalf("missing %q", want)
		}
	}
	if uiInclude.Match(uiHTML) {
		t.Fatalf("unexpanded include marker %q", uiInclude.Find(uiHTML))
	}
}

func TestManagementValidationIncludesTranslationMetadata(t *testing.T) {
	err := (billing.Plan{}).Validate()
	if err == nil {
		t.Fatal("expected invalid plan")
	}
	response := errorResponse(err)
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Key     string `json:"message_key"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body, &body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadRequest || body.Error.Code != "invalid" || body.Error.Key == "" || body.Error.Message == "" {
		t.Fatalf("response = %s", response.Body)
	}
}

func TestProtocolRejectionsUseEnglishWithoutUITranslationMetadata(t *testing.T) {
	for _, format := range []string{"openai", "claude", "gemini"} {
		responses := []RequestInterceptResponse{
			concurrencyLimitResponse(format, billing.SlotDecision{Active: 2, Limit: 2}),
			quotaExhaustedResponse(format, billing.Decision{PlanName: "Test plan"}, time.Now()),
			modelForbiddenResponse(format, billing.RouteRule{}, "test-model"),
			modelUnservedResponse(format, "test-model"),
			routingErrorResponse(format, "routing_credential_mismatch", "The selected upstream credential is not permitted by this API key's routing rules"),
			priceRefusal(format, "model_price_error", "Model test-model has no configured price"),
		}
		for _, response := range responses {
			var payload struct {
				Error map[string]any `json:"error"`
			}
			if err := json.Unmarshal(response.ResponseBody, &payload); err != nil {
				t.Fatal(err)
			}
			message, _ := payload.Error["message"].(string)
			if message == "" || strings.IndexFunc(message, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
				t.Fatalf("%s response must use English: %s", format, response.ResponseBody)
			}
			if payload.Error["message_key"] != nil || payload.Error["message_params"] != nil {
				t.Fatalf("UI metadata must not leak into the client protocol: %s", response.ResponseBody)
			}
		}
	}
	diagnostics := MenuLabel + MenuDescription
	for _, shortfall := range routingShortfalls {
		diagnostics += shortfall.message("test-model")
	}
	if strings.IndexFunc(diagnostics, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
		t.Fatal("registration and scheduler diagnostics must use English")
	}
}
