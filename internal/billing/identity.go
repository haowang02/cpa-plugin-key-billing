package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// callerScopeSalt must stay byte-identical to CLIProxyAPI's
// sdk/cliproxy/session.CallerScope. The host derives caller_scope with this
// exact prefix and places it in interceptor metadata; this plugin derives the
// same value from the plaintext key it sees in usage records and in the key
// list pushed by the admin UI. Changing it silently detaches billing from
// enforcement, so it is covered by a unit test with a fixed vector.
const callerScopeSalt = "cli-proxy-api:caller-scope:v1\x00"

// CallerScope returns the host-compatible caller scope for a downstream API key
// (more precisely, for an access provider principal). An empty or blank value
// yields an empty scope, which callers treat as "not attributable".
func CallerScope(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(callerScopeSalt + value))
	return hex.EncodeToString(sum[:])
}

// PreviewKey masks a plaintext key for display. The plaintext itself is never
// persisted; only this preview and the scope hash are.
func PreviewKey(key string) string {
	key = strings.TrimSpace(key)
	switch {
	case key == "":
		return ""
	case len(key) <= 12:
		// Too short to mask meaningfully without leaking most of it.
		return strings.Repeat("*", len(key))
	default:
		return key[:6] + "…" + key[len(key)-4:]
	}
}
