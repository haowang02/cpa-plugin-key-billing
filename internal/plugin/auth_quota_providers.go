package plugin

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"cpa-key-billing/internal/messages"
)

func (a *App) fetchAuthQuota(callbackID string, file hostAuthFile) (authQuotaResponse, error) {
	client, errClient := a.openAuthQuotaClient(callbackID, file)
	if errClient != nil {
		return authQuotaResponse{}, errClient
	}
	defer client.close()
	result := authQuotaResponse{AuthRevision: authFileRevision(file), FetchedAt: time.Now().UTC(), Quota: []quotaRow{}}
	var err error
	switch authCategory(file.Type) {
	case "codex":
		err = client.fetchCodexQuota(&result)
	case "claude":
		err = client.fetchClaudeQuota(&result)
	case "kimi":
		err = client.fetchKimiQuota(&result)
	case "xai":
		err = client.fetchXAIQuota(&result)
	case "antigravity":
		projectID := firstNonEmptyString(file.ProjectID, credentialString(client.credential, "project_id", "projectId", "gemini_virtual_project"))
		if projectID == "" {
			return result, messages.Errorf("Auth file is missing project_id")
		}
		err = client.fetchAntigravityQuota(projectID, &result)
	}
	return result, err
}

func (a *App) resetAuthQuota(callbackID string, file hostAuthFile, resetID string) (bool, error) {
	var reset func(*authQuotaClient, string) error
	switch authCategory(file.Type) {
	case "codex":
		reset = (*authQuotaClient).resetCodexQuota
	case "claude":
		reset = (*authQuotaClient).resetClaudeQuota
	default:
		return false, fmt.Errorf("Quota resets are not supported for this auth file type")
	}
	client, err := a.openAuthQuotaClient(callbackID, file)
	if err != nil {
		return false, err
	}
	defer client.close()
	if err := reset(client, resetID); err != nil {
		return false, err
	}
	// Upstream reset already succeeded. A missing optional callback or local
	// cooldown error must never turn this into a failed, retryable redemption.
	_, err = a.hostCaller(hostRoutingResetCooldown, map[string]string{"auth_index": file.AuthIndex, "host_callback_id": callbackID})
	return err == nil, nil
}

// Codex plans are labelled like the official Codex client.
var codexPlanLabels = map[string]string{
	"promax": "Pro 500", "pro": "Pro 200", "prolite": "Pro 100", "pro-lite": "Pro 100", "pro_lite": "Pro 100",
	"plus": "Plus", "go": "Go", "free": "Free",
	"self_serve_business_prolite": "Business Premium", "team": "Business", "self_serve_business_usage_based": "Business",
	"business": "Enterprise", "enterprise": "Enterprise", "ent26": "Enterprise", "enterprise_cbp_usage_based": "Enterprise",
	"hc": "Enterprise", "enterprise_cbp_automation": "Enterprise (Automation)",
	"edu_pro": "Edu Pro", "edu_plus": "Edu Plus", "edu": "Edu", "education": "Edu",
}

func codexPlanLabel(plan string) string {
	plan = strings.TrimSpace(plan)
	if label := codexPlanLabels[strings.ToLower(plan)]; label != "" {
		return label
	}
	return plan
}

var creditBalancePattern = regexp.MustCompile(`^\d+(?:\.\d+)?$`)

func codexCreditBalance(value any) string {
	var balance string
	switch value := value.(type) {
	case string:
		balance = strings.TrimSpace(value)
	case float64:
		balance = strconv.FormatFloat(value, 'f', -1, 64)
	}
	if !creditBalancePattern.MatchString(balance) {
		return ""
	}
	return balance
}

func (c *authQuotaClient) fetchCodexQuota(result *authQuotaResponse) error {
	result.Plan = codexPlanLabel(credentialString(c.credential, "plan_type", "planType"))
	headers := http.Header{"User-Agent": {"codex_cli_rs/0.76.0"}, "Content-Type": {"application/json"}}
	if accountID := credentialString(c.credential, "account_id", "accountId", "chatgpt_account_id", "chatgptAccountId"); accountID != "" {
		headers.Set("Chatgpt-Account-Id", accountID)
	}
	object, errCall := c.upstream(http.MethodGet, "https://chatgpt.com/backend-api/wham/usage", headers, nil)
	if errCall != nil {
		return errCall
	}
	if plan := firstString(object, "plan_type", "planType"); plan != "" {
		result.Plan = codexPlanLabel(plan)
	}
	credits := objectMap(object, "credits")
	result.CreditBalance, result.CreditsUnlimited = codexCreditBalance(credits["balance"]), credits["unlimited"] == true
	appendCodexRateLimit(result, codexMainLimit, objectMap(object, "rate_limit", "rateLimit"))
	appendCodexRateLimit(result, codexCodeReviewLimit, objectMap(object, "code_review_rate_limit", "codeReviewRateLimit"))
	for _, raw := range objectSlice(object, "additional_rate_limits", "additionalRateLimits") {
		additional, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		limit := codexAdditionalLimit(firstString(additional, "limit_name", "limitName"))
		appendCodexRateLimit(result, limit, objectMap(additional, "rate_limit", "rateLimit"))
	}
	usageCredits := normalizeCodexResetCredits(objectMap(object, "rate_limit_reset_credits", "rateLimitResetCredits"))
	result.RateLimitResetCreditsAvailableCount = usageCredits.availableCount
	headers.Set("Accept", "application/json")
	headers.Set("OpenAI-Beta", "codex-1")
	headers.Set("Originator", "Codex Desktop")
	credits, err := c.upstream(http.MethodGet, "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits", headers, nil)
	details := normalizeCodexResetCredits(credits)
	result.RateLimitResetCreditsUnavailable = err != nil || details.invalidPayload
	if result.RateLimitResetCreditsUnavailable {
		return nil
	}
	result.RateLimitResetCredits = details.credits
	// Match CPAMC: detail count, then non-empty available details, then usage count.
	if details.availableCount != nil {
		result.RateLimitResetCreditsAvailableCount = details.availableCount
	} else if count := len(details.credits); count > 0 {
		result.RateLimitResetCreditsAvailableCount = &count
	}
	return nil
}

type codexResetCreditsSummary struct {
	availableCount *int
	credits        []resetCreditExpiry
	invalidPayload bool
}

func normalizeCodexResetCredits(object map[string]any) codexResetCreditsSummary {
	result := codexResetCreditsSummary{invalidPayload: true}
	for _, key := range []string{"credits", "available_count", "availableCount", "applicable_available_count", "applicableAvailableCount"} {
		if _, ok := object[key]; ok {
			result.invalidPayload = false
			break
		}
	}
	if count, ok := floatValue(object, "available_count", "availableCount"); ok && !math.IsNaN(count) && !math.IsInf(count, 0) {
		value := int(count)
		result.availableCount = &value
	}
	for _, raw := range objectSlice(object, "credits") {
		credit, ok := raw.(map[string]any)
		if !ok || firstString(credit, "reset_type", "resetType") != "codex_rate_limits" || firstString(credit, "status") != "available" {
			continue
		}
		expiresAt := firstString(credit, "expires_at", "expiresAt")
		if expiresAt != "" {
			// Preserve non-empty dates as CPAMC does, even if their format is unknown.
			result.credits = append(result.credits, resetCreditExpiry{ExpiresAt: expiresAt})
		}
	}
	return result
}

func appendCodexRateLimit(result *authQuotaResponse, limit codexLimit, info map[string]any) {
	allowed, hasAllowed := boolValue(info, "allowed")
	reached, hasReached := boolValue(info, "limit_reached", "limitReached")
	windows := []quotaWindow{}
	for _, role := range []string{"Primary", "Secondary"} {
		key := strings.ToLower(role) + "_window"
		value := objectMap(info, key, camelKey(key))
		if value == nil {
			continue
		}
		seconds, _ := intValue(value, "limit_window_seconds", "limitWindowSeconds")
		window := quotaWindow{Role: role, Seconds: seconds, meter: limit.meter()}
		if used, ok := floatValue(value, "used_percent", "usedPercent"); ok {
			window.RemainingPercent = remainingPercent(100 - used)
		} else if hasReached && reached || hasAllowed && !allowed {
			window.RemainingPercent = remainingPercent(0)
		}
		if reset, ok := intValue(value, "reset_at", "resetAt"); ok && reset > 0 {
			window.ResetAt = time.Unix(reset, 0).UTC().Format(time.RFC3339)
		} else if reset, ok := intValue(value, "reset_after_seconds", "resetAfterSeconds"); ok {
			window.ResetAt = resetAtAfter(result.FetchedAt, reset)
		}
		windows = append(windows, window)
	}
	appendQuotaWindows(result, limit.key, limit.labelPrefix, windows)
}

func (c *authQuotaClient) resetCodexQuota(resetID string) error {
	headers := http.Header{"User-Agent": {"codex_cli_rs/0.76.0"}}
	if accountID := credentialString(c.credential, "account_id", "accountId", "chatgpt_account_id", "chatgptAccountId"); accountID != "" {
		headers.Set("Chatgpt-Account-Id", accountID)
	}
	_, errCall := c.upstreamCall(
		http.MethodPost, "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume",
		headers, map[string]string{"redeem_request_id": resetID},
	)
	return errCall
}

type quotaWindow struct {
	Role             string
	Seconds          int64
	RemainingPercent *float64
	ResetAt          string
	meter            *quotaMeter
}

func appendQuotaWindows(result *authQuotaResponse, limitKey, labelPrefix string, windows []quotaWindow) {
	sort.SliceStable(windows, func(i, j int) bool {
		return windows[i].order() < windows[j].order()
	})
	for _, window := range windows {
		result.Quota = append(result.Quota, window.row(limitKey, labelPrefix))
	}
}

func (window quotaWindow) row(limitKey, labelPrefix string) quotaRow {
	label := "5-hour limit"
	if window.Role == "Secondary" {
		label = "Weekly limit"
	}
	switch seconds := window.Seconds; {
	case seconds == 5*60*60:
		label = "5-hour limit"
	case seconds == 7*24*60*60:
		label = "Weekly limit"
	case seconds >= 28*24*60*60 && seconds <= 31*24*60*60:
		label = "Monthly limit"
	case seconds > 0:
		label = strconv.FormatInt(seconds/60, 10) + " min"
	}
	return quotaRow{Label: labelPrefix + label, LabelMessage: messages.Literal(label), LabelPrefix: labelPrefix, LimitKey: limitKey,
		RemainingPercent: window.RemainingPercent, ResetAt: window.ResetAt, windowSeconds: window.Seconds, meter: window.meter}
}

func (window quotaWindow) order() int {
	seconds, role := window.Seconds, window.Role
	switch {
	case seconds == 5*60*60:
		return 0
	case seconds == 7*24*60*60 || seconds >= 28*24*60*60 && seconds <= 31*24*60*60:
		return 1
	case role == "Primary":
		return 2
	default:
		return 3
	}
}

func claudeFableLimit(usage map[string]any) map[string]any {
	var fallback map[string]any
	for _, raw := range objectSlice(usage, "limits") {
		limit, ok := raw.(map[string]any)
		if !ok || !strings.EqualFold(firstString(limit, "kind"), "weekly_scoped") {
			continue
		}
		model := objectMap(objectMap(limit, "scope"), "model")
		name := strings.ToLower(firstString(model, "display_name", "displayName"))
		if name != "fable" && name != "fable 5" {
			continue
		}
		if _, okPercent := floatValue(limit, "percent"); !okPercent {
			continue
		}
		if active, okActive := boolValue(limit, "is_active", "isActive"); okActive && active {
			return limit
		}
		if fallback == nil {
			fallback = limit
		}
	}
	return fallback
}

func (c *authQuotaClient) fetchClaudeQuota(result *authQuotaResponse) error {
	headers := http.Header{"Anthropic-Beta": {"oauth-2025-04-20"}, "Content-Type": {"application/json"}}
	usage, errCall := c.upstream(http.MethodGet, "https://api.anthropic.com/api/oauth/usage", headers, nil)
	if errCall != nil {
		return errCall
	}
	fable := claudeFableLimit(usage)
	const fiveHours, week = 5 * 60 * 60, 7 * 24 * 60 * 60
	for _, window := range []struct {
		key, label, limit string
		seconds           int64
		meter             *quotaMeter
	}{
		{"five_hour", "5-hour limit", claudeUnifiedLimit, fiveHours, &quotaMeter{}},
		{"seven_day", "Weekly limit", claudeUnifiedLimit, week, &quotaMeter{}},
		{"seven_day_oauth_apps", "OAuth Apps weekly limit", "", week, nil},
		{"seven_day_opus", "Opus weekly limit", "", week, claudeModelMeter("opus")},
		{"seven_day_sonnet", "Sonnet weekly limit", "", week, claudeModelMeter("sonnet")},
		{"seven_day_cowork", "Cowork weekly limit", "", week, nil},
		{"iguana_necktie", "Iguana Necktie", "", 0, nil},
	} {
		if window.key == "iguana_necktie" && fable != nil {
			continue
		}
		value := objectMap(usage, window.key, camelKey(window.key))
		if value == nil {
			continue
		}
		row := quotaRow{Label: window.label, LabelMessage: messages.Literal(window.label), ResetAt: quotaResetAt(firstString(value, "resets_at", "resetsAt")),
			LimitKey: window.limit, windowSeconds: window.seconds, meter: window.meter}
		if percent, ok := floatValue(value, "utilization"); ok {
			row.RemainingPercent = remainingPercent(100 - percent)
		}
		result.Quota = append(result.Quota, row)
	}
	if fable != nil {
		percent, _ := floatValue(fable, "percent")
		result.Quota = append(result.Quota, quotaRow{Label: "Fable weekly limit", LabelMessage: messages.New("Fable weekly limit"),
			RemainingPercent: remainingPercent(100 - percent), ResetAt: quotaResetAt(firstString(fable, "resets_at", "resetsAt")), windowSeconds: week, meter: claudeModelMeter("fable")})
	}
	if extra := objectMap(usage, "extra_usage", "extraUsage"); extra != nil {
		enabled, hasEnabled := boolValue(extra, "is_enabled", "isEnabled")
		if !hasEnabled || enabled {
			row := quotaRow{Label: "Extra usage", LabelMessage: messages.New("Extra usage"), Currency: "USD"}
			if value, ok := usdValue(extra, "used_credits", "usedCredits"); ok {
				row.Used = floatPointer(value)
			}
			if value, ok := usdValue(extra, "monthly_limit", "monthlyLimit"); ok {
				row.Limit = floatPointer(value)
			}
			if value, ok := floatValue(extra, "utilization"); ok {
				row.RemainingPercent = remainingPercent(100 - value)
			}
			result.Quota = append(result.Quota, row)
		}
	}
	if status, errStatus := c.claudeResetStatus(headers); errStatus == nil {
		count := 0
		for _, grant := range status.grants {
			count += grant.left
		}
		result.RateLimitResetCreditsAvailableCount = &count
	}
	if profile, errProfile := c.upstream(http.MethodGet, "https://api.anthropic.com/api/oauth/profile", headers, nil); errProfile == nil {
		account, organization := objectMap(profile, "account"), objectMap(profile, "organization")
		max, hasMax := boolValue(account, "has_claude_max", "hasClaudeMax")
		pro, hasPro := boolValue(account, "has_claude_pro", "hasClaudePro")
		plan := ""
		if hasMax && max {
			plan = "Max"
		} else if hasPro && pro {
			plan = "Pro"
		} else if strings.EqualFold(firstString(organization, "organization_type", "organizationType"), "claude_team") && strings.EqualFold(firstString(organization, "subscription_status", "subscriptionStatus"), "active") {
			plan = "Team"
		} else if hasMax && hasPro && !max && !pro {
			plan = "Free"
		}
		if plan != "" {
			result.Plan = plan
		}
	}
	return nil
}

var (
	errClaudeResetNotLimited = messages.Errorf("No reset was used: the Claude account is not rate limited")
	errClaudeResetCooldown   = messages.Errorf("No reset was used: a Claude reset cooldown is active")
	errClaudeResetUnusable   = messages.Errorf("No reset was used: no Claude reset grant is usable")
)

var (
	claudeOrganizationPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	claudeResetGrantIDPattern = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
)

// The claim re-reads the grants and spends one only when it is usable now.
func (c *authQuotaClient) resetClaudeQuota(resetID string) error {
	headers := http.Header{"Anthropic-Beta": {"oauth-2025-04-20"}}
	profile, errProfile := c.upstream(http.MethodGet, "https://api.anthropic.com/api/oauth/profile", headers, nil)
	if errProfile != nil {
		return errProfile
	}
	organization := strings.ToLower(firstString(objectMap(profile, "organization"), "uuid"))
	if !claudeOrganizationPattern.MatchString(organization) {
		return messages.Errorf("Invalid upstream response")
	}
	status, errStatus := c.claudeResetStatus(headers)
	if errStatus != nil {
		return errStatus
	}
	grantID, errGrant := status.usableGrant(time.Now())
	if errGrant != nil {
		return errGrant
	}
	response, errClaim := c.upstreamCall(
		http.MethodPost, "https://api.anthropic.com/api/organizations/"+organization+"/reset_rate_limits",
		headers, map[string]string{"program": "cedar_ember", "grant_id": grantID, "request_id": resetID},
	)
	if errClaim != nil {
		return errClaim
	}
	switch firstString(response, "result") {
	case "reset", "already_used":
		return nil
	case "not_limited":
		return errClaudeResetNotLimited
	case "cooldown":
		return errClaudeResetCooldown
	case "ineligible", "unavailable":
		return errClaudeResetUnusable
	}
	return messages.Errorf("Claude did not confirm the reset")
}

func (c *authQuotaClient) claudeResetStatus(headers http.Header) (claudeResetStatus, error) {
	object, errCall := c.upstream(http.MethodGet, "https://api.anthropic.com/api/oauth/usage?cedar_ember=1&skip_spend=1", headers, nil)
	if errCall != nil {
		return claudeResetStatus{}, errCall
	}
	status, ok := parseClaudeResetStatus(objectMap(object, "cedar_ember"))
	if !ok {
		return claudeResetStatus{}, messages.Errorf("Invalid upstream response")
	}
	return status, nil
}

// claudeResetStatus is Anthropic's cedar_ember reset-grant block.
type claudeResetStatus struct {
	eligible, atLimit bool
	next              string
	cooldownUntil     time.Time
	grants            []claudeResetGrant
}

type claudeResetGrant struct {
	id                         string
	left                       int
	startsAt, endsAt           time.Time
	paused, usable, needsLimit bool
}

// One malformed grant rejects the whole block, so a partial read never offers a reset.
func parseClaudeResetStatus(block map[string]any) (claudeResetStatus, bool) {
	eligible, okEligible := block["eligible"].(bool)
	rawGrants, okGrants := block["grants"].([]any)
	atLimit, okLimit := strictFlag(block, "at_limit", false)
	cooldown, okCooldown := strictTime(block, "cooldown_until")
	if !okEligible || block["grants"] != nil && !okGrants || !okLimit || !okCooldown {
		return claudeResetStatus{}, false
	}
	status := claudeResetStatus{eligible: eligible, atLimit: atLimit, cooldownUntil: cooldown}
	seen := map[string]bool{}
	for _, raw := range rawGrants {
		item, _ := raw.(map[string]any)
		grant, ok := parseClaudeResetGrant(item)
		if !ok || seen[grant.id] {
			return claudeResetStatus{}, false
		}
		seen[grant.id] = true
		status.grants = append(status.grants, grant)
	}
	if next, _ := block["next_grant_id"].(string); seen[next] {
		status.next = next
	}
	return status, true
}

func parseClaudeResetGrant(item map[string]any) (claudeResetGrant, bool) {
	id, _ := item["id"].(string)
	total, okTotal := item["resets_total"].(float64)
	left, okLeft := item["resets_left"].(float64)
	startsAt, okStart := strictTime(item, "starts_at")
	endsAt, okEnd := strictTime(item, "ends_at")
	paused, okPaused := strictFlag(item, "paused", false)
	// A grant that does not say it is usable is not offered for spending.
	usable, okUsable := strictFlag(item, "usable_now", false)
	needsLimit, okNeeds := strictFlag(item, "use_requires_limit", true)
	ok := claudeResetGrantIDPattern.MatchString(id) && okTotal && okLeft && left >= 0 && left <= total &&
		left == math.Trunc(left) && total == math.Trunc(total) && okStart && okEnd && okPaused && okUsable && okNeeds
	return claudeResetGrant{id: id, left: int(left), startsAt: startsAt, endsAt: endsAt, paused: paused, usable: usable, needsLimit: needsLimit}, ok
}

// usableGrant prefers the upstream recommendation, then the lowest usable ID.
func (s claudeResetStatus) usableGrant(now time.Time) (string, error) {
	if s.cooldownUntil.After(now) {
		return "", errClaudeResetCooldown
	}
	var usable []string
	notLimited := false
	for _, grant := range s.grants {
		if !s.eligible || grant.paused || !grant.usable || grant.left <= 0 || grant.startsAt.After(now) || !grant.endsAt.IsZero() && !grant.endsAt.After(now) {
			continue
		}
		if grant.needsLimit && !s.atLimit {
			notLimited = true
			continue
		}
		if grant.id == s.next {
			return grant.id, nil
		}
		usable = append(usable, grant.id)
	}
	if len(usable) > 0 {
		return slices.Min(usable), nil
	}
	if notLimited {
		return "", errClaudeResetNotLimited
	}
	return "", errClaudeResetUnusable
}

func strictFlag(object map[string]any, key string, fallback bool) (bool, bool) {
	switch value := object[key].(type) {
	case nil:
		return fallback, true
	case bool:
		return value, true
	}
	return false, false
}

func strictTime(object map[string]any, key string) (time.Time, bool) {
	switch value := object[key].(type) {
	case nil:
		return time.Time{}, true
	case string:
		parsed, err := time.Parse(time.RFC3339, value)
		return parsed, err == nil
	}
	return time.Time{}, false
}

func (c *authQuotaClient) fetchKimiQuota(result *authQuotaResponse) error {
	usage, errCall := c.upstream(http.MethodGet, "https://api.kimi.com/coding/v1/usages", nil, nil)
	if errCall != nil {
		return errCall
	}
	for _, raw := range objectSlice(usage, "limits") {
		limit, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		detail := objectMap(limit, "detail")
		values := limit
		if meaningfulQuotaValues(detail) {
			values = detail
		}
		windowSeconds := kimiWindowSeconds(limit)
		label := firstNonEmptyString(firstString(limit, "name", "title"), firstString(detail, "name", "title"), windowLabel(windowSeconds), "Quota")
		row := quotaRow{Label: label, windowSeconds: windowSeconds, meter: &quotaMeter{},
			ResetAt: quotaResetAt(firstString(limit, "reset_at", "resetAt", "resetTime"), firstString(detail, "reset_at", "resetAt", "resetTime"))}
		if firstNonEmptyString(firstString(limit, "name", "title"), firstString(detail, "name", "title")) == "" {
			row.LabelMessage = messages.Literal(label)
		}
		fillQuotaValues(&row, values)
		if row.ResetAt == "" {
			if reset, ok := resetSeconds(limit, detail); ok {
				row.ResetAt = resetAtAfter(result.FetchedAt, reset)
			}
		}
		result.Quota = append(result.Quota, row)
	}
	if summary := objectMap(usage, "usage"); meaningfulQuotaValues(summary) {
		row := quotaRow{Label: firstNonEmptyString(firstString(summary, "title"), "Weekly limit"), ResetAt: quotaResetAt(firstString(summary, "reset_at", "resetAt", "resetTime"))}
		if firstString(summary, "title") == "" {
			row.LabelMessage = messages.New("Weekly limit")
		}
		fillQuotaValues(&row, summary)
		if row.ResetAt == "" {
			if reset, ok := resetSeconds(summary); ok {
				row.ResetAt = resetAtAfter(result.FetchedAt, reset)
			}
		}
		result.Quota = append(result.Quota, row)
	}
	return nil
}

func kimiWindowSeconds(limit map[string]any) int64 {
	window, detail := objectMap(limit, "window"), objectMap(limit, "detail")
	duration, hasDuration := intValue(window, "duration")
	if !hasDuration {
		duration, hasDuration = intValue(limit, "duration")
	}
	if !hasDuration {
		duration, hasDuration = intValue(detail, "duration")
	}
	if !hasDuration {
		return 0
	}
	unit := firstNonEmptyString(firstString(window, "timeUnit", "time_unit"), firstString(limit, "timeUnit", "time_unit"), firstString(detail, "timeUnit", "time_unit"))
	normalized := strings.TrimPrefix(strings.ToLower(unit), "time_unit_")
	multipliers := map[string]int64{"s": 1, "second": 1, "seconds": 1, "m": 60, "minute": 60, "minutes": 60, "h": 3600, "hour": 3600, "hours": 3600, "d": 86400, "day": 86400, "days": 86400, "w": 604800, "week": 604800, "weeks": 604800}
	multiplier := multipliers[normalized]
	if multiplier == 0 {
		multiplier = 60
	}
	return duration * multiplier
}

func windowLabel(seconds int64) string {
	switch seconds {
	case 18000:
		return "5-hour limit"
	case 604800:
		return "Weekly limit"
	case 2592000:
		return "Monthly limit"
	}
	return ""
}

func resetSeconds(objects ...map[string]any) (int64, bool) {
	for _, object := range objects {
		if value, ok := floatValue(object, "reset_in", "resetIn", "ttl"); ok && value >= 0 {
			return int64(value), true
		}
	}
	return 0, false
}

func meaningfulQuotaValues(object map[string]any) bool {
	if object == nil {
		return false
	}
	for _, key := range []string{"used", "limit", "remaining"} {
		if _, ok := floatValue(object, key); ok {
			return true
		}
	}
	return false
}

func fillQuotaValues(row *quotaRow, object map[string]any) {
	used, hasUsed := floatValue(object, "used")
	limit, hasLimit := floatValue(object, "limit")
	remaining, hasRemaining := floatValue(object, "remaining")
	if !hasUsed && hasLimit {
		used = 0
		if hasRemaining {
			used = limit - remaining
		}
		hasUsed = true
	}
	if hasUsed {
		row.Used = floatPointer(used)
	}
	if hasLimit {
		row.Limit = floatPointer(limit)
	}
	if hasLimit && limit > 0 && hasUsed {
		row.RemainingPercent = remainingPercent((1 - used/limit) * 100)
	}
}

const xaiGrokClientVersion = "1.0.44"

func (c *authQuotaClient) fetchXAIQuota(result *authQuotaResponse) error {
	result.Plan = xaiPlan(c.token)
	headers := http.Header{
		"X-Xai-Token-Auth":      {"xai-grok-cli"},
		"X-Grok-Client-Version": {xaiGrokClientVersion},
		"User-Agent":            {"grok-pager/" + xaiGrokClientVersion + " grok-shell/" + xaiGrokClientVersion},
		"Accept":                {"*/*"},
	}
	if userID := credentialString(c.credential, "user_id", "userId", "xai_user_id", "xaiUserId", "sub", "subject"); userID != "" {
		headers.Set("X-Userid", userID)
	}
	weekly, weeklyErr := c.upstream(http.MethodGet, "https://cli-chat-proxy.grok.com/v1/billing?format=credits", headers.Clone(), nil)
	monthly, monthlyErr := c.upstream(http.MethodGet, "https://cli-chat-proxy.grok.com/v1/billing", headers.Clone(), nil)
	if weeklyErr != nil && monthlyErr != nil {
		return weeklyErr
	}
	weeklyConfig, monthlyConfig := objectMap(weekly, "config"), objectMap(monthly, "config")
	if weeklyConfig == nil {
		weeklyConfig = objectMap(objectMap(weekly, "body"), "config")
	}
	if monthlyConfig == nil {
		monthlyConfig = objectMap(objectMap(monthly, "body"), "config")
	}
	if percent, ok := floatValue(weeklyConfig, "creditUsagePercent", "credit_usage_percent"); ok {
		resetAt, start := xaiResetAt(weeklyConfig), firstString(objectMap(weeklyConfig, "currentPeriod", "current_period"), "start")
		result.Quota = append(result.Quota, quotaRow{Label: "Weekly limit", LabelMessage: messages.New("Weekly limit"), RemainingPercent: remainingPercent(100 - percent),
			ResetAt: resetAt, windowSeconds: periodSeconds(start, resetAt), meter: &quotaMeter{}})
	}
	prepaid, hasPrepaid := usdValue(weeklyConfig, "prepaidBalance", "prepaid_balance")
	if !hasPrepaid {
		prepaid, hasPrepaid = usdValue(monthlyConfig, "prepaidBalance", "prepaid_balance")
	}
	if hasPrepaid && prepaid > 0 {
		result.CreditBalance, result.CreditCurrency = strconv.FormatFloat(prepaid, 'f', -1, 64), "USD"
	}
	monthlyLimit, hasLimit := usdValue(monthlyConfig, "monthlyLimit", "monthly_limit")
	totalUsed, hasUsed := usdValue(monthlyConfig, "used")
	onDemandCap, hasOnDemandCap := usdValue(monthlyConfig, "onDemandCap", "on_demand_cap")
	onDemandUsed, hasOnDemandUsed := usdValue(monthlyConfig, "onDemandUsed", "on_demand_used")
	if !hasOnDemandUsed && hasUsed && hasLimit {
		onDemandUsed = math.Max(0, totalUsed-monthlyLimit)
		hasOnDemandUsed = true
	}
	if hasLimit || hasUsed {
		resetAt := quotaResetAt(firstString(monthlyConfig, "billingPeriodEnd", "billing_period_end"))
		row := quotaRow{Label: "Monthly allowance", LabelMessage: messages.New("Monthly allowance"), Currency: "USD", ResetAt: resetAt,
			windowSeconds: periodSeconds(firstString(monthlyConfig, "billingPeriodStart", "billing_period_start"), resetAt), meter: &quotaMeter{}}
		if hasLimit {
			row.Limit = floatPointer(monthlyLimit)
		}
		if hasUsed {
			included := totalUsed
			if hasLimit {
				included = math.Min(totalUsed, math.Max(0, monthlyLimit))
			}
			row.Used = floatPointer(included)
			if hasLimit {
				if monthlyLimit > 0 {
					row.RemainingPercent = remainingPercent((1 - included/monthlyLimit) * 100)
				}
			}
		}
		result.Quota = append(result.Quota, row)
	}
	if hasOnDemandCap && onDemandCap > 0 {
		row := quotaRow{Label: "Pay-as-you-go allowance", LabelMessage: messages.New("Pay-as-you-go allowance"), Currency: "USD", Limit: floatPointer(onDemandCap), ResetAt: quotaResetAt(firstString(monthlyConfig, "billingPeriodEnd", "billing_period_end"))}
		if hasOnDemandUsed {
			row.Used = floatPointer(onDemandUsed)
			row.RemainingPercent = remainingPercent((1 - onDemandUsed/onDemandCap) * 100)
		}
		result.Quota = append(result.Quota, row)
	}
	type productQuota struct {
		name, normalized string
		percent          float64
	}
	products := make(map[string]productQuota)
	for _, raw := range objectSlice(weeklyConfig, "productUsage", "product_usage") {
		product, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := strings.Join(strings.Fields(firstString(product, "product")), " ")
		percent, ok := floatValue(product, "usagePercent", "usage_percent")
		if name == "" || !ok {
			continue
		}
		normalized := strings.ToLower(name)
		if current, exists := products[normalized]; !exists || percent > current.percent {
			products[normalized] = productQuota{name: name, normalized: normalized, percent: percent}
		}
	}
	ordered := make([]productQuota, 0, len(products))
	for _, product := range products {
		ordered = append(ordered, product)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].normalized < ordered[j].normalized })
	for _, product := range ordered {
		name, percent := product.name, product.percent
		result.Quota = append(result.Quota, quotaRow{Label: name + " usage", LabelMessage: messages.New("%s usage", name), RemainingPercent: remainingPercent(100 - percent), ResetAt: xaiResetAt(weeklyConfig)})
	}
	return nil
}

// xAI signs the account tier into its access token; any tier above free is paid.
func xaiPlan(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, errDecode := base64.RawURLEncoding.DecodeString(parts[1])
	if errDecode != nil {
		return ""
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	for key := range claims {
		normalized := strings.ToLower(key)
		if normalized == "tier" || strings.HasSuffix(normalized, "/tier") || strings.HasSuffix(normalized, ":tier") {
			if tier, ok := floatValue(claims, key); ok && tier >= 1 {
				return "Paid"
			}
		}
	}
	return ""
}

func xaiResetAt(config map[string]any) string {
	return quotaResetAt(
		firstString(objectMap(config, "currentPeriod", "current_period"), "end"),
		firstString(config, "billingPeriodEnd", "billing_period_end"),
	)
}

func objectMap(object map[string]any, keys ...string) map[string]any {
	if object == nil {
		return nil
	}
	for _, key := range keys {
		if value, ok := object[key].(map[string]any); ok {
			return value
		}
	}
	return nil
}

func objectSlice(object map[string]any, keys ...string) []any {
	if object == nil {
		return nil
	}
	for _, key := range keys {
		if value, ok := object[key].([]any); ok {
			return value
		}
	}
	return nil
}

func firstString(object map[string]any, keys ...string) string {
	if object == nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func floatValue(object map[string]any, keys ...string) (float64, bool) {
	if object == nil {
		return 0, false
	}
	for _, key := range keys {
		switch value := object[key].(type) {
		case float64:
			return value, true
		case json.Number:
			parsed, err := value.Float64()
			return parsed, err == nil
		case string:
			parsed, err := strconv.ParseFloat(value, 64)
			return parsed, err == nil
		}
	}
	return 0, false
}

func intValue(object map[string]any, keys ...string) (int64, bool) {
	value, ok := floatValue(object, keys...)
	return int64(value), ok
}

func boolValue(object map[string]any, keys ...string) (bool, bool) {
	if object == nil {
		return false, false
	}
	for _, key := range keys {
		switch value := object[key].(type) {
		case bool:
			return value, true
		case float64:
			return value != 0, true
		case string:
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "true", "1", "yes", "y", "on":
				return true, true
			case "false", "0", "no", "n", "off":
				return false, true
			}
		}
	}
	return false, false
}

func floatPointer(value float64) *float64 { return &value }

func remainingPercent(value float64) *float64 {
	return floatPointer(math.Max(0, math.Min(100, value)))
}

func resetAtAfter(fetchedAt time.Time, seconds int64) string {
	if fetchedAt.IsZero() || seconds < 0 {
		return ""
	}
	return fetchedAt.Add(time.Duration(seconds) * time.Second).UTC().Format(time.RFC3339)
}

func quotaResetAt(values ...string) string {
	for _, value := range values {
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
		if err == nil {
			return parsed.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

func camelKey(value string) string {
	parts := strings.Split(value, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

func usdValue(object map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if nested := objectMap(object, key); nested != nil {
			value, ok := floatValue(nested, "val")
			return value / 100, ok
		}
		if value, ok := floatValue(object, key); ok {
			return value / 100, true
		}
	}
	return 0, false
}
