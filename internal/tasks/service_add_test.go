package tasks

import (
	"testing"
)

func TestResolveHostIP_IPInURL(t *testing.T) {
	got := resolveHostIP("https://192.168.1.1/file")
	if got != "192.168.1.1" {
		t.Errorf("resolveHostIP(IP url) = %q, want 192.168.1.1", got)
	}
}

func TestResolveHostIP_InvalidURL(t *testing.T) {
	got := resolveHostIP(":::invalid")
	if got != "" {
		t.Errorf("resolveHostIP(invalid url) = %q, want empty", got)
	}
}

func TestResolveHostIP_NoHost(t *testing.T) {
	got := resolveHostIP("https:///file")
	if got != "" {
		t.Errorf("resolveHostIP(no host) = %q, want empty", got)
	}
}

func TestResolveHostIP_DomainResolves(t *testing.T) {
	got := resolveHostIP("https://example.com/file")
	if got == "" {
		t.Skip("DNS resolve returned empty — skipping (offline or DNS unavailable)")
	}
}

func TestResolveHostIP_DNSFailure_ReturnsEmpty(t *testing.T) {
	got := resolveHostIP("https://nonexistent.invalid.domain.example/file")
	if got != "" {
		t.Errorf("resolveHostIP(nonexistent domain) = %q, want empty", got)
	}
}
