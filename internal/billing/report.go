package billing

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// What the plugin log says about downstream traffic. The billing log covers
// only requests that produced something to bill, so these entries are the sole
// record of the ones that never became a charge.

// Every failure is reported, the billable ones included, so that reading the
// log never means wondering which ones it left out.
func (s *Store) reportFailedRequest(requestID string, entry PendingRequest, record *UsageRecord, reason string) {
	var name, credential string
	s.Read(func(state *State) {
		name = state.describeKey(entry.Scope)
		credential = state.Credentials[entry.AuthIndex].Name()
	})

	var message strings.Builder
	message.WriteString("请求失败：")
	message.WriteString(name)
	if endpoint := strings.TrimSpace(entry.Endpoint); endpoint != "" {
		message.WriteString(" → ")
		message.WriteString(endpoint)
	}
	if model := requestModel(record); model != "" {
		message.WriteString("，模型 ")
		message.WriteString(model)
	}
	if credential != "" {
		message.WriteString("，凭据 ")
		message.WriteString(credential)
	}
	if id := strings.TrimSpace(requestID); id != "" {
		message.WriteString("，请求 ")
		message.WriteString(id)
	}
	message.WriteString("。")
	if reason = strings.TrimSpace(reason); reason != "" {
		message.WriteString("原因：")
		message.WriteString(reason)
	} else {
		// The response hooks never saw an error body: the upstream failed before
		// answering, or failed in a way the proxy reported on its own.
		message.WriteString("插件未观察到错误内容。")
	}
	s.Event(EventError, "%s", message.String())
}

// ReportQuotaBlock records that a key was turned away for an exhausted
// subscription. It is the only trace such a request leaves: enforcement runs
// before the request reaches an upstream, so nothing is billed and nothing is
// logged for it otherwise.
func (s *Store) ReportQuotaBlock(scope, endpoint string, decision Decision) {
	if decision.Allowed {
		return
	}
	scope = strings.TrimSpace(scope)
	if scope == "" || !s.blocked.onset(scope, decision.CycleStartAt) {
		return
	}
	name := ""
	s.Read(func(state *State) { name = state.describeKey(scope) })

	var message strings.Builder
	message.WriteString("额度拦截：")
	message.WriteString(name)
	if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
		message.WriteString(" → ")
		message.WriteString(endpoint)
	}
	fmt.Fprintf(&message, "，本期已用 $%.4f / $%.4f", decision.SpentUSD, decision.LimitUSD)
	if plan := planName(decision); plan != "" {
		message.WriteString("，计划 ")
		message.WriteString(plan)
	}
	message.WriteString("。")
	if !decision.ResetAt.IsZero() {
		message.WriteString("额度将于 ")
		message.WriteString(decision.ResetAt.UTC().Format(time.RFC3339))
		message.WriteString(" 重置。")
	}
	// Enforcement working as configured is not a fault of the plugin's, so this
	// stays out of the level an operator reads to find one.
	s.Event(EventInfo, "%s", message.String())
}

// ReportModelBlock records that a key asked for a model it may not call. Like a
// quota block this is the request's only trace: it never reached an upstream and
// produced nothing to bill.
func (s *Store) ReportModelBlock(scope, endpoint string, decision ModelDecision) {
	if decision.Allowed {
		return
	}
	scope = strings.TrimSpace(scope)
	if scope == "" || !s.denied.onset(scope, decision.Model) {
		return
	}
	name := ""
	s.Read(func(state *State) { name = state.describeKey(scope) })

	var message strings.Builder
	message.WriteString("模型拦截：")
	message.WriteString(name)
	if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
		message.WriteString(" → ")
		message.WriteString(endpoint)
	}
	message.WriteString("，请求模型 ")
	message.WriteString(decision.Model)
	message.WriteString("。")
	message.WriteString(decision.Describe())
	message.WriteString("。")
	// Enforcement working as configured is not a fault of the plugin's, so this
	// stays out of the level an operator reads to find one.
	s.Event(EventInfo, "%s", message.String())
}

// blockedKeys remembers which subscription window a key was last reported
// blocked in, so an exhausted key names itself once rather than once per
// request the client behind it retries. A window that rolls, and an operator
// who resets or rebinds one, produce a different instant and therefore a fresh
// report. Only a key with a plan is ever blocked, so this holds at most one
// entry per tracked key.
type blockedKeys struct {
	mu     sync.Mutex
	cycles map[string]time.Time
}

func (b *blockedKeys) onset(scope string, cycleStart time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cycles == nil {
		b.cycles = make(map[string]time.Time)
	}
	if reported, exists := b.cycles[scope]; exists && reported.Equal(cycleStart) {
		return false
	}
	b.cycles[scope] = cycleStart
	return true
}

// deniedModels remembers which model a key was last turned away for, so a
// client looping on one it may not call names itself once rather than once per
// retry. A different model reports again, and so does the same one after an
// operator changed what the key may reach — the entry is dropped when the grant
// behind it does. Unlike an exhausted quota this needs no expiry of its own: the
// grant only changes when someone changes it.
type deniedModels struct {
	mu     sync.Mutex
	models map[string]string
}

func (d *deniedModels) onset(scope, model string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.models == nil {
		d.models = make(map[string]string)
	}
	if reported, exists := d.models[scope]; exists && reported == model {
		return false
	}
	d.models[scope] = model
	return true
}

func (d *deniedModels) forget(scopes ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, scope := range scopes {
		delete(d.models, scope)
	}
}

// describeKey names a key the way the panel does: the operator's remark beside
// the masked preview, either one alone when that is all there is, and the head
// of the scope for a key no synchronization has ever named.
func (s *State) describeKey(scope string) string {
	preview := ""
	if key := s.Keys[scope]; key != nil {
		label := strings.TrimSpace(key.Label)
		preview = strings.TrimSpace(key.Preview)
		switch {
		case label != "" && preview != "":
			return label + " · " + preview
		case label != "":
			return label
		}
	}
	if preview != "" {
		return preview
	}
	if len(scope) > 12 {
		return scope[:12] + "…"
	}
	return scope
}

func requestModel(record *UsageRecord) string {
	if record == nil {
		return ""
	}
	if model := strings.TrimSpace(record.BillingModel); model != "" {
		return model
	}
	return modelWithoutSuffix(record.UpstreamModel)
}

func planName(decision Decision) string {
	if name := strings.TrimSpace(decision.PlanName); name != "" {
		return name
	}
	return strings.TrimSpace(decision.PlanID)
}
