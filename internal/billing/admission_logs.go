package billing

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

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
	s.read(func(state *State) { name = state.describeKey(scope) })

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
	s.AddPluginLog(PluginLogInfo, "%s", message.String())
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

func (b *blockedKeys) forget(scopes ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, scope := range scopes {
		delete(b.cycles, scope)
	}
}

// describeKey names a key the way the panel does: the operator's remark beside
// the masked preview, either one alone when that is all there is, and the head
// of the scope for a key no synchronization has ever named.
func (s *State) describeKey(scope string) string {
	if description := keyDescription(s.Keys[scope]); description != "" {
		return description
	}
	if len(scope) > 12 {
		return scope[:12] + "…"
	}
	return scope
}

func keyDescription(key *KeyState) string {
	if key == nil {
		return ""
	}
	label, preview := strings.TrimSpace(key.Label), strings.TrimSpace(key.Preview)
	switch {
	case label != "" && preview != "":
		return label + " · " + preview
	case label != "":
		return label
	default:
		return preview
	}
}

func planName(decision Decision) string {
	if name := strings.TrimSpace(decision.PlanName); name != "" {
		return name
	}
	return strings.TrimSpace(decision.PlanID)
}
