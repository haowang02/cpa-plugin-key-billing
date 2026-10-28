package plugin

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"cpa-key-billing/internal/billing"
)

func TestAIProviderViewsScopeUsageAndRouting(t *testing.T) {
	app := newAppWithPrice(t, true)
	from := app.store.Now().Add(-time.Hour).Truncate(time.Second)
	to := from.Add(2 * time.Minute)
	refA, refB := billing.CredentialFingerprint("config-a"), billing.CredentialFingerprint("config-b")
	for _, key := range []string{accountTestKeyA, accountTestKeyB} {
		app.store.AcquireSlot(billing.CallerScope(key), key)
		app.store.AssignSlotCredential(key, refA)
	}
	if _, err := app.store.SyncKeys([]string{accountTestKeyA, accountTestKeyB}, false); err != nil {
		t.Fatal(err)
	}
	callOK(t, app, http.MethodPost, routeCredentialsSync, nil, map[string]any{"credentials": []map[string]any{
		{"ref": refA, "provider": "codex", "auth_index": "config-a", "type": "codex", "key_preview": "sk-dum…1234", "weight": 2},
		{"ref": refB, "provider": "claude", "auth_index": "config-b", "type": "claude", "weight": 1},
	}}, http.StatusOK, nil)
	for _, sample := range []struct {
		index, key string
		at         time.Time
		failed     bool
	}{
		{"config-a", accountTestKeyA, from, false},
		{"config-a", accountTestKeyA, from.Add(time.Minute), true},
		{"config-a", accountTestKeyB, from, false},
		{"config-a", accountTestKeyA, from.Add(-time.Minute), false},
		{"config-a", accountTestKeyA, to, false},
		{"config-b", accountTestKeyA, from, false},
	} {
		record := UsageRecord{Provider: "codex", AuthType: "apikey", AuthIndex: sample.index, APIKey: sample.key, Model: "gpt-5.5", RequestedAt: sample.at, Failed: sample.failed, Detail: UsageDetail{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 50}}
		if sample.failed {
			record.Detail = UsageDetail{}
		}
		publishUsageRecord(t, app, record)
	}
	app.SetHostCaller(func(string, any) (json.RawMessage, error) {
		t.Fatal("provider views must not query the host")
		return nil, nil
	})
	query := url.Values{"from": {from.Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}}
	var admin aiProviderListResponse
	callOK(t, app, http.MethodGet, routeAIProviders, query, nil, http.StatusOK, &admin)
	if len(admin.Providers) != 2 || admin.Providers[1].Usage.Requests != 3 || admin.Providers[0].InFlight != 0 || admin.Providers[1].InFlight != 2 {
		t.Fatalf("admin providers = %+v", admin)
	}
	for _, rule := range []billing.RouteRule{
		{CredentialIDs: []string{refA}},
		{DeniedCredentialProviders: []billing.CredentialProviderSelector{{Source: billing.CredentialSourceAIProviders, Provider: "claude"}}},
	} {
		if err := app.store.SetKeyRoutes(billing.CallerScope(accountTestKeyA), billing.RouteBindings{RouteRule: rule}); err != nil {
			t.Fatal(err)
		}
		response := callAccount(t, app, routeAIProviders, accountTestKeyA, query)
		var account aiProviderListResponse
		if err := json.Unmarshal(response.Body, &account); err != nil || response.StatusCode != http.StatusOK || len(account.Providers) != 1 {
			t.Fatalf("account providers: %s, %v", response.Body, err)
		}
		got := account.Providers[0]
		if got.InFlight != 1 {
			t.Fatalf("account in-flight requests = %d, want 1", got.InFlight)
		}
		if got.Ref != refA || got.Usage.Requests != 2 || got.Usage.Failed != 1 || got.Usage.InputTokens != 100 || got.Usage.OutputTokens != 20 || got.Usage.CacheReadTokens != 50 {
			t.Fatalf("account usage = %+v", got.Usage)
		}
		query.Set("credential_ref", refA)
		response = callAccount(t, app, routeUsageModels, accountTestKeyA, query)
		var models struct {
			Models []billing.ModelUsage `json:"models"`
		}
		if err := json.Unmarshal(response.Body, &models); err != nil || response.StatusCode != http.StatusOK || len(models.Models) != 1 || models.Models[0].Requests != 2 {
			t.Fatalf("account model usage = %s, %v", response.Body, err)
		}
		query.Set("credential_ref", refB)
		if response = callAccount(t, app, routeUsageModels, accountTestKeyA, query); response.StatusCode != http.StatusNotFound {
			t.Fatalf("denied model usage = %s", response.Body)
		}
		query.Del("credential_ref")
	}
	if response := callAccount(t, app, routeAIProviders, "sk-untracked", query); response.StatusCode != http.StatusUnauthorized {
		t.Fatal(response.StatusCode)
	}
	if response := callAccount(t, app, routeAIProviderBalance, accountTestKeyA, nil); response.StatusCode != http.StatusNotFound {
		t.Fatal(response.StatusCode)
	}
	callOK(t, app, http.MethodPost, routeCredentialsSync, nil, map[string]any{"credentials": []any{}}, http.StatusOK, nil)
	if response := callManagement(t, app, http.MethodPost, routeAIProviderBalance, nil, map[string]any{"auth_index": "config-a", "balance": billing.CredentialBalance{FetchedAt: from, Balances: []billing.CurrencyBalance{{Currency: "USD", Amount: 1}}}}); response.StatusCode != http.StatusNotFound {
		t.Fatal("balance update recreated a removed credential")
	}
}

func TestConfigCredentialSyncSurvivesRestartAndRollsBack(t *testing.T) {
	app, path := newAppWithPriceAndState(t, true)
	configuration := mustMarshal(t, LifecycleRequest{ConfigYAML: []byte(fmt.Sprintf("state_file: %q\n", path))})
	const rawKey = "sk-dummy-upstream-secret-1234"
	ref := billing.CredentialFingerprint("dummy-config")
	configRef := billing.CredentialFingerprint("dummy-config-identity")
	removed := billing.CredentialFingerprint("dummy-removed")
	if _, err := app.store.SyncKeys([]string{accountTestKeyA}, false); err != nil {
		t.Fatal(err)
	}
	if err := app.store.SetKeyRoutes(billing.CallerScope(accountTestKeyA), billing.RouteBindings{RouteRule: billing.RouteRule{CredentialIDs: []string{ref}}}); err != nil {
		t.Fatal(err)
	}
	response := callManagement(t, app, http.MethodPost, routeCredentialsSync, nil, map[string]any{"credentials": []map[string]any{
		{"ref": ref, "config_ref": configRef, "provider": "codex", "key_preview": rawKey, "auth_index": "dummy-index", "type": "codex", "name": "Codex Relay", "base_url": "https://example.com/v1", "priority": 2, "weight": 3},
		{"ref": removed, "provider": "codex", "key_preview": "", "auth_index": "dummy-index", "disabled": true},
	}})
	if response.StatusCode != http.StatusOK || strings.Contains(string(response.Body), rawKey) {
		t.Fatalf("sync response = %+v", response)
	}
	balance := billing.CredentialBalance{FetchedAt: app.store.Now().UTC(), Balances: []billing.CurrencyBalance{{Currency: "USD", Amount: 12.5, Parts: []billing.BalancePart{{Label: "ui.balance_used_value", Amounts: []float64{2.5, 15}}}}}}
	callOK(t, app, http.MethodPost, routeAIProviderBalance, nil, map[string]any{"auth_index": "dummy-index", "balance": balance}, http.StatusOK, nil)
	callOK(t, app, http.MethodPost, routeAIProviderNote, nil, map[string]any{"ref": ref, "note": "  Team provider  "}, http.StatusOK, nil)
	app.observeCandidates([]SchedulerAuthCandidate{{ID: "dummy-runtime", Provider: "codex", Attributes: map[string]string{"source_backend": "config"}}})
	restart := func() {
		app.Shutdown()
		app = newTestApp(t)
		t.Cleanup(app.Shutdown)
		if err := app.configure(configuration); err != nil {
			t.Fatal(err)
		}
		app.SetHostCaller(func(method string, _ any) (json.RawMessage, error) {
			if method != hostAuthList && method != hostAuthModels {
				t.Fatalf("host method = %q", method)
			}
			return json.RawMessage(`{"files":[{"id":"dummy-auth-file","provider":"codex","source":"file","email":"live@example.com"}]}`), nil
		})
	}
	restart()
	response = callAccount(t, app, routeRouting, accountTestKeyA, nil)
	var routing accountRoutingResponse
	if err := json.Unmarshal(response.Body, &routing); err != nil || response.StatusCode != http.StatusOK ||
		len(routing.Credentials) != 1 || routing.Credentials[0].ProviderName != "Codex Relay" || routing.Credentials[0].Name != billing.PreviewKey(rawKey) ||
		routing.Credentials[0].Status != "active" || len(routing.Warnings) != 0 {
		t.Fatalf("routing after restart = %+v, response = %+v, err = %v", routing, response, err)
	}
	if snapshot := app.store.ConfigCredentials(); len(snapshot) != 2 || snapshot[removed].KeyPreview != "" || !snapshot[removed].Disabled || !reflect.DeepEqual(snapshot[removed].Balance, &balance) {
		t.Fatalf("persisted config snapshot = %+v", snapshot)
	}
	if saved := app.store.ConfigCredentials()[ref]; saved.BaseURL != "https://example.com/v1" || saved.Priority != 2 || saved.Weight != 3 || saved.Note != "Team provider" || saved.ConfigRef != configRef || saved.AuthIndex != "dummy-index" || !reflect.DeepEqual(saved.Balance, &balance) {
		t.Fatalf("provider snapshot after restart = %+v", saved)
	}
	if inventory := app.credentialInventory(); len(inventory) != 3 {
		t.Fatalf("host files and config snapshot were not merged: %+v", inventory)
	}
	app.observeCandidates([]SchedulerAuthCandidate{{ID: "dummy-config", Provider: "codex", Status: "cooldown", Attributes: map[string]string{"source_backend": "config"}}})
	before := app.credentialInventory()
	if err := app.configure(configuration); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(app.credentialInventory(), before) {
		t.Fatal("reconfiguration replaced live credential state with the snapshot")
	}

	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec("CREATE TRIGGER reject_config BEFORE INSERT ON config_credentials BEGIN SELECT RAISE(ABORT, 'dummy failure'); END"); err != nil {
		t.Fatal(err)
	}
	update := map[string]any{"credentials": []map[string]any{
		{"ref": ref, "provider": "codex", "key_preview": "sk-new…5678", "auth_index": "dummy-index", "disabled": true},
	}}
	if response := callManagement(t, app, http.MethodPost, routeCredentialsSync, nil, update); response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("failed sync response = %+v", response)
	}
	if noteResponse := callManagement(t, app, http.MethodPost, routeAIProviderNote, nil, map[string]any{"ref": ref, "note": "Rejected"}); noteResponse.StatusCode != http.StatusInternalServerError || app.store.ConfigCredentials()[ref].Note != "Team provider" {
		t.Fatal("failed note update changed stored state")
	}
	if !reflect.DeepEqual(app.credentialInventory(), before) || app.store.ConfigCredentials()[ref].KeyPreview != billing.PreviewKey(rawKey) {
		t.Fatal("failed sync changed the live snapshot")
	}
	var stored int
	if err := raw.QueryRow("SELECT count(*) FROM config_credentials").Scan(&stored); err != nil || stored != 2 {
		t.Fatal("failed replacement lost persisted credentials", stored, err)
	}
	var preview string
	if err := raw.QueryRow("SELECT key_preview FROM config_credentials WHERE ref = ?", ref).Scan(&preview); err != nil || preview != billing.PreviewKey(rawKey) {
		t.Fatal("failed replacement changed the persisted preview", preview, err)
	}
	if _, err := raw.Exec("DROP TRIGGER reject_config"); err != nil {
		t.Fatal(err)
	}
	callOK(t, app, http.MethodPost, routeCredentialsSync, nil, update, http.StatusOK, nil)
	if !reflect.DeepEqual(app.store.ConfigCredentials()[ref].Balance, &balance) {
		t.Fatal("configuration sync erased the balance")
	}

	restart()
	if inventory := app.credentialInventory(); len(inventory) != 1 || inventory[0].Ref != ref ||
		inventory[0].DisplayName != "sk-new…5678" || !inventory[0].Disabled || inventory[0].Status != "disabled" {
		t.Fatalf("replacement after restart = %+v", inventory)
	}
	if err := app.configure(mustMarshal(t, LifecycleRequest{ConfigYAML: testConfigYAML(t, true)})); err != nil {
		t.Fatal(err)
	}
	if len(app.credentialInventory()) != 0 || len(app.store.ConfigCredentials()) != 0 {
		t.Fatal("old config credentials survived a database switch")
	}
	if err := app.configure(configuration); err != nil {
		t.Fatal(err)
	}
	if inventory := app.credentialInventory(); len(inventory) != 1 || inventory[0].Ref != ref || !inventory[0].Disabled {
		t.Fatalf("config snapshot was not restored after switching back: %+v", inventory)
	}

	changedRef := billing.CredentialFingerprint("disabled-config")
	callOK(t, app, http.MethodPost, routeCredentialsSync, nil, map[string]any{"credentials": []map[string]any{
		{"ref": changedRef, "config_ref": configRef, "provider": "codex", "disabled": true},
	}}, http.StatusOK, nil)
	if app.store.ConfigCredentials()[changedRef].Note != "Team provider" {
		t.Fatal("credential identity change erased its note")
	}
	restart()
	if app.store.ConfigCredentials()[changedRef].Note != "Team provider" {
		t.Fatal("provider note did not survive restart")
	}
	callOK(t, app, http.MethodPost, routeAIProviderNote, nil, map[string]any{"ref": changedRef, "note": " "}, http.StatusOK, nil)
	restart()
	if app.store.ConfigCredentials()[changedRef].Note != "" {
		t.Fatal("empty note did not clear persisted note")
	}
	if err := app.refreshCredentialInventory(); err != nil {
		t.Fatal(err)
	}
	callOK(t, app, http.MethodPost, routeCredentialsSync, nil, map[string]any{"credentials": []any{}}, http.StatusOK, nil)
	if inventory := app.credentialInventory(); len(inventory) != 1 || inventory[0].Source != billing.CredentialSourceAuthFiles {
		t.Fatalf("empty sync did not remove only config credentials: %+v", inventory)
	}
	restart()
	if inventory := app.credentialInventory(); len(inventory) != 0 || len(app.store.ConfigCredentials()) != 0 {
		t.Fatalf("cleared credentials reappeared after restart: %+v", inventory)
	}
}
