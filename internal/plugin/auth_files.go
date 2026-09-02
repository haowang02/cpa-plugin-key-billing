package plugin

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	hostAuthList = "host.auth.list"
	hostAuthGet  = "host.auth.get"
	hostHTTPDo   = "host.http.do"
)

type HostCaller func(method string, payload any) (json.RawMessage, error)

type hostAuthFile struct {
	AuthIndex   string    `json:"auth_index"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Disabled    bool      `json:"disabled"`
	Unavailable bool      `json:"unavailable"`
	Email       string    `json:"email"`
	ProjectID   string    `json:"project_id"`
	AccountType string    `json:"account_type"`
	RuntimeOnly bool      `json:"runtime_only"`
	ModTime     time.Time `json:"modtime"`
}

type authFileView struct {
	AuthIndex      string `json:"auth_index"`
	Name           string `json:"name"`
	Category       string `json:"category"`
	Email          string `json:"email,omitempty"`
	Disabled       bool   `json:"disabled"`
	Unavailable    bool   `json:"unavailable"`
	QuotaSupported bool   `json:"quota_supported"`
	QuotaReason    string `json:"quota_unavailable_reason,omitempty"`
	CacheRevision  string `json:"cache_revision,omitempty"`
}

type authFileListResponse struct {
	Files []authFileView `json:"files"`
}

type hostAuthListResponse struct {
	Files []hostAuthFile `json:"files"`
}

type hostAuthGetResponse struct {
	JSON json.RawMessage `json:"json"`
}

type hostHTTPRequest struct {
	HostCallbackID string      `json:"host_callback_id,omitempty"`
	Method         string      `json:"method"`
	URL            string      `json:"url"`
	Headers        http.Header `json:"headers,omitempty"`
	Body           []byte      `json:"body,omitempty"`
}

type hostHTTPResponse struct {
	StatusCode int    `json:"StatusCode"`
	Body       []byte `json:"Body"`
}

type quotaRow struct {
	Label             string   `json:"label,omitempty"`
	MoneyCents        bool     `json:"money_cents,omitempty"`
	GroupLabel        string   `json:"group_label,omitempty"`
	Used              *float64 `json:"used,omitempty"`
	Limit             *float64 `json:"limit,omitempty"`
	Remaining         *float64 `json:"remaining,omitempty"`
	UsedPercent       *float64 `json:"used_percent,omitempty"`
	RemainingFraction *float64 `json:"remaining_fraction,omitempty"`
	ResetAt           string   `json:"reset_at,omitempty"`
	ResetAfterSeconds *int64   `json:"reset_after_seconds,omitempty"`
	windowSeconds     int64
}

type authQuotaResponse struct {
	AuthRevision                        string     `json:"auth_revision,omitempty"`
	FetchedAt                           time.Time  `json:"fetched_at"`
	Plan                                string     `json:"plan,omitempty"`
	RateLimitResetCreditsAvailableCount *int       `json:"rate_limit_reset_credits_available_count,omitempty"`
	Quota                               []quotaRow `json:"quota"`
}

func (a *App) authFiles(access viewAccess) ManagementResponse {
	if access.APIKey && !access.Tracked {
		return apiKeyUnauthorized()
	}
	files, errList := a.listAuthFiles()
	if errList != nil {
		return viewJSONError(access, http.StatusBadGateway, "host_unavailable", errList.Error())
	}
	return viewJSON(access, http.StatusOK, authFileListResponse{Files: files})
}

func (a *App) authQuota(req ManagementRequest, access viewAccess) ManagementResponse {
	if access.APIKey && !access.Tracked {
		return apiKeyUnauthorized()
	}
	authIndex := strings.TrimSpace(req.Query.Get("auth_index"))
	if authIndex == "" || len(authIndex) > 512 {
		return viewJSONError(access, http.StatusBadRequest, "invalid", "auth_index 无效")
	}
	files, errList := a.listHostAuthFiles()
	if errList != nil {
		return viewJSONError(access, http.StatusBadGateway, "host_unavailable", errList.Error())
	}
	var selected *hostAuthFile
	for i := range files {
		if files[i].AuthIndex == authIndex {
			selected = &files[i]
			break
		}
	}
	if selected == nil {
		return viewJSONError(access, http.StatusNotFound, "not_found", "认证文件不存在")
	}
	if strings.EqualFold(strings.TrimSpace(selected.AccountType), "api_key") {
		return viewJSONError(access, http.StatusNotFound, "not_found", "认证文件不存在")
	}
	if selected.Disabled {
		return viewJSONError(access, http.StatusUnprocessableEntity, "disabled", "认证文件已停用")
	}
	provider := authCategory(selected.Type)
	if authCategoryOrder(provider) == 5 {
		return viewJSONError(access, http.StatusUnprocessableEntity, "unsupported", "该认证文件类型暂不支持限额查询")
	}
	if selected.RuntimeOnly {
		return viewJSONError(access, http.StatusUnprocessableEntity, "unsupported", "运行时认证文件没有可读取的物理凭据")
	}
	result, errQuota := a.fetchAuthQuota(req.HostCallbackID, *selected, provider)
	if errQuota != nil {
		return viewJSONError(access, http.StatusBadGateway, "quota_failed", errQuota.Error())
	}
	return viewJSON(access, http.StatusOK, result)
}

func (a *App) listAuthFiles() ([]authFileView, error) {
	files, errList := a.listHostAuthFiles()
	if errList != nil {
		return nil, errList
	}
	views := make([]authFileView, 0, len(files))
	for _, file := range files {
		if strings.TrimSpace(file.AuthIndex) == "" || strings.EqualFold(strings.TrimSpace(file.AccountType), "api_key") {
			continue
		}
		category := authCategory(file.Type)
		quotaSupported, quotaReason := authQuotaAvailability(file, category)
		views = append(views, authFileView{
			AuthIndex: file.AuthIndex, Name: file.Name, Category: category, Email: file.Email,
			Disabled: file.Disabled, Unavailable: file.Unavailable,
			QuotaSupported: quotaSupported, QuotaReason: quotaReason, CacheRevision: authFileRevision(file),
		})
	}
	sort.SliceStable(views, func(i, j int) bool {
		left, right := authCategoryOrder(views[i].Category), authCategoryOrder(views[j].Category)
		if left != right {
			return left < right
		}
		if left == 5 && views[i].Category != views[j].Category {
			return views[i].Category < views[j].Category
		}
		leftName, rightName := strings.ToLower(views[i].Name), strings.ToLower(views[j].Name)
		if leftName != rightName {
			return leftName < rightName
		}
		return views[i].AuthIndex < views[j].AuthIndex
	})
	return views, nil
}

func authQuotaAvailability(file hostAuthFile, category string) (bool, string) {
	if file.Disabled {
		return false, "认证文件已停用"
	}
	if file.RuntimeOnly {
		return false, "运行时认证文件没有可读取的物理凭据"
	}
	if authCategoryOrder(category) == 5 {
		return false, "该认证文件类型暂不支持限额查询"
	}
	return true, ""
}

func authFileRevision(file hostAuthFile) string {
	if file.ModTime.IsZero() {
		return ""
	}
	return file.ModTime.UTC().Format(time.RFC3339Nano)
}

func normalizeCodexPlan(plan string) string {
	display := strings.TrimSpace(plan)
	switch strings.ToLower(display) {
	case "pro":
		return "pro-20x"
	case "prolite", "pro-lite", "pro_lite":
		return "pro-5x"
	case "free", "plus", "team", "pro-5x", "pro-20x", "enterprise":
		return strings.ToLower(display)
	default:
		return display
	}
}

func (a *App) listHostAuthFiles() ([]hostAuthFile, error) {
	if a == nil || a.hostCaller == nil {
		return nil, fmt.Errorf("当前 CLIProxyAPI 不支持认证文件 host callback")
	}
	raw, errCall := a.hostCaller(hostAuthList, map[string]any{})
	if errCall != nil {
		return nil, fmt.Errorf("读取认证文件列表失败：%w", errCall)
	}
	var response hostAuthListResponse
	if errDecode := json.Unmarshal(raw, &response); errDecode != nil {
		return nil, fmt.Errorf("解析认证文件列表失败：%w", errDecode)
	}
	return response.Files, nil
}

func authCategory(authType string) string {
	return strings.ToLower(strings.TrimSpace(authType))
}

func authCategoryOrder(category string) int {
	switch category {
	case "claude":
		return 0
	case "antigravity":
		return 1
	case "codex":
		return 2
	case "xai":
		return 3
	case "kimi":
		return 4
	default:
		return 5
	}
}

func (a *App) fetchAuthQuota(callbackID string, file hostAuthFile, provider string) (authQuotaResponse, error) {
	raw, errGet := a.hostCaller(hostAuthGet, map[string]string{"auth_index": file.AuthIndex})
	if errGet != nil {
		return authQuotaResponse{}, fmt.Errorf("读取认证文件失败：%w", errGet)
	}
	var auth hostAuthGetResponse
	if errDecode := json.Unmarshal(raw, &auth); errDecode != nil {
		return authQuotaResponse{}, fmt.Errorf("解析认证文件失败：%w", errDecode)
	}
	var credential map[string]any
	if errDecode := json.Unmarshal(auth.JSON, &credential); errDecode != nil {
		return authQuotaResponse{}, fmt.Errorf("认证文件内容无效")
	}
	if credentialUsesAPIKey(credential) {
		return authQuotaResponse{}, fmt.Errorf("API Key 类型的上游认证不属于认证文件限额")
	}
	if credentialString(credential, "proxy_url", "proxyUrl") != "" {
		return authQuotaResponse{}, fmt.Errorf("当前 host callback 无法使用认证文件配置的独立代理")
	}
	result := authQuotaResponse{AuthRevision: authFileRevision(file), FetchedAt: time.Now().UTC(), Quota: []quotaRow{}}
	if provider == "xai" && paidXAICredential(credential) {
		result.Plan = "Paid"
		return result, nil
	}
	token := credentialToken(credential)
	if token == "" {
		return authQuotaResponse{}, fmt.Errorf("认证文件中没有可用凭据")
	}
	var err error
	switch provider {
	case "codex":
		if plan := credentialString(credential, "plan_type", "planType"); plan != "" {
			result.Plan = normalizeCodexPlan(plan)
		}
		err = a.fetchCodexQuota(callbackID, token, credentialString(credential, "account_id", "accountId", "chatgpt_account_id", "chatgptAccountId"), &result)
	case "claude":
		err = a.fetchClaudeQuota(callbackID, token, &result)
	case "kimi":
		err = a.fetchKimiQuota(callbackID, token, &result)
	case "xai":
		err = a.fetchXAIQuota(callbackID, token, credentialString(credential, "user_id", "userId", "xai_user_id", "xaiUserId", "sub", "subject"), &result)
	case "antigravity":
		projectID := firstNonEmptyString(file.ProjectID, credentialString(credential, "project_id", "projectId", "gemini_virtual_project"))
		if projectID == "" {
			return result, fmt.Errorf("认证文件缺少 project_id")
		}
		err = a.fetchAntigravityQuota(callbackID, token, projectID, &result)
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func (a *App) upstream(callbackID, method, endpoint, token string, headers http.Header, body any) (map[string]any, error) {
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Authorization", "Bearer "+token)
	var rawBody []byte
	if body != nil {
		headers.Set("Content-Type", "application/json")
		var errMarshal error
		rawBody, errMarshal = json.Marshal(body)
		if errMarshal != nil {
			return nil, fmt.Errorf("构造限额请求失败：%w", errMarshal)
		}
	}
	raw, errCall := a.hostCaller(hostHTTPDo, hostHTTPRequest{
		HostCallbackID: callbackID, Method: method, URL: endpoint, Headers: headers, Body: rawBody,
	})
	if errCall != nil {
		return nil, fmt.Errorf("限额请求失败：%s", redactSecret(errCall.Error(), token))
	}
	var response hostHTTPResponse
	if errDecode := json.Unmarshal(raw, &response); errDecode != nil {
		return nil, fmt.Errorf("解析限额响应失败：%w", errDecode)
	}
	var object map[string]any
	if len(response.Body) > 0 {
		_ = json.Unmarshal(response.Body, &object)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := upstreamErrorMessage(object)
		if response.StatusCode == http.StatusUnauthorized {
			if message != "" {
				message = "认证文件中的物理凭据已失效或过期：" + message
			} else {
				message = "认证文件中的物理凭据已失效或过期"
			}
		}
		if message == "" {
			message = http.StatusText(response.StatusCode)
		}
		message = redactSecret(message, token)
		return nil, fmt.Errorf("上游返回 HTTP %d：%s", response.StatusCode, message)
	}
	if object == nil {
		return nil, fmt.Errorf("上游返回了无效 JSON")
	}
	return object, nil
}

func upstreamErrorMessage(object map[string]any) string {
	message := firstString(object, "message", "error_description")
	if nested := objectMap(object, "error"); nested != nil {
		message = firstNonEmptyString(firstString(nested, "message", "detail"), message)
	}
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 500 {
		message = message[:500] + "…"
	}
	return message
}

func (a *App) fetchCodexQuota(callbackID, token, accountID string, result *authQuotaResponse) error {
	headers := http.Header{"User-Agent": {"codex_cli_rs/0.76.0"}, "Content-Type": {"application/json"}}
	if accountID != "" {
		headers.Set("Chatgpt-Account-Id", accountID)
	}
	object, errCall := a.upstream(callbackID, http.MethodGet, "https://chatgpt.com/backend-api/wham/usage", token, headers, nil)
	if errCall != nil {
		return errCall
	}
	if plan := firstString(object, "plan_type", "planType"); plan != "" {
		result.Plan = normalizeCodexPlan(plan)
	}
	appendCodexRateLimit(&result.Quota, "", objectMap(object, "rate_limit", "rateLimit"))
	appendCodexRateLimit(&result.Quota, "Code Review ", objectMap(object, "code_review_rate_limit", "codeReviewRateLimit"))
	for _, raw := range objectSlice(object, "additional_rate_limits", "additionalRateLimits") {
		additional, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := firstString(additional, "limit_name", "limitName")
		if name == "" {
			name = "Additional"
		}
		appendCodexRateLimit(&result.Quota, name+" ", objectMap(additional, "rate_limit", "rateLimit"))
	}
	if credits := objectMap(object, "rate_limit_reset_credits", "rateLimitResetCredits"); credits != nil {
		if count, ok := intValue(credits, "available_count", "availableCount"); ok {
			value := int(count)
			result.RateLimitResetCreditsAvailableCount = &value
		}
	}
	return nil
}

func appendCodexRateLimit(rows *[]quotaRow, labelPrefix string, info map[string]any) {
	if info == nil {
		return
	}
	allowed, hasAllowed := boolValue(info, "allowed")
	reached, hasReached := boolValue(info, "limit_reached", "limitReached")
	windows := []struct {
		key, role string
		value     map[string]any
		seconds   int64
	}{
		{key: "primary_window", role: "Primary"},
		{key: "secondary_window", role: "Secondary"},
	}
	for index := range windows {
		windows[index].value = objectMap(info, windows[index].key, camelKey(windows[index].key))
		windows[index].seconds, _ = intValue(windows[index].value, "limit_window_seconds", "limitWindowSeconds")
	}
	sort.SliceStable(windows, func(i, j int) bool {
		return codexWindowOrder(windows[i].seconds, windows[i].role) < codexWindowOrder(windows[j].seconds, windows[j].role)
	})
	for _, window := range windows {
		value := window.value
		if value == nil {
			continue
		}
		seconds := window.seconds
		label := "5 小时限额"
		if window.role == "Secondary" {
			label = "周限额"
		}
		switch {
		case seconds == 5*60*60:
			label = "5 小时限额"
		case seconds == 7*24*60*60:
			label = "周限额"
		case seconds >= 28*24*60*60 && seconds <= 31*24*60*60:
			label = "月限额"
		}
		row := quotaRow{Label: labelPrefix + label}
		if used, ok := floatValue(value, "used_percent", "usedPercent"); ok {
			row.UsedPercent = floatPointer(used)
		} else if hasReached && reached || hasAllowed && !allowed {
			row.UsedPercent = floatPointer(100)
		}
		if reset, ok := intValue(value, "reset_after_seconds", "resetAfterSeconds"); ok {
			row.ResetAfterSeconds = intPointer(reset)
		}
		if resetAt, ok := intValue(value, "reset_at", "resetAt"); ok && resetAt > 0 {
			row.ResetAt = time.Unix(resetAt, 0).UTC().Format(time.RFC3339)
		}
		*rows = append(*rows, row)
	}
}

func codexWindowOrder(seconds int64, role string) int {
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

func (a *App) fetchClaudeQuota(callbackID, token string, result *authQuotaResponse) error {
	headers := http.Header{"Anthropic-Beta": {"oauth-2025-04-20"}, "Content-Type": {"application/json"}}
	usage, errCall := a.upstream(callbackID, http.MethodGet, "https://api.anthropic.com/api/oauth/usage", token, headers, nil)
	if errCall != nil {
		return errCall
	}
	fable := claudeFableLimit(usage)
	for _, window := range []struct{ key, label string }{
		{"five_hour", "5 小时限额"}, {"seven_day", "周限额"},
		{"seven_day_oauth_apps", "OAuth Apps 周限额"}, {"seven_day_opus", "Opus 周限额"},
		{"seven_day_sonnet", "Sonnet 周限额"}, {"seven_day_cowork", "Cowork 周限额"},
		{"iguana_necktie", "Iguana Necktie"},
	} {
		if window.key == "iguana_necktie" && fable != nil {
			continue
		}
		value := objectMap(usage, window.key, camelKey(window.key))
		if value == nil {
			continue
		}
		row := quotaRow{Label: window.label, ResetAt: firstString(value, "resets_at", "resetsAt")}
		if percent, ok := floatValue(value, "utilization"); ok {
			row.UsedPercent = floatPointer(percent)
		}
		result.Quota = append(result.Quota, row)
	}
	if fable != nil {
		percent, _ := floatValue(fable, "percent")
		result.Quota = append(result.Quota, quotaRow{Label: "Fable 周限额", UsedPercent: floatPointer(percent), ResetAt: firstString(fable, "resets_at", "resetsAt")})
	}
	if extra := objectMap(usage, "extra_usage", "extraUsage"); extra != nil {
		enabled, hasEnabled := boolValue(extra, "is_enabled", "isEnabled")
		if !hasEnabled || enabled {
			row := quotaRow{Label: "额外用量", MoneyCents: true}
			if value, ok := floatValue(extra, "used_credits", "usedCredits"); ok {
				row.Used = floatPointer(value)
			}
			if value, ok := floatValue(extra, "monthly_limit", "monthlyLimit"); ok {
				row.Limit = floatPointer(value)
			}
			if value, ok := floatValue(extra, "utilization"); ok {
				row.UsedPercent = floatPointer(value)
			}
			result.Quota = append(result.Quota, row)
		}
	}
	if profile, errProfile := a.upstream(callbackID, http.MethodGet, "https://api.anthropic.com/api/oauth/profile", token, headers, nil); errProfile == nil {
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

func (a *App) fetchKimiQuota(callbackID, token string, result *authQuotaResponse) error {
	usage, errCall := a.upstream(callbackID, http.MethodGet, "https://api.kimi.com/coding/v1/usages", token, nil, nil)
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
		label := firstNonEmptyString(firstString(limit, "name", "title"), firstString(detail, "name", "title"), windowLabel(windowSeconds), "限额")
		row := quotaRow{Label: label,
			ResetAt: firstNonEmptyString(firstString(limit, "reset_at", "resetAt", "resetTime"), firstString(detail, "reset_at", "resetAt", "resetTime"))}
		fillQuotaValues(&row, values)
		if reset, ok := resetSeconds(limit, detail); ok {
			row.ResetAfterSeconds = intPointer(reset)
		}
		result.Quota = append(result.Quota, row)
	}
	if summary := objectMap(usage, "usage"); meaningfulQuotaValues(summary) {
		row := quotaRow{Label: firstNonEmptyString(firstString(summary, "title"), "周限额"), ResetAt: firstString(summary, "reset_at", "resetAt", "resetTime")}
		fillQuotaValues(&row, summary)
		if reset, ok := resetSeconds(summary); ok {
			row.ResetAfterSeconds = intPointer(reset)
		}
		result.Quota = append(result.Quota, row)
	}
	return nil
}

func (a *App) fetchXAIQuota(callbackID, token, userID string, result *authQuotaResponse) error {
	headers := http.Header{"X-Xai-Token-Auth": {"xai-grok-cli"}, "X-Grok-Client-Version": {"0.2.93"}, "User-Agent": {"grok-pager/0.2.93 grok-shell/0.2.93"}, "Accept": {"*/*"}}
	if userID != "" {
		headers.Set("X-Userid", userID)
	}
	weekly, weeklyErr := a.upstream(callbackID, http.MethodGet, "https://cli-chat-proxy.grok.com/v1/billing?format=credits", token, headers.Clone(), nil)
	monthly, monthlyErr := a.upstream(callbackID, http.MethodGet, "https://cli-chat-proxy.grok.com/v1/billing", token, headers.Clone(), nil)
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
		row := quotaRow{Label: "周限额", UsedPercent: floatPointer(percent), ResetAt: xaiResetAt(weeklyConfig)}
		result.Quota = append(result.Quota, row)
	}
	monthlyLimit, hasLimit := moneyValue(monthlyConfig, "monthlyLimit", "monthly_limit")
	totalUsed, hasUsed := moneyValue(monthlyConfig, "used")
	onDemandCap, hasOnDemandCap := moneyValue(monthlyConfig, "onDemandCap", "on_demand_cap")
	onDemandUsed, hasOnDemandUsed := moneyValue(monthlyConfig, "onDemandUsed", "on_demand_used")
	if !hasOnDemandUsed && hasUsed && hasLimit {
		onDemandUsed = math.Max(0, totalUsed-monthlyLimit)
		hasOnDemandUsed = true
	}
	if hasLimit || hasUsed {
		row := quotaRow{Label: "月度额度", MoneyCents: true, ResetAt: firstString(monthlyConfig, "billingPeriodEnd", "billing_period_end")}
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
				remaining := math.Max(0, monthlyLimit-included)
				row.Remaining = floatPointer(remaining)
				if monthlyLimit > 0 {
					row.UsedPercent = floatPointer(included / monthlyLimit * 100)
				}
			}
		}
		result.Quota = append(result.Quota, row)
	}
	if hasOnDemandCap && onDemandCap > 0 {
		row := quotaRow{Label: "按量付费额度", MoneyCents: true, Limit: floatPointer(onDemandCap), ResetAt: firstString(monthlyConfig, "billingPeriodEnd", "billing_period_end")}
		if hasOnDemandUsed {
			row.Used = floatPointer(onDemandUsed)
			row.Remaining = floatPointer(math.Max(0, onDemandCap-onDemandUsed))
			row.UsedPercent = floatPointer(onDemandUsed / onDemandCap * 100)
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
		result.Quota = append(result.Quota, quotaRow{Label: name + " 用量", UsedPercent: floatPointer(percent), ResetAt: xaiResetAt(weeklyConfig)})
	}
	return nil
}

func (a *App) fetchAntigravityQuota(callbackID, token, projectID string, result *authQuotaResponse) error {
	headers := http.Header{"User-Agent": {"antigravity/cli/1.0.13"}}
	var quota map[string]any
	var lastErr error
	for _, endpoint := range []string{"https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary", "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary", "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"} {
		value, err := a.upstream(callbackID, http.MethodPost, endpoint, token, headers.Clone(), map[string]string{"project": projectID})
		if err != nil {
			lastErr = err
			continue
		}
		if len(objectSlice(value, "groups")) == 0 {
			if body := objectMap(value, "body"); body != nil {
				value = body
			}
		}
		quota = value
		if len(objectSlice(value, "groups")) > 0 {
			break
		}
	}
	if quota == nil {
		return lastErr
	}
	for groupIndex, raw := range objectSlice(quota, "groups") {
		group, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		groupLabel := firstNonEmptyString(firstString(group, "displayName", "display_name"), fmt.Sprintf("限额组 %d", groupIndex+1))
		groupRows := make([]quotaRow, 0, len(objectSlice(group, "buckets")))
		for _, bucketRaw := range objectSlice(group, "buckets") {
			bucket, ok := bucketRaw.(map[string]any)
			if !ok {
				continue
			}
			remaining, ok := floatValue(bucket, "remainingFraction", "remaining_fraction")
			if !ok {
				continue
			}
			windowName := strings.ToLower(firstString(bucket, "window"))
			label := firstNonEmptyString(firstString(bucket, "displayName", "display_name"), "限额")
			var windowSeconds int64
			if windowName == "5h" || windowName == "five-hour" || windowName == "five_hour" {
				label = "5 小时限额"
				windowSeconds = 18000
			} else if windowName == "weekly" || windowName == "week" {
				label = "周限额"
				windowSeconds = 604800
			}
			groupRows = append(groupRows, quotaRow{Label: label, GroupLabel: groupLabel, RemainingFraction: floatPointer(remaining), windowSeconds: windowSeconds, ResetAt: firstString(bucket, "resetTime", "reset_time")})
		}
		sort.SliceStable(groupRows, func(i, j int) bool {
			return quotaWindowOrder(groupRows[i].windowSeconds) < quotaWindowOrder(groupRows[j].windowSeconds)
		})
		result.Quota = append(result.Quota, groupRows...)
	}
	for _, endpoint := range []string{"https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist", "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"} {
		subscription, err := a.upstream(callbackID, http.MethodPost, endpoint, token, headers.Clone(), map[string]any{"metadata": map[string]string{"ideType": "ANTIGRAVITY"}})
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

func quotaWindowOrder(seconds int64) int {
	switch seconds {
	case 18000:
		return 0
	case 604800:
		return 1
	default:
		return 2
	}
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

func credentialRecords(credential map[string]any) []map[string]any {
	var records []map[string]any
	var visit func(map[string]any, int)
	visit = func(record map[string]any, depth int) {
		if record == nil || depth > 2 {
			return
		}
		records = append(records, record)
		for _, key := range []string{"metadata", "attributes", "oauth", "raw", "credential", "auth", "token", "installed", "web", "user"} {
			if nested, ok := record[key].(map[string]any); ok {
				visit(nested, depth+1)
			}
		}
	}
	visit(credential, 0)
	return records
}

func credentialString(credential map[string]any, keys ...string) string {
	for _, record := range credentialRecords(credential) {
		if value := firstString(record, keys...); value != "" {
			return value
		}
	}
	return ""
}

func credentialToken(credential map[string]any) string {
	return credentialString(credential, "access_token", "accessToken", "token", "id_token", "idToken", "cookie")
}

func paidXAICredential(credential map[string]any) bool {
	parts := strings.Split(credentialToken(credential), ".")
	if len(parts) < 2 {
		return false
	}
	payload, errDecode := base64.RawURLEncoding.DecodeString(parts[1])
	if errDecode != nil {
		return false
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return false
	}
	for key := range claims {
		normalized := strings.ToLower(key)
		if normalized == "tier" || strings.HasSuffix(normalized, "/tier") || strings.HasSuffix(normalized, ":tier") {
			if tier, ok := floatValue(claims, key); ok && tier >= 1 {
				return true
			}
		}
	}
	return false
}

func credentialUsesAPIKey(credential map[string]any) bool {
	if credentialString(credential, "api_key", "apiKey") != "" {
		return true
	}
	for _, record := range credentialRecords(credential) {
		if usingAPI, ok := boolValue(record, "using_api", "usingApi"); ok && usingAPI {
			return true
		}
	}
	return false
}

func redactSecret(value, secret string) string {
	if secret == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, "[REDACTED]")
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
func intPointer(value int64) *int64       { return &value }
func camelKey(value string) string {
	parts := strings.Split(value, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
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
	if !hasUsed && hasLimit && hasRemaining {
		used = limit - remaining
		hasUsed = true
	}
	if !hasRemaining && hasLimit && hasUsed {
		remaining = math.Max(0, limit-used)
		hasRemaining = true
	}
	if !hasUsed && hasLimit {
		used = 0
		hasUsed = true
		remaining = limit
		hasRemaining = true
	}
	if hasUsed {
		row.Used = floatPointer(used)
	}
	if hasLimit {
		row.Limit = floatPointer(limit)
	}
	if hasRemaining {
		row.Remaining = floatPointer(remaining)
	}
	if hasLimit && limit > 0 && hasUsed {
		row.UsedPercent = floatPointer(used / limit * 100)
	}
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

func resetSeconds(objects ...map[string]any) (int64, bool) {
	for _, object := range objects {
		if value, ok := floatValue(object, "reset_in", "resetIn", "ttl"); ok && value >= 0 {
			return int64(value), true
		}
	}
	return 0, false
}

func windowLabel(seconds int64) string {
	switch seconds {
	case 18000:
		return "5 小时限额"
	case 604800:
		return "周限额"
	case 2592000:
		return "月限额"
	}
	return ""
}
func moneyValue(object map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if nested := objectMap(object, key); nested != nil {
			return floatValue(nested, "val")
		}
		if value, ok := floatValue(object, key); ok {
			return value, true
		}
	}
	return 0, false
}
func xaiResetAt(config map[string]any) string {
	if period := objectMap(config, "currentPeriod", "current_period"); period != nil {
		if value := firstString(period, "end"); value != "" {
			return value
		}
	}
	return firstString(config, "billingPeriodEnd", "billing_period_end")
}
