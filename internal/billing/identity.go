package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// callerScopeSalt must stay byte-identical to CLIProxyAPI's session.CallerScope, or synced keys stop
// matching the host's caller_scope.
const callerScopeSalt = "cli-proxy-api:caller-scope:v1\x00"

const UnknownKeyPreview = "unknown"

func CallerScope(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(callerScopeSalt + value))
	return hex.EncodeToString(sum[:])
}

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

func freeID(name, prefix string, taken func(string) bool) string {
	// Names without Latin letters use numbered IDs rather than numeric fragments.
	if base := slugify(name); strings.ContainsAny(base, "abcdefghijklmnopqrstuvwxyz") {
		if !taken(base) {
			return base
		}
		for i := 2; i < 1000; i++ {
			if candidate := fmt.Sprintf("%s-%d", base, i); !taken(candidate) {
				return candidate
			}
		}
	}
	for i := 1; ; i++ {
		if candidate := fmt.Sprintf("%s-%d", prefix, i); !taken(candidate) {
			return candidate
		}
	}
}

func slugify(value string) string {
	var builder strings.Builder
	lastHyphen := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastHyphen = false
		default:
			if !lastHyphen && builder.Len() > 0 {
				builder.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if len(slug) > 48 {
		slug = strings.Trim(slug[:48], "-")
	}
	return slug
}
