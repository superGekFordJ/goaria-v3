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

	"goaria-v3/internal/config"
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

// withConfig sets config.Current to a fresh AppConfig for the test and restores
// the original on cleanup, so persistence tests don't clobber the real config.
func withConfig(t *testing.T) {
	t.Helper()
	orig := config.Current
	config.Current = &config.AppConfig{}
	t.Cleanup(func() { config.Current = orig })
}

func TestPairing_Start_PersistsSecretToConfig(t *testing.T) {
	withConfig(t)
	store := NewSecretStore()
	ps := NewPairingService(store, nil)

	if _, err := ps.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ps.Stop()

	if config.Current.ExtensionSecret != store.GetSecret() {
		t.Fatalf("config.ExtensionSecret %q != store secret %q", config.Current.ExtensionSecret, store.GetSecret())
	}
	if config.Current.ExtensionSecret == "" {
		t.Fatal("persisted secret should not be empty")
	}
}

func TestPairing_Regenerate_NewNonceAndSecret(t *testing.T) {
	withConfig(t)
	store := NewSecretStore()
	ps := NewPairingService(store, nil)

	url1, err := ps.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ps.Stop()
	secret1 := store.GetSecret()

	url2, err := ps.Regenerate()
	if err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	defer ps.Stop()
	secret2 := store.GetSecret()

	if url2 == url1 {
		t.Fatal("Regenerate should produce a new URL")
	}
	if secret2 == secret1 {
		t.Fatal("Regenerate should produce a new secret")
	}
	if secret2 == "" {
		t.Fatal("new secret should not be empty")
	}
}

func TestPairing_Regenerate_StopsOldServer(t *testing.T) {
	withConfig(t)
	store := NewSecretStore()
	ps := NewPairingService(store, nil)

	url1, err := ps.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Consume the old nonce so we can distinguish "old server gone" from "nonce replay".
	respOld := httpGet(t, url1)
	respOld.Body.Close()

	if _, err := ps.Regenerate(); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	defer ps.Stop()

	// The old URL's nonce is already consumed AND the old server is stopped.
	// If the new server happened to rebind the same port, the old nonce is gone (410).
	// If the port is free, we get a connection error. Either way, no 200 with the old secret.
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get(url1)
	if err != nil {
		return // Connection error — old listener is gone. Expected.
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("old URL should not return 200 after Regenerate (stale nonce/server)")
	}
}

func TestPairing_TTL_AutoStops(t *testing.T) {
	withConfig(t)
	origTTL := pairingTTL
	pairingTTL = 50 * time.Millisecond
	t.Cleanup(func() { pairingTTL = origTTL })

	store := NewSecretStore()
	ps := NewPairingService(store, nil)

	if _, err := ps.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !ps.IsActive() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ps.IsActive() {
		t.Fatal("pairing service should auto-stop after TTL")
	}
}

func TestPairing_Start_EmptySecretReturnsError(t *testing.T) {
	withConfig(t)
	origReader := randReader
	randReader = &failingReader{}
	t.Cleanup(func() { randReader = origReader })

	store := NewSecretStore()
	ps := NewPairingService(store, nil)

	_, err := ps.Start()
	if err == nil {
		ps.Stop()
		t.Fatal("expected error when secret generation fails")
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Fatalf("error should mention secret, got: %v", err)
	}
	if ps.IsActive() {
		t.Fatal("pairing service should not be active after empty-secret error")
	}
}

type failingReader struct{}

func (f *failingReader) Read(p []byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}
