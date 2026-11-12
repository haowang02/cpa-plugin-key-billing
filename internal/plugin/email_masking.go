package plugin

import (
	"regexp"
	"strings"

	"cpa-key-billing/internal/billing"
	"cpa-key-billing/internal/messages"
)

var viewEmailToken = regexp.MustCompile(`[\p{L}\p{N}._%+\-]+@[\p{L}\p{N}\-]+(?:\.[\p{L}\p{N}\-]+)*`)

func maskEmails(value string) string {
	return viewEmailToken.ReplaceAllStringFunc(value, maskEmail)
}

func maskEmail(email string) string {
	at := strings.LastIndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return email
	}
	local := []rune(email[:at])
	var masked string
	switch len(local) {
	case 1:
		masked = "*"
	case 2, 3:
		masked = string(local[:1]) + strings.Repeat("*", len(local)-1)
	default:
		masked = string(local[:2]) + strings.Repeat("*", len(local)-3) + string(local[len(local)-1])
	}
	return masked + email[at:]
}

func maskMessage(message messages.Message) messages.Message {
	message.Text = maskEmails(message.Text)
	if len(message.Params) > 0 {
		params := make(map[string]string, len(message.Params))
		for key, value := range message.Params {
			params[key] = maskEmails(value)
		}
		message.Params = params
	}
	return message
}

func maskAPIKeyPayload(payload any) (any, bool) {
	switch value := payload.(type) {
	case accountProfileResponse:
		value.Identity.Label = maskEmails(value.Identity.Label)
		return value, true
	case accountSubscriptionResponse:
		value.Subscription.Name = maskEmails(value.Subscription.Name)
		return value, true
	case accountRoutingResponse:
		for i := range value.Credentials {
			value.Credentials[i].Name = maskEmails(value.Credentials[i].Name)
			value.Credentials[i].NameMessage = maskMessage(value.Credentials[i].NameMessage)
		}
		for i := range value.DeniedCredentials {
			value.DeniedCredentials[i].Name = maskEmails(value.DeniedCredentials[i].Name)
			value.DeniedCredentials[i].NameMessage = maskMessage(value.DeniedCredentials[i].NameMessage)
		}
		for i := range value.Warnings {
			value.Warnings[i] = maskEmails(value.Warnings[i])
		}
		for i := range value.WarningMessages {
			value.WarningMessages[i] = maskMessage(value.WarningMessages[i])
		}
		return value, true
	case billing.RequestEventView:
		for i := range value.Entries {
			value.Entries[i].Account = maskEmails(value.Entries[i].Account)
			value.Entries[i].Source = maskEmails(value.Entries[i].Source)
			value.Entries[i].ErrorBody = maskEmails(value.Entries[i].ErrorBody)
			if execution := value.Entries[i].Execution; execution != nil {
				masked := *execution
				masked.RequestID = maskEmails(masked.RequestID)
				masked.TraceID = maskEmails(masked.TraceID)
				masked.SessionID = maskEmails(masked.SessionID)
				masked.ParentSessionID = maskEmails(masked.ParentSessionID)
				masked.AccessProvider = maskEmails(masked.AccessProvider)
				value.Entries[i].Execution = &masked
			}
		}
		if value.Filters != nil {
			for i := range value.Filters.SourceOptions {
				value.Filters.SourceOptions[i].Label = maskEmails(value.Filters.SourceOptions[i].Label)
			}
		}
		return value, true
	case billing.RequestErrorView:
		for i := range value.Entries {
			value.Entries[i].Source = maskEmails(value.Entries[i].Source)
			value.Entries[i].Body = maskEmails(value.Entries[i].Body)
		}
		if value.Filters != nil {
			for i := range value.Filters.SourceOptions {
				value.Filters.SourceOptions[i].Label = maskEmails(value.Filters.SourceOptions[i].Label)
			}
		}
		return value, true
	case billing.AnalysisView:
		for i := range value.UsageDistribution.Sources {
			value.UsageDistribution.Sources[i].Key = maskEmails(value.UsageDistribution.Sources[i].Key)
			value.UsageDistribution.Sources[i].Label = maskEmails(value.UsageDistribution.Sources[i].Label)
		}
		return value, true
	case authFileListResponse:
		for i := range value.Files {
			value.Files[i].Name = maskEmails(value.Files[i].Name)
			value.Files[i].Title = maskEmails(value.Files[i].Title)
			value.Files[i].QuotaReason = maskEmails(value.Files[i].QuotaReason)
			value.Files[i].QuotaReasonMessage = maskMessage(value.Files[i].QuotaReasonMessage)
			maskQuota(value.Files[i].Quota)
		}
		return value, true
	case *authFileQuota:
		maskQuota(value)
		return value, true
	case []billing.PriceRow, billing.AnalysisSpeedView:
		return value, true
	default:
		return payload, false
	}
}

func maskQuota(quota *authFileQuota) {
	if quota == nil {
		return
	}
	quota.Plan = maskEmails(quota.Plan)
	for i := range quota.Quota {
		quota.Quota[i].Label = maskEmails(quota.Quota[i].Label)
		quota.Quota[i].GroupLabel = maskEmails(quota.Quota[i].GroupLabel)
		quota.Quota[i].LabelPrefix = maskEmails(quota.Quota[i].LabelPrefix)
		quota.Quota[i].LabelMessage = maskMessage(quota.Quota[i].LabelMessage)
		quota.Quota[i].GroupMessage = maskMessage(quota.Quota[i].GroupMessage)
	}
}
