package extension

import (
	"io"
	"net/http"
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

func httpGet(t *testing.T, url string) *http.Response {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}
