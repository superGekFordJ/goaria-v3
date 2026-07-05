package extension

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPairing_NonceOneTime(t *testing.T) {
	store := NewSecretStore()
	ps := NewPairingService(store, nil)

	url, err := ps.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ps.Stop()

	// First request with nonce should succeed.
	resp1 := httpGet(t, url)
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", resp1.StatusCode)
	}

	// Second request with same nonce should get 410 Gone.
	resp2 := httpGet(t, url)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusGone {
		t.Fatalf("replay: expected 410, got %d", resp2.StatusCode)
	}
}

func TestPairing_HostHeaderCheck(t *testing.T) {
	store := NewSecretStore()
	ps := NewPairingService(store, nil)

	url, err := ps.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ps.Stop()

	// Replace 127.0.0.1 with evil.com in the Host header.
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "evil.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for bad Host, got %d", resp.StatusCode)
	}
}

func TestPairing_SecretInDOMNotURL(t *testing.T) {
	store := NewSecretStore()
	ps := NewPairingService(store, nil)

	url, err := ps.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ps.Stop()

	resp := httpGet(t, url)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, "data-secret=") {
		t.Fatal("response HTML missing data-secret attribute")
	}
	if strings.Contains(url, "secret=") {
		t.Fatal("secret must not appear in URL")
	}
}

func TestPairing_StopClosesListener(t *testing.T) {
	store := NewSecretStore()
	ps := NewPairingService(store, nil)

	_, err := ps.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ps.Stop()

	if ps.IsActive() {
		t.Fatal("expected inactive after Stop")
	}
}

func TestPairing_NoCORSHeader(t *testing.T) {
	store := NewSecretStore()
	ps := NewPairingService(store, nil)

	url, err := ps.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ps.Stop()

	resp := httpGet(t, url)
	defer resp.Body.Close()
	if h := resp.Header.Get("Access-Control-Allow-Origin"); h != "" {
		t.Fatalf("expected no CORS header, got: %s", h)
	}
}

func TestPairing_ConcurrentStart_ReturnsExisting(t *testing.T) {
	store := NewSecretStore()
	ps := NewPairingService(store, nil)

	url1, err := ps.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ps.Stop()

	// Second Start should return the same URL, not start a new server.
	url2, err := ps.Start()
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if url1 != url2 {
		t.Fatalf("expected same URL, got %s vs %s", url1, url2)
	}
}

func httpGet(t *testing.T, urlStr string) *http.Response {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(urlStr)
	if err != nil {
		t.Fatalf("GET %s: %v", urlStr, err)
	}
	return resp
}

// TestPairing_Start_UsesFallbackPort verifies Start binds to one of the
// PairPortFallbacks entries rather than a random OS port.
func TestPairing_Start_UsesFallbackPort(t *testing.T) {
	store := NewSecretStore()
	ps := NewPairingService(store, nil)

	urlStr, err := ps.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ps.Stop()

	parsed, err := url.Parse(urlStr)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	host, portStr, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("split host:port: %v", err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("expected 127.0.0.1, got %s", host)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("parse port: %v", err)
	}
	found := false
	for _, p := range PairPortFallbacks {
		if port == p {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("port %d not in PairPortFallbacks %v", port, PairPortFallbacks)
	}
}

// TestPairing_AllPortsInUse_ReturnsError verifies that when every fallback
// port is occupied, Start returns an error mentioning "in use".
func TestPairing_AllPortsInUse_ReturnsError(t *testing.T) {
	var blockers []net.Listener
	for _, port := range PairPortFallbacks {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			// Port already taken by something else; that still counts as occupied.
			continue
		}
		blockers = append(blockers, l)
	}
	defer func() {
		for _, l := range blockers {
			l.Close()
		}
	}()
	if len(blockers) == 0 {
		t.Skip("could not occupy any pairing port")
	}

	store := NewSecretStore()
	ps := NewPairingService(store, nil)
	_, err := ps.Start()
	if err == nil {
		ps.Stop()
		t.Fatal("expected error when all pairing ports are in use")
	}
	if !strings.Contains(err.Error(), "in use") {
		t.Fatalf("error should mention ports in use, got: %v", err)
	}
}
