package plugin

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"cpa-key-billing/internal/messages"
)

// Cloud Code limits the model inventory reported to older clients.
const antigravityUserAgent = "antigravity/hub/2.23.0 darwin/arm64"

func (c *authQuotaClient) fetchAntigravityQuota(projectID string, result *authQuotaResponse) error {
	headers := http.Header{"User-Agent": {antigravityUserAgent}}
	var lastErr error
	fetched := false
	for _, endpoint := range []string{
		"https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
		"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary",
		"https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
	} {
		value, err := c.upstream(http.MethodPost, endpoint, headers.Clone(), map[string]string{"project": projectID})
		if err != nil {
			lastErr = err
			continue
		}
		fetched = true
		if rows := antigravityQuotaRows(value); len(rows) > 0 {
			result.Quota = append(result.Quota, rows...)
			break
		}
	}
	if !fetched {
		return lastErr
	}
	for _, endpoint := range []string{
		"https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist",
		"https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist",
	} {
		subscription, err := c.upstream(http.MethodPost, endpoint, headers.Clone(), map[string]any{"metadata": map[string]string{"ideType": "ANTIGRAVITY"}})
		if err != nil {
			continue
		}
		if body := objectMap(subscription, "body"); body != nil {
			subscription = body
		}
		tier := objectMap(subscription, "paidTier", "paid_tier")
		if tier == nil {
			tier = objectMap(subscription, "currentTier", "current_tier")
		}
		if tier != nil {
			result.Plan = firstNonEmptyString(firstString(tier, "name"), firstString(tier, "description"))
			break
		}
	}
	return nil
}

func antigravityQuotaRows(quota map[string]any) []quotaRow {
	if len(objectSlice(quota, "groups")) == 0 {
		if body := objectMap(quota, "body"); body != nil {
			quota = body
		}
	}
	var rows []quotaRow
	for groupIndex, raw := range objectSlice(quota, "groups") {
		group, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		groupLabel := firstString(group, "displayName", "display_name")
		var groupMessage messages.Message
		if groupLabel == "" {
			groupLabel = fmt.Sprintf("Quota group %d", groupIndex+1)
			groupMessage = messages.New("Quota group %d", groupIndex+1)
		}
		groupRows := make([]quotaRow, 0, len(objectSlice(group, "buckets")))
		for _, bucketRaw := range objectSlice(group, "buckets") {
			bucket, ok := bucketRaw.(map[string]any)
			if !ok {
				continue
			}
			remaining := antigravityRemainingPercent(bucket)
			if remaining == nil {
				continue
			}
			windowName := strings.ToLower(firstString(bucket, "window"))
			label := firstNonEmptyString(firstString(bucket, "displayName", "display_name", "description", "window", "bucketId", "bucket_id"), "Quota")
			var windowSeconds int64
			if windowName == "5h" || windowName == "five-hour" || windowName == "five_hour" {
				label = "5-hour limit"
				windowSeconds = 18000
			} else if windowName == "weekly" || windowName == "week" {
				label = "Weekly limit"
				windowSeconds = 604800
			}
			var labelMessage messages.Message
			if windowSeconds > 0 || label == "Quota" {
				labelMessage = messages.Literal(label)
			}
			groupRows = append(groupRows, quotaRow{
				Label: label, GroupLabel: groupLabel, LabelMessage: labelMessage, GroupMessage: groupMessage,
				RemainingPercent: remaining, windowSeconds: windowSeconds,
				ResetAt: quotaResetAt(firstString(bucket, "resetTime", "reset_time")),
			})
		}
		sort.SliceStable(groupRows, func(i, j int) bool {
			return antigravityWindowOrder(groupRows[i].windowSeconds) < antigravityWindowOrder(groupRows[j].windowSeconds)
		})
		rows = append(rows, groupRows...)
	}
	return rows
}

func antigravityRemainingPercent(bucket map[string]any) *float64 {
	for _, key := range []string{"remainingFraction", "remaining_fraction"} {
		var value float64
		var err error
		scale := 100.0
		switch raw := bucket[key].(type) {
		case float64:
			value = raw
		case json.Number:
			value, err = raw.Float64()
		case string:
			raw = strings.TrimSpace(raw)
			if text, percent := strings.CutSuffix(raw, "%"); percent {
				raw, scale = strings.TrimSpace(text), 1
			}
			value, err = strconv.ParseFloat(raw, 64)
		default:
			continue
		}
		if err == nil && !math.IsNaN(value) && !math.IsInf(value, 0) {
			return remainingPercent(value * scale)
		}
	}
	return nil
}

func antigravityWindowOrder(seconds int64) int {
	switch seconds {
	case 18000:
		return 0
	case 604800:
		return 1
	default:
		return 2
	}
}
