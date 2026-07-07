package tasks

import (
	"testing"

	"goaria-v3/internal/speedstats"
)

// resolveClassificationURL mirrors the finalURL-priority selection used in
// addTaskCandidate: prefer finalURL (post-redirect CDN URL), fall back to the
// original URL when finalURL is empty (graceful degradation for old extensions).
func resolveClassificationURL(finalURL, originalURL string) string {
	if finalURL == "" {
		return originalURL
	}
	return finalURL
}

func TestFinalURL_ClassificationURLSelection(t *testing.T) {
	t.Run("finalURL present uses finalURL", func(t *testing.T) {
		got := resolveClassificationURL("https://cdn.example.com/file", "https://openlist.home/file")
		if got != "https://cdn.example.com/file" {
			t.Errorf("classificationURL = %q, want cdn URL", got)
		}
	})
	t.Run("finalURL empty falls back to original URL", func(t *testing.T) {
		got := resolveClassificationURL("", "https://openlist.home/file")
		if got != "https://openlist.home/file" {
			t.Errorf("classificationURL = %q, want original URL", got)
		}
	})
	t.Run("finalURL same as url is a no-op", func(t *testing.T) {
		got := resolveClassificationURL("https://example.com/file", "https://example.com/file")
		if got != "https://example.com/file" {
			t.Errorf("classificationURL = %q, want same URL", got)
		}
	})
}

func TestFinalURL_DomainFromOriginalURL_ScopeFromCDNIP(t *testing.T) {
	classifier := speedstats.NewScopeClassifier()
	originalURL := "https://openlist.home.fordj.me/file"
	cdnIP := "203.0.113.1" // TEST-NET-1, public → wan

	scope, domain := classifier.ClassifyByURLAndIP(originalURL, cdnIP)

	if domain != "openlist.home.fordj.me" {
		t.Errorf("domain = %q, want openlist.home.fordj.me (from original URL)", domain)
	}
	if scope != speedstats.ScopeWAN {
		t.Errorf("scope = %q, want wan (from CDN IP)", scope)
	}
}

func TestFinalURL_LANOriginal_WANCDN_ScopeFromCDNIP(t *testing.T) {
	classifier := speedstats.NewScopeClassifier()
	// Original host resolves to a LAN IP, but finalUrl CDN IP is WAN.
	// scope must come from the CDN IP (wan), domain from the original URL.
	originalURL := "https://openlist.home.fordj.me/file"
	lanIP := "192.168.1.50"

	scope, domain := classifier.ClassifyByURLAndIP(originalURL, lanIP)

	if domain != "openlist.home.fordj.me" {
		t.Errorf("domain = %q, want original host", domain)
	}
	if scope != speedstats.ScopeLAN {
		t.Errorf("scope = %q, want lan (from LAN IP)", scope)
	}
}

func TestFinalURL_IPLiteral_ReturnedDirectly(t *testing.T) {
	got := resolveHostIP("https://203.0.113.1/file")
	if got != "203.0.113.1" {
		t.Errorf("resolveHostIP(IP finalUrl) = %q, want 203.0.113.1", got)
	}
}

func TestFinalURL_Malformed_ResolveHostIPReturnsEmpty(t *testing.T) {
	got := resolveHostIP(":::invalid")
	if got != "" {
		t.Errorf("resolveHostIP(malformed finalUrl) = %q, want empty", got)
	}
}

func TestFinalURL_Malformed_ClassifyByURLAndIPConservativeWAN(t *testing.T) {
	classifier := speedstats.NewScopeClassifier()
	originalURL := "https://openlist.home.fordj.me/file"
	// resolveHostIP(malformed) → "" → ClassifyByURLAndIP conservatively defaults wan.
	scope, domain := classifier.ClassifyByURLAndIP(originalURL, "")

	if domain != "openlist.home.fordj.me" {
		t.Errorf("domain = %q, want original host", domain)
	}
	if scope != speedstats.ScopeWAN {
		t.Errorf("scope = %q, want wan (conservative default for empty IP)", scope)
	}
}

func TestFinalURL_CacheConsistency_WithClassifyByHost(t *testing.T) {
	classifier := speedstats.NewScopeClassifier()
	originalURL := "https://openlist.home.fordj.me/file"
	cdnIP := "203.0.113.1" // wan

	scope, domain := classifier.ClassifyByURLAndIP(originalURL, cdnIP)
	if domain != "openlist.home.fordj.me" {
		t.Fatalf("domain = %q, want original host", domain)
	}

	// ClassifyByURLAndIP caches host→scope; a subsequent ClassifyByHost on the
	// same original host must hit the cache and return the same scope, matching
	// the direct-add path (no BBR history fragmentation).
	cachedScope := classifier.ClassifyByHost("openlist.home.fordj.me")
	if cachedScope != scope {
		t.Errorf("ClassifyByHost cache miss: got %q, want %q (consistent with ClassifyByURLAndIP)", cachedScope, scope)
	}
}

func TestFinalURL_FullClassificationFlow_RedirectScenario(t *testing.T) {
	// Simulate the SmartThread extension path: skipHeadProbe=true → remoteIP="".
	// finalURL points to a CDN (IP literal to avoid DNS dependency in tests).
	classifier := speedstats.NewScopeClassifier()
	candidate := struct {
		url      string
		finalURL string
	}{
		url:      "https://openlist.home.fordj.me/file",
		finalURL: "https://203.0.113.1/cdn-file",
	}

	classificationURL := resolveClassificationURL(candidate.finalURL, candidate.url)
	finalIP := resolveHostIP(classificationURL)
	scope, domain := classifier.ClassifyByURLAndIP(candidate.url, finalIP)

	if finalIP != "203.0.113.1" {
		t.Fatalf("finalIP = %q, want 203.0.113.1 (CDN IP from finalURL)", finalIP)
	}
	if domain != "openlist.home.fordj.me" {
		t.Errorf("domain = %q, want openlist.home.fordj.me (from original URL)", domain)
	}
	if scope != speedstats.ScopeWAN {
		t.Errorf("scope = %q, want wan (CDN IP is public)", scope)
	}
}

func TestFinalURL_FullClassificationFlow_EmptyFinalURL_Degradation(t *testing.T) {
	// Old extension without final_url support: FinalURL="" → fall back to
	// resolveHostIP(candidate.url). Use an IP-literal original URL to avoid DNS.
	classifier := speedstats.NewScopeClassifier()
	candidate := struct {
		url      string
		finalURL string
	}{
		url:      "https://203.0.113.10/file",
		finalURL: "",
	}

	classificationURL := resolveClassificationURL(candidate.finalURL, candidate.url)
	finalIP := resolveHostIP(classificationURL)
	scope, domain := classifier.ClassifyByURLAndIP(candidate.url, finalIP)

	if finalIP != "203.0.113.10" {
		t.Fatalf("finalIP = %q, want 203.0.113.10 (from original URL fallback)", finalIP)
	}
	if domain != "203.0.113.10" {
		t.Errorf("domain = %q, want 203.0.113.10 (from original URL)", domain)
	}
	if scope != speedstats.ScopeWAN {
		t.Errorf("scope = %q, want wan", scope)
	}
}

func TestFinalURL_FullClassificationFlow_SameURL_NoOp(t *testing.T) {
	// finalUrl == url (no redirect): resolveHostIP produces the same IP, no-op.
	classifier := speedstats.NewScopeClassifier()
	candidate := struct {
		url      string
		finalURL string
	}{
		url:      "https://203.0.113.20/file",
		finalURL: "https://203.0.113.20/file",
	}

	classificationURL := resolveClassificationURL(candidate.finalURL, candidate.url)
	finalIP := resolveHostIP(classificationURL)
	scope, domain := classifier.ClassifyByURLAndIP(candidate.url, finalIP)

	if finalIP != "203.0.113.20" {
		t.Fatalf("finalIP = %q, want 203.0.113.20", finalIP)
	}
	if domain != "203.0.113.20" {
		t.Errorf("domain = %q, want 203.0.113.20", domain)
	}
	if scope != speedstats.ScopeWAN {
		t.Errorf("scope = %q, want wan", scope)
	}
}
