package plugin

import (
	"encoding/json"
	"strings"

	"cpa-key-billing/internal/billing"
)

const maxPendingRouteLogs = 4096

type pendingRouteLog struct {
	Sequence             uint64
	Key                  string
	Model                string
	ModelRestricted      bool
	ModelAllowed         bool
	CredentialRestricted bool
	ConfigurationError   bool
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
		return credential.Provider + " · " + value
	}
	return boundedLogValue(sourceLabel(credential.Source)+" · "+credential.Provider+" · "+credential.DisplayName, 512)
}

func (a *App) beginRouteLog(requestID, scope string, decision billing.RoutingDecision) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || (!decision.ModelRestricted && !decision.CredentialRestricted && decision.ConfigurationError == "") {
		return
	}
	keyDescription := boundedLogValue(a.store.KeyDescription(scope), 256)
	a.routingMu.Lock()
	defer a.routingMu.Unlock()
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
	a.pending[requestID] = pendingRouteLog{Sequence: a.pendingSequence, Key: keyDescription, Model: boundedLogValue(decision.Model, 512), ModelRestricted: decision.ModelRestricted, ModelAllowed: decision.ModelAllowed, CredentialRestricted: decision.CredentialRestricted, ConfigurationError: decision.ConfigurationError != ""}
}

func (a *App) finishRouteLog(completion RequestCompletion) {
	a.routingMu.Lock()
	pending, ok := a.pending[strings.TrimSpace(completion.RequestID)]
	if ok {
		delete(a.pending, strings.TrimSpace(completion.RequestID))
	}
	a.routingMu.Unlock()
	if !ok {
		return
	}
	row := map[string]any{"key": pending.Key, "model": pending.Model, "model_policy": "unrestricted", "model_result": "allow", "credential_policy": "unrestricted", "credential_result": "unrestricted", "outcome": boundedLogValue(completion.Outcome, 64)}
	if pending.ModelRestricted {
		row["model_policy"] = "restricted"
	}
	if pending.ConfigurationError {
		row["model_result"] = "configuration_error"
	} else if !pending.ModelAllowed {
		row["model_result"] = "deny"
	}
	if pending.CredentialRestricted {
		row["credential_policy"] = "restricted"
		row["credential_result"] = "not_observed"
	}
	if pending.ConfigurationError {
		row["credential_result"] = "configuration_error"
	} else if !pending.ModelAllowed {
		row["credential_result"] = "not_reached"
	} else if pending.CredentialRestricted {
		if id := metadataString(completion.Metadata, "selected_auth_id"); id != "" {
			if credential, found := a.credentialByRawID(id); found {
				row["credential_result"] = "selected"
				row["selected_credential"] = credentialLogName(credential)
			} else {
				ref := billing.CredentialFingerprint(id)
				row["credential_result"] = "selected"
				row["selected_credential"] = "未知上游凭证 · " + shortCredentialRef(ref)
			}
		}
	}
	if completion.StatusCode > 0 {
		row["status"] = completion.StatusCode
	}
	raw, err := json.Marshal(row)
	if err != nil {
		return
	}
	a.store.AddPluginLog(billing.PluginLogDebug, "route %s", raw)
}
