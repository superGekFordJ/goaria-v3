package extension

import (
	"fmt"
	"html"
	"net"
	"net/http"
	"sync"

	"goaria-v3/internal/events"
)

// PairingService runs a short-lived HTTP server on a random port.
// The pairing page injects the secret into the DOM (data-secret) and shuts down after use.
type PairingService struct {
	mu           sync.Mutex
	listener     net.Listener
	httpServer   *http.Server
	store        *SecretStore
	eventHub     *events.Hub
	active       bool
	pairURL      string
	expectedHost string
}

// NewPairingService creates a new pairing service.
func NewPairingService(store *SecretStore, eventHub *events.Hub) *PairingService {
	return &PairingService{
		store:    store,
		eventHub: eventHub,
	}
}

// IsActive returns true if a pairing server is currently running.
func (p *PairingService) IsActive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active
}

// Start launches a temporary HTTP server on 127.0.0.1:0 (random port).
// If already active, returns the existing URL (boundary: concurrent double-click).
func (p *PairingService) Start() (string, error) {
	p.mu.Lock()
	if p.active {
		url := p.pairURL
		p.mu.Unlock()
		return url, nil
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		p.mu.Unlock()
		return "", fmt.Errorf("pairing listen: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port
	expectedHost := fmt.Sprintf("127.0.0.1:%d", port)

	nonce := p.store.GenerateNonce()
	p.store.SetPairNonce(nonce)

	secret := p.store.GenerateSecret()
	p.store.SetSecret(secret)

	mux := http.NewServeMux()
	mux.HandleFunc(PairPagePath, func(w http.ResponseWriter, r *http.Request) {
		p.handlePairPage(w, r, expectedHost, nonce, secret)
	})

	srv := &http.Server{Handler: mux}

	p.listener = listener
	p.httpServer = srv
	p.active = true
	p.expectedHost = expectedHost
	p.pairURL = fmt.Sprintf("http://127.0.0.1:%d%s?n=%s", port, PairPagePath, nonce)
	url := p.pairURL
	p.mu.Unlock()

	go func() {
		_ = srv.Serve(listener)
	}()

	return url, nil
}

// handlePairPage validates Host header + nonce, then returns HTML with data-secret.
func (p *PairingService) handlePairPage(w http.ResponseWriter, r *http.Request, expectedHost, nonce, secret string) {
	// Host header validation: prevent DNS rebinding.
	if r.Host != expectedHost {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	// Nonce one-time consumption.
	queryNonce := r.URL.Query().Get("n")
	if !p.store.VerifyAndConsumeNonce(queryNonce) {
		w.WriteHeader(http.StatusGone)
		return
	}

	// No CORS headers — the extension reads DOM directly.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := pairPageHTML(secret)
	_, _ = w.Write([]byte(html))
}

// Stop closes the pairing HTTP server.
func (p *PairingService) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.httpServer != nil {
		_ = p.httpServer.Close()
		p.httpServer = nil
	}
	if p.listener != nil {
		_ = p.listener.Close()
		p.listener = nil
	}
	p.active = false
}

// pairPageHTML returns a minimal HTML page with the secret in a data attribute.
// Bilingual text since the pairing page is outside the Wails WebView (no i18n).
func pairPageHTML(secret string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>GoAria Pairing</title></head>
<body>
<div id="cfg" data-secret="%s"></div>
<p>Pairing in progress... / 正在绑定...</p>
</body>
</html>`, html.EscapeString(secret))
}
