package rpc

import (
	"testing"
)

func TestSurgeEngine_MapStatus(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"downloading", "active"},
		{"pausing", "active"},
		{"paused", "paused"},
		{"queued", "waiting"},
		{"completed", "complete"},
		{"error", "error"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		got := mapStatus(tt.input)
		if got != tt.expected {
			t.Errorf("mapStatus(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
