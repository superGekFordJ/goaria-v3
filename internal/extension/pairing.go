package extension

import (
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"sync"
	"time"

	"goaria-v3/internal/events"
)

// PairingTTL is the auto-stop duration for an abandoned pairing server.
const PairingTTL = 10 * time.Minute

// pairingTTL is overridable in tests.
var pairingTTL = PairingTTL

// PairingService runs a short-lived HTTP server on a fixed pairing port.
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
	ttlTimer     *time.Timer
	gen          uint64
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

// Start launches a temporary HTTP server on the first available pairing port.
// If already active, returns the existing URL (boundary: concurrent double-click).
func (p *PairingService) Start() (string, error) {
	p.mu.Lock()
	if p.active {
		url := p.pairURL
		p.mu.Unlock()
		return url, nil
	}

	var listener net.Listener
	for _, port := range PairPortFallbacks {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		listener = l
		break
	}
	if listener == nil {
		p.mu.Unlock()
		return "", fmt.Errorf("all pairing ports %v are in use", PairPortFallbacks)
	}

	port := listener.Addr().(*net.TCPAddr).Port
	expectedHost := fmt.Sprintf("127.0.0.1:%d", port)

	nonce := p.store.GenerateNonce()
	if nonce == "" {
		p.mu.Unlock()
		return "", errors.New("failed to generate pairing nonce")
	}
	p.store.SetPairNonce(nonce)

	secret := p.store.GetSecret()
	if secret == "" {
		p.mu.Unlock()
		return "", errors.New("extension secret not initialized; ensure config.Load() has run")
	}

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
	p.gen++
	myGen := p.gen
	p.ttlTimer = time.AfterFunc(pairingTTL, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.gen != myGen {
			return
		}
		p.stopLocked()
	})
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
	p.stopLocked()
}

// stopLocked closes the server and cancels the TTL timer; caller must hold p.mu.
func (p *PairingService) stopLocked() {
	if p.ttlTimer != nil {
		p.ttlTimer.Stop()
		p.ttlTimer = nil
	}
	if p.httpServer != nil {
		_ = p.httpServer.Close()
		p.httpServer = nil
	}
	if p.listener != nil {
		_ = p.listener.Close()
		p.listener = nil
	}
	p.active = false
	p.pairURL = ""
	p.expectedHost = ""
}

// Regenerate stops the current pairing server and starts a fresh one with a
// new nonce + URL. The global secret is NOT rotated — already-paired
// extensions remain connected. Use this to refresh an expired pairing URL.
// The lock is released between stop and start (Start re-acquires it), but the
// single-method API prevents wrong-order calls.
func (p *PairingService) Regenerate() (string, error) {
	p.mu.Lock()
	if p.active {
		p.stopLocked()
	}
	p.mu.Unlock()
	return p.Start()
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
