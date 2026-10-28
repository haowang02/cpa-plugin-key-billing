package plugin

import (
	"crypto/rand"
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

	"cpa-key-billing/internal/billing"
	"cpa-key-billing/internal/messages"
)

const (
	hostAuthList             = "host.auth.list"
	hostAuthGet              = "host.auth.get"
	hostAuthGetRuntime       = "host.auth.get_runtime"
	hostAuthModels           = "host.auth.models"
	hostRoutingResetCooldown = "host.routing.reset_cooldown"
)

var authQuotaProviders = []string{"claude", "antigravity", "codex", "xai", "kimi"}

var quotaResetIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type HostCaller func(method string, payload any) (json.RawMessage, error)

type hostAuthFile struct {
	ID          string    `json:"id"`
	AuthIndex   string    `json:"auth_index"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Provider    string    `json:"provider"`
	Label       string    `json:"label"`
	Note        string    `json:"note"`
	Status      string    `json:"status"`
	Source      string    `json:"source"`
	Path        string    `json:"path"`
	Account     string    `json:"account"`
	Disabled    bool      `json:"disabled"`
	Unavailable bool      `json:"unavailable"`
	Priority    int       `json:"priority"`
	Email       string    `json:"email"`
	ProjectID   string    `json:"project_id"`
	AccountType string    `json:"account_type"`
	RuntimeOnly bool      `json:"runtime_only"`
	ModTime     time.Time `json:"modtime"`
}

type authFileView struct {
	AuthIndex          string                       `json:"auth_index"`
	Name               string                       `json:"name"`
	Title              string                       `json:"title"`
	Note               string                       `json:"note"`
	Category           string                       `json:"category"`
	Disabled           bool                         `json:"disabled"`
	Unavailable        bool                         `json:"unavailable"`
	Priority           int                          `json:"priority"`
	Weight             *int64                       `json:"weight,omitempty"`
	InFlight           int                          `json:"in_flight"`
	QuotaSupported     bool                         `json:"quota_supported"`
	QuotaReason        string                       `json:"quota_unavailable_reason,omitempty"`
	QuotaReasonMessage messages.Message             `json:"quota_unavailable_message,omitzero"`
	CacheRevision      string                       `json:"cache_revision,omitempty"`
	Usage              billing.CredentialUsageStats `json:"usage"`
	Quota              *authQuotaResponse           `json:"quota,omitempty"`
}

type authFileListResponse struct {
	Files []authFileView `json:"files"`
}

type hostAuthListResponse struct {
	Files []hostAuthFile `json:"files"`
}

type quotaRow struct {
	Label            string           `json:"label,omitempty"`
	GroupLabel       string           `json:"group_label,omitempty"`
	LabelMessage     messages.Message `json:"label_message,omitzero"`
	GroupMessage     messages.Message `json:"group_message,omitzero"`
	LabelPrefix      string           `json:"label_prefix,omitempty"`
	RemainingPercent *float64         `json:"remaining_percent,omitempty"`
	Used             *float64         `json:"used,omitempty"`
	Limit            *float64         `json:"limit,omitempty"`
	Currency         string           `json:"currency,omitempty"`
	ResetAt          string           `json:"reset_at,omitempty"`
	ObservedAt       time.Time        `json:"observed_at,omitzero"`
	// LimitKey names the limit of a window that requests report; with the
	// window's length it identifies the window from one report to the next.
	LimitKey string `json:"limit_key,omitempty"`
	// Usage is what CLIProxyAPI requests consumed in the window's current
	// period, which UsageQuery selects.
	Usage         *billing.UsageTotal `json:"usage,omitempty"`
	UsageQuery    *billing.UsageQuery `json:"usage_query,omitempty"`
	windowSeconds int64
	meter         *quotaMeter
}

type authQuotaResponse struct {
	PlanObservedAt                      time.Time           `json:"plan_observed_at,omitzero"`
	CreditBalanceObservedAt             time.Time           `json:"credit_balance_observed_at,omitzero"`
	CreditsUnlimitedObservedAt          time.Time           `json:"credits_unlimited_observed_at,omitzero"`
	AuthRevision                        string              `json:"auth_revision,omitempty"`
	FetchedAt                           time.Time           `json:"fetched_at"`
	Plan                                string              `json:"plan,omitempty"`
	CreditBalance                       string              `json:"credit_balance,omitempty"`
	CreditCurrency                      string              `json:"credit_currency,omitempty"`
	CreditsUnlimited                    bool                `json:"credits_unlimited,omitempty"`
	RateLimitResetCreditsAvailableCount *int                `json:"rate_limit_reset_credits_available_count,omitempty"`
	RateLimitResetCredits               []resetCreditExpiry `json:"rate_limit_reset_credits,omitempty"`
	RateLimitResetCreditsUnavailable    bool                `json:"rate_limit_reset_credits_unavailable,omitempty"`
	Quota                               []quotaRow          `json:"quota"`
}

type resetCreditExpiry struct {
	ExpiresAt string `json:"expires_at"`
}

func (a *App) authFiles(req ManagementRequest, access viewAccess) ManagementResponse {
	if access.APIKey && !access.Tracked {
		return apiKeyUnauthorized()
	}
	var from, to time.Time
	if err := timeRangeParams(req.Query, &from, &to); err != nil {
		return viewErrorResponse(access, err)
	}
	files, errList := a.listAuthFiles(access, from, to)
	if errList != nil {
		if billing.KindOf(errList) == billing.KindInvalid {
			return viewErrorResponse(access, errList)
		}
		return viewDetailedError(access, http.StatusBadGateway, "host_unavailable", errList)
	}
	return viewJSON(access, http.StatusOK, authFileListResponse{Files: files})
}

func (a *App) authQuota(req ManagementRequest, access viewAccess) ManagementResponse {
	if access.APIKey && !access.Tracked {
		return apiKeyUnauthorized()
	}
	selected, failure := a.resolveQuotaAuthFile(req, access)
	if selected == nil {
		return failure
	}
	result, errQuota := a.fetchAuthQuota(req.HostCallbackID, *selected)
	if errQuota != nil {
		return viewDetailedError(access, http.StatusBadGateway, "quota_failed", errQuota)
	}
	credential := quotaCredential{selected.AuthIndex, authCategory(selected.Type)}
	if errUsage := a.attachQuotaUsage(access.usageScope(), map[quotaCredential]*authQuotaResponse{credential: &result}); errUsage != nil {
		return viewErrorResponse(access, errUsage)
	}
	return viewJSON(access, http.StatusOK, result)
}

func (a *App) authFileHistory(req ManagementRequest, access viewAccess) ManagementResponse {
	if access.APIKey && !access.Tracked {
		return apiKeyUnauthorized()
	}
	selected, failure := a.resolveAuthFile(req, access)
	if selected == nil {
		return failure
	}
	history, err := a.readAuthFileHistory(quotaCredential{selected.AuthIndex, authCategory(selected.Type)}, access.usageScope())
	if err != nil {
		return viewErrorResponse(access, err)
	}
	return viewJSON(access, http.StatusOK, history)
}

// Resource routes are GET-only. Require a reset ID header and preserve it on retries.
func (a *App) authQuotaReset(req ManagementRequest, access viewAccess) ManagementResponse {
	if access.APIKey && !access.Tracked {
		return apiKeyUnauthorized()
	}
	if access.APIKey && !a.store.Config().AllowAPIKeyQuotaReset {
		return viewJSONError(access, http.StatusForbidden, "forbidden", "Quota resets are disabled for API key users")
	}
	resetID := req.Headers.Get("X-Quota-Reset-ID")
	if !quotaResetIDPattern.MatchString(resetID) {
		return viewJSONError(access, http.StatusBadRequest, "invalid", "Invalid quota reset request ID")
	}
	selected, failure := a.resolveQuotaAuthFile(req, access)
	if selected == nil {
		return failure
	}
	if category := authCategory(selected.Type); category != "codex" && category != "claude" {
		return viewJSONError(access, http.StatusUnprocessableEntity, "unsupported", "Quota resets are not supported for this auth file type")
	}
	name := selected.Name
	if access.APIKey && access.MaskEmails {
		name = maskEmails(name)
	}
	if !req.Query.Has("auth_revision") || req.Query.Get("auth_revision") != authFileRevision(*selected) || req.Query.Get("auth_name") != name {
		return viewJSONError(access, http.StatusConflict, "auth_file_changed", "Auth file changed; refresh the auth file list and try again")
	}
	cleared, errReset := a.resetAuthQuota(req.HostCallbackID, *selected, resetID)
	if errReset != nil {
		return viewDetailedError(access, http.StatusBadGateway, "reset_failed", errReset)
	}
	return viewJSON(access, http.StatusOK, map[string]bool{"reset": true, "cooldown_cleared": cleared})
}

func (a *App) resolveQuotaAuthFile(req ManagementRequest, access viewAccess) (*hostAuthFile, ManagementResponse) {
	selected, failure := a.resolveAuthFile(req, access)
	if selected == nil {
		return nil, failure
	}
	if supported, reason := authQuotaAvailability(*selected, authCategory(selected.Type)); !supported {
		code := "unsupported"
		if selected.Disabled {
			code = "disabled"
		}
		return nil, viewJSONError(access, http.StatusUnprocessableEntity, code, reason)
	}
	return selected, ManagementResponse{}
}

// resolveAuthFile finds the auth file a request names, among those the caller may see.
func (a *App) resolveAuthFile(req ManagementRequest, access viewAccess) (*hostAuthFile, ManagementResponse) {
	authIndex := strings.TrimSpace(req.Query.Get("auth_index"))
	if authIndex == "" || len(authIndex) > 512 {
		return nil, viewJSONError(access, http.StatusBadRequest, "invalid", "Invalid auth file identifier")
	}
	selected, errList := a.findAuthFile(authIndex)
	if errList != nil {
		return nil, viewDetailedError(access, http.StatusBadGateway, "host_unavailable", errList)
	}
	if selected == nil {
		return nil, viewJSONError(access, http.StatusNotFound, "not_found", "Auth file does not exist")
	}
	if access.APIKey {
		decision := a.store.ResolveRouting(access.Scope, "", "")
		if decision.RestrictsCredentials() && !routingAllowsAuthFile(*selected, decision) {
			return nil, viewJSONError(access, http.StatusNotFound, "not_found", "Auth file does not exist")
		}
	}
	return selected, ManagementResponse{}
}

func (a *App) listAuthFiles(access viewAccess, from, to time.Time) ([]authFileView, error) {
	files, errList := a.listHostAuthFiles()
	if errList != nil {
		return nil, errList
	}
	if access.APIKey {
		decision := a.store.ResolveRouting(access.Scope, "", "")
		if decision.RestrictsCredentials() {
			filtered := files[:0]
			for _, file := range files {
				if routingAllowsAuthFile(file, decision) {
					filtered = append(filtered, file)
				}
			}
			files = filtered
		}
	}
	concurrency := a.store.CredentialConcurrency(access.usageScope())
	views := make([]authFileView, 0, len(files))
	for _, file := range files {
		if strings.TrimSpace(file.AuthIndex) == "" || strings.EqualFold(strings.TrimSpace(file.AccountType), "api_key") {
			continue
		}
		category := authCategory(file.Type)
		quotaSupported, quotaReason := authQuotaAvailability(file, category)
		views = append(views, authFileView{
			AuthIndex: file.AuthIndex, Name: file.Name, Title: authFileTitle(file), Note: cleanText(file.Note), Category: category,
			Disabled: file.Disabled, Unavailable: file.Unavailable, Priority: file.Priority, Weight: a.authFileWeight(file),
			InFlight:       concurrency[billing.CredentialFingerprint(file.ID)],
			QuotaSupported: quotaSupported, QuotaReason: quotaReason, CacheRevision: authFileRevision(file),
			QuotaReasonMessage: messages.Literal(quotaReason),
		})
	}
	indexes := make([]string, len(views))
	for i := range views {
		indexes[i] = views[i].AuthIndex
	}
	usage, errUsage := a.store.CredentialUsageStats(indexes, access.usageScope(), from, to)
	if errUsage != nil {
		return nil, errUsage
	}
	// Older reports describe no window that is still current.
	reports, errReports := a.store.LatestQuotaReports(indexes, a.store.Now().Add(-quotaHistoryRange-quotaHistoryLookback))
	if errReports != nil {
		return nil, errReports
	}
	quotas := make(map[quotaCredential]*authQuotaResponse)
	for i := range views {
		views[i].Usage = usage[views[i].AuthIndex]
		views[i].Quota = observedAuthQuota(views[i].Category, views[i].CacheRevision, reports[views[i].AuthIndex])
		if views[i].Quota != nil {
			quotas[quotaCredential{views[i].AuthIndex, views[i].Category}] = views[i].Quota
		}
	}
	if errUsage := a.attachQuotaUsage(access.usageScope(), quotas); errUsage != nil {
		return nil, errUsage
	}
	sort.SliceStable(views, func(i, j int) bool {
		left, right := authCategoryOrder(views[i].Category), authCategoryOrder(views[j].Category)
		if left != right {
			return left < right
		}
		if left == len(authQuotaProviders) && views[i].Category != views[j].Category {
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
		return false, "Auth file is disabled"
	}
	if file.RuntimeOnly {
		return false, "Runtime-only auth files have no readable credentials"
	}
	if !slices.Contains(authQuotaProviders, category) {
		return false, "Quota queries are not supported for this auth file type"
	}
	return true, ""
}

func authFileRevision(file hostAuthFile) string {
	if file.ModTime.IsZero() {
		return ""
	}
	return file.ModTime.UTC().Format(time.RFC3339Nano)
}

func (a *App) listHostAuthFiles() ([]hostAuthFile, error) {
	if a.hostCaller == nil {
		return nil, messages.Errorf("This CLIProxyAPI version does not support reading auth files")
	}
	raw, errCall := a.hostCaller(hostAuthList, map[string]any{})
	if errCall != nil {
		return nil, messages.Errorf("Read auth file list: %w", errCall)
	}
	var response hostAuthListResponse
	if errDecode := json.Unmarshal(raw, &response); errDecode != nil {
		return nil, messages.Errorf("Parse auth file list: %w", errDecode)
	}
	return response.Files, nil
}

func (a *App) readHostAuthFile(authIndex string) (map[string]any, error) {
	raw, err := a.hostCaller(hostAuthGet, map[string]string{"auth_index": authIndex})
	if err != nil {
		return nil, messages.Errorf("Read auth file: %w", err)
	}
	var response struct {
		JSON json.RawMessage `json:"json"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, messages.Errorf("Parse auth file: %w", err)
	}
	var metadata map[string]any
	if json.Unmarshal(response.JSON, &metadata) != nil {
		return nil, messages.Errorf("Invalid auth file contents")
	}
	return metadata, nil
}

func (a *App) authFileWeight(file hostAuthFile) *int64 {
	if file.RuntimeOnly || strings.TrimSpace(file.Path) == "" {
		return nil
	}
	// The list callback omits weight; a failed file read must remain unknown.
	metadata, err := a.readHostAuthFile(file.AuthIndex)
	if err != nil {
		return nil
	}
	weight := int64(1)
	switch value := metadata["weight"].(type) {
	case float64:
		weight = int64(value)
	case string:
		if value = strings.TrimSpace(value); value != "" {
			weight, _ = strconv.ParseInt(value, 10, 64)
		}
	}
	weight = max(0, weight)
	return &weight
}

// Auth files are titled the way CPAMC titles them: email, then project ID, then
// the file name. An email is never parsed out of the file name.
func authFileTitle(file hostAuthFile) string {
	for _, title := range []string{file.Email, file.ProjectID} {
		if title = cleanText(title); title != "" {
			return title
		}
	}
	name := cleanText(file.Name)
	if base := len(name) - len(".json"); base > 0 && strings.EqualFold(name[base:], ".json") {
		return name[:base]
	}
	return name
}

func authCategory(authType string) string {
	return strings.ToLower(strings.TrimSpace(authType))
}

func authCategoryOrder(category string) int {
	if index := slices.Index(authQuotaProviders, category); index >= 0 {
		return index
	}
	return len(authQuotaProviders)
}

func (a *App) findAuthFile(authIndex string) (*hostAuthFile, error) {
	files, err := a.listHostAuthFiles()
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if file.AuthIndex == authIndex && !strings.EqualFold(strings.TrimSpace(file.AccountType), "api_key") {
			return &file, nil
		}
	}
	return nil, nil
}

func (a *App) handleQuota(method string, raw []byte) ([]byte, error) {
	var req QuotaRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	file, err := a.findAuthFile(strings.TrimSpace(req.AuthIndex))
	if err != nil {
		return nil, err
	}
	if file == nil || file.AuthIndex == "" {
		return nil, messages.Errorf("Auth file does not exist")
	}
	if req.AuthID != "" && req.AuthID != file.ID {
		return nil, fmt.Errorf("Credential changed; refresh the credential list")
	}
	if req.Provider != "" && !strings.EqualFold(req.Provider, file.Type) {
		return nil, fmt.Errorf("Credential provider does not match")
	}
	if supported, reason := authQuotaAvailability(*file, authCategory(file.Type)); !supported {
		return nil, fmt.Errorf("%s", reason)
	}
	if method == MethodQuotaFetch {
		quota, err := a.fetchAuthQuota(req.HostCallbackID, *file)
		if err != nil {
			return nil, err
		}
		return OKEnvelope(quota.normalized())
	}
	cleared, err := a.resetAuthQuota(req.HostCallbackID, *file, newQuotaResetID())
	if err != nil {
		return nil, err
	}
	response := QuotaResetResponse{Success: true}
	if !cleared {
		response.Message = "Upstream quota reset succeeded; local routing cooldown could not be cleared"
	}
	return OKEnvelope(response)
}

func (input authQuotaResponse) normalized() QuotaResponse {
	result := QuotaResponse{}
	if input.Plan != "" {
		result.Subscription = &QuotaSubscription{Plan: input.Plan}
	}
	if input.CreditsUnlimited {
		result.Groups = append(result.Groups, QuotaGroup{DisplayName: "Credits", Buckets: []QuotaBucket{{Window: "Unlimited credits", RemainingFraction: 1}}})
	} else if value, err := strconv.ParseFloat(input.CreditBalance, 64); err == nil && value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
		metric := QuotaMetric{Key: "credit_balance", Label: "Credit balance", Value: value, Format: "number"}
		if input.CreditCurrency != "" {
			metric.Format, metric.Currency = "currency", input.CreditCurrency
		}
		result.Summary = append(result.Summary, metric)
	}
	if input.RateLimitResetCreditsAvailableCount != nil {
		result.Summary = append(result.Summary, QuotaMetric{Key: "reset_credits", Label: "Available quota resets", Value: float64(*input.RateLimitResetCreditsAvailableCount), Format: "number"})
	}
	positions := map[string]int{}
	for _, row := range input.Quota {
		percent := row.RemainingPercent
		if percent == nil && row.Used != nil && row.Limit != nil && *row.Limit > 0 {
			value := 100 * (1 - *row.Used / *row.Limit)
			percent = &value
		}
		// The host requires a number for each bucket. Unknown remaining quota must
		// be omitted instead of being presented as exhausted or unlimited.
		if percent == nil || math.IsNaN(*percent) || math.IsInf(*percent, 0) {
			continue
		}
		name := firstNonEmptyString(row.GroupLabel, row.GroupMessage.Text, "Quota")
		index, exists := positions[name]
		if !exists {
			index = len(result.Groups)
			positions[name] = index
			result.Groups = append(result.Groups, QuotaGroup{DisplayName: name})
		}
		result.Groups[index].Buckets = append(result.Groups[index].Buckets, QuotaBucket{
			Window:            row.LabelPrefix + firstNonEmptyString(row.Label, row.LabelMessage.Text, "Quota"),
			RemainingFraction: math.Max(0, math.Min(1, *percent/100)), ResetTime: row.ResetAt,
		})
	}
	return result
}

func newQuotaResetID() string {
	var bytes [16]byte
	_, _ = rand.Read(bytes[:])
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:])
}
