package plugin

import (
	"encoding/json"
	"net/http"
	"strings"

	"cpa-key-billing/internal/billing"
)

const maxPendingRouteLogs = 4096

type pendingRouteLog struct {
	Sequence             uint64
	Key                  string
	Model                string
	ModelRestricted      bool
	ModelDenied          bool
	CredentialRestricted bool
	SelectedCredentialID string
	CredentialResult     string
	CredentialError      string
}

func boundedLogValue(value string, limit int) string {
	value = secretLikeToken.ReplaceAllStringFunc(value, billing.PreviewKey)
	value = emailLikeToken.ReplaceAllStringFunc(value, billing.PreviewKey)
	value = cleanText(value)
	if len([]byte(value)) > limit {
		return string([]byte(value)[:limit])
	}
	return value
}

func credentialLogName(credential credentialView) string {
	if credential.Source == billing.CredentialSourceAuthFiles {
		value := cleanText(credential.DisplayName)
		if len([]byte(value)) > 512 {
			return string([]byte(value)[:512])
		}
		return credential.ProviderName + " · " + value
	}
	return boundedLogValue(credential.ProviderName+" · "+credential.DisplayName, 512)
}

func (a *App) beginRouteLog(requestID, scope string, decision billing.RoutingDecision) {
	modelRestricted := decision.RestrictsModels()
	credentialRestricted := decision.RestrictsCredentials()
	if !modelRestricted && !credentialRestricted {
		return
	}
	keyDescription := boundedLogValue(a.store.KeyDescription(scope), 256)
	a.routingMu.Lock()
	defer a.routingMu.Unlock()
	if _, exists := a.pending[requestID]; exists {
		return
	}
	if len(a.pending) >= maxPendingRouteLogs {
		var oldestID string
		var oldest uint64
		for id, item := range a.pending {
			if oldestID == "" || item.Sequence < oldest {
				oldestID = id
				oldest = item.Sequence
			}
		}
		delete(a.pending, oldestID)
	}
	a.pendingSequence++
	a.pending[requestID] = pendingRouteLog{
		Sequence: a.pendingSequence, Key: keyDescription,
		Model: boundedLogValue(decision.Model, 512), ModelRestricted: modelRestricted,
		ModelDenied:          modelRestricted && !decision.AllowsModel(),
		CredentialRestricted: credentialRestricted,
	}
}

func (a *App) observeRouteCredential(req RequestInterceptRequest, result, detail string) {
	credentialID := metadataString(req.Metadata, MetadataSelectedAuth)
	credentialIndex := metadataString(req.Metadata, MetadataSelectedIndex)
	a.routingMu.Lock()
	defer a.routingMu.Unlock()
	if credentialID != "" && credentialIndex != "" {
		ref := billing.CredentialFingerprint(credentialID)
		if _, known := a.credentials[ref]; known {
			a.credentialRefsByIndex[credentialIndex] = ref
		}
	}
	pending, ok := a.pending[req.RequestID]
	if !ok {
		return
	}
	pending.SelectedCredentialID = credentialID
	pending.CredentialResult = result
	pending.CredentialError = boundedLogValue(detail, 512)
	pending.CredentialRestricted = result != "unrestricted"
	a.pending[req.RequestID] = pending
}

func (a *App) finishRouteLog(completion RequestCompletion) {
	a.routingMu.Lock()
	pending, ok := a.pending[completion.RequestID]
	delete(a.pending, completion.RequestID)
	a.routingMu.Unlock()
	if !ok {
		return
	}
	row := map[string]any{"key": pending.Key, "model": pending.Model, "model_policy": "unrestricted", "model_result": "allow", "credential_policy": "unrestricted", "credential_result": "unrestricted", "outcome": boundedLogValue(completion.Outcome, 64)}
	if pending.ModelRestricted {
		row["model_policy"] = "restricted"
	}
	if pending.ModelDenied {
		row["model_result"] = "deny"
	}
	if pending.CredentialRestricted {
		row["credential_policy"] = "restricted"
		row["credential_result"] = "not_observed"
	}
	if pending.ModelDenied {
		row["credential_result"] = "not_reached"
	} else if pending.CredentialRestricted {
		if pending.CredentialResult != "" {
			row["credential_result"] = pending.CredentialResult
		} else if completion.StatusCode == http.StatusServiceUnavailable {
			for _, shortfall := range routingShortfalls {
				if strings.Contains(completion.Error, shortfall.message) {
					row["credential_result"] = shortfall.result
					break
				}
			}
		}
	}
	if pending.CredentialRestricted && pending.SelectedCredentialID != "" {
		if credential, found := a.credentialByRawID(pending.SelectedCredentialID); found {
			row["selected_credential"] = credentialLogName(credential)
		} else {
			ref := billing.CredentialFingerprint(pending.SelectedCredentialID)
			row["selected_credential"] = "Unknown upstream credential · " + shortCredentialRef(ref)
		}
	}
	if pending.CredentialError != "" {
		row["credential_error"] = pending.CredentialError
	}
	if completion.StatusCode > 0 {
		row["status"] = completion.StatusCode
	}
	raw, _ := json.Marshal(row)
	level := billing.PluginLogDebug
	switch row["credential_result"] {
	case "deny", "unverified", routeNoCredential.result:
		level = billing.PluginLogError
	case routeUnavailable.result, routeNoMatch.result:
		level = billing.PluginLogInfo
	}
	a.store.AddPluginLog(level, "route %s", raw)
}
