package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// callerScopeSalt must stay byte-identical to CLIProxyAPI's
// sdk/cliproxy/session.CallerScope. The host derives caller_scope with this
// exact prefix and places it in interceptor metadata; this plugin derives the
// same value when the admin UI sends the configured key list. Changing it
// silently detaches those synchronized keys from request accounting, so a
// fixed host-generated vector covers it.
const callerScopeSalt = "cli-proxy-api:caller-scope:v1\x00"

// Blank principals are not attributable and therefore have no scope.
func CallerScope(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(callerScopeSalt + value))
	return hex.EncodeToString(sum[:])
}

// PreviewKey masks a plaintext key for display and persistence.
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
