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

// NormalizeMAC lowercases the MAC and strips colons/dashes, leaving 12 hex chars.
func NormalizeMAC(rawMAC string) string {
	s := strings.ToLower(rawMAC)
	s = strings.ReplaceAll(s, ":", "")
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
