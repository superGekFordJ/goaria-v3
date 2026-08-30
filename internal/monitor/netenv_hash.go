package monitor

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	routeCodeDirect = "0"
	routeCodeProxy  = "1"
)

// NormalizeMAC canonicalizes six-byte separated MACs (zero-padding single-digit bytes to 12 hex chars) and preserves unseparated surrogate values.
func NormalizeMAC(rawMAC string) string {
	clean := strings.ToLower(strings.TrimSpace(rawMAC))
	parts := strings.FieldsFunc(clean, func(r rune) bool {
		return r == ':' || r == '-'
	})
	if len(parts) == 6 {
		var sb strings.Builder
		sb.Grow(12)
		for _, p := range parts {
			if len(p) == 1 {
				sb.WriteByte('0')
			}
			sb.WriteString(p)
		}
		return sb.String()
	}
	s := strings.ReplaceAll(clean, ":", "")
	s = strings.ReplaceAll(s, "-", "")
	return s
}

// ComputeEnvKey returns the first 8 hex chars of sha256(routeCode + ":" + normalizedMAC).
func ComputeEnvKey(routeCode string, physicalMAC string) string {
	normalized := NormalizeMAC(physicalMAC)
	input := routeCode + ":" + normalized
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:4])
}

// ComputeHistoryKey joins scope and envKey into the persisted bucket key (e.g. "wan-7f2a9b").
func ComputeHistoryKey(scope string, envKey string) string {
	if scope == "" {
		scope = "wan"
	}
	return scope + "-" + envKey
}
