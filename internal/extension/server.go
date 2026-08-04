package extension

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/events"

	"github.com/gorilla/websocket"
)

// allowEmptySecret is the dev escape hatch for the MVP empty-secret bypass.
// Production rejects empty secrets unless GOARIA_EXTENSION_ALLOW_EMPTY_SECRET=1.
// atomic.Bool so tests can flip it without racing in-flight handleConn readers.
var allowEmptySecret atomic.Bool

func init() {
	allowEmptySecret.Store(os.Getenv("GOARIA_EXTENSION_ALLOW_EMPTY_SECRET") == "1")
}

// authFailedSink is a test-only hook for observing extension:auth_failed emits
// without a full Wails event hub. Production leaves this nil.
var authFailedSink func()

// Server is the WebSocket server that receives download requests from the browser extension.
// It holds a TaskAdder (tasks.Service), never rpc.DownloadEngine directly.
type Server struct {
	mu               sync.RWMutex
	listener         net.Listener
	httpServer       *http.Server
	upgrader         websocket.Upgrader
	store            *SecretStore
	eventHub         *events.Hub
	taskAdder        TaskAdder
	pairingService   *PairingService
	connectedClients int
	wsPort           int
	paired           bool
	activeConns      map[*websocket.Conn]struct{}
}

// NewServer creates a new extension WebSocket server.
// taskAdder is the access-layer contract: downloads go through tasks.Service.
func NewServer(eventHub *events.Hub, taskAdder TaskAdder, store *SecretStore) *Server {
	return &Server{
		eventHub:  eventHub,
		taskAdder: taskAdder,
		store:     store,
		upgrader: websocket.Upgrader{
			CheckOrigin:  checkOrigin,
			Subprotocols: []string{"goaria-extension"},
		},
		activeConns: make(map[*websocket.Conn]struct{}),
	}
}

// SetPairingService injects the pairing service for shut-down-after-use.
func (s *Server) SetPairingService(ps *PairingService) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairingService = ps
}

// StartPairing creates or reuses the pairing service and returns the pairing URL.
// If an active pairing service already exists, returns its URL (boundary: double-click).
func (s *Server) StartPairing() (string, error) {
	s.mu.Lock()
	ps := s.pairingService
	s.mu.Unlock()
	if ps != nil && ps.IsActive() {
		return ps.Start()
	}
	ps = NewPairingService(s.store, s.eventHub)
	s.mu.Lock()
	s.pairingService = ps
	s.paired = false
	s.mu.Unlock()
	return ps.Start()
}

// RegeneratePairing delegates to the active pairing service's Regenerate.
// Returns an error if no pairing service is active.
func (s *Server) RegeneratePairing() (string, error) {
	s.mu.Lock()
	ps := s.pairingService
	s.mu.Unlock()
	if ps == nil {
		return "", fmt.Errorf("no active pairing service")
	}
	return ps.Regenerate()
}

// GetStore returns the secret store (shared with the pairing service).
func (s *Server) GetStore() *SecretStore {
	return s.store
}

// Start binds to the first available port and begins accepting.
// preferredPort > 0 is tried first, then WSPortFallbacks entries (excluding it).
// Non-blocking: accept loop runs in a goroutine.
func (s *Server) Start(preferredPort int) error {
	ports := WSPortFallbacks
	if preferredPort > 0 {
		ports = []int{preferredPort}
		for _, p := range WSPortFallbacks {
			if p != preferredPort {
				ports = append(ports, p)
			}
		}
	}

	var listener net.Listener
	for _, port := range ports {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		listener = l
		s.mu.Lock()
		s.wsPort = port
		s.mu.Unlock()
		break
	}
	if listener == nil {
		return fmt.Errorf("all WebSocket ports %v are in use", ports)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleWS)

	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()

	srv := &http.Server{Handler: mux}
	s.mu.Lock()
	s.httpServer = srv
	s.mu.Unlock()

	go func() {
		_ = srv.Serve(listener)
	}()
	return nil
}

// handleWS upgrades to WebSocket and delegates to handleConn.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.handleConn(conn)
}

// handleConn validates Origin + Secret (if-branch: empty=MVP, non-empty=production),
// then processes download requests.
func (s *Server) handleConn(conn *websocket.Conn) {
	defer conn.Close()

	s.mu.Lock()
	s.activeConns[conn] = struct{}{}
	s.connectedClients++
	count := s.connectedClients
	s.mu.Unlock()
	s.emitStatus(count)

	defer func() {
		s.mu.Lock()
		delete(s.activeConns, conn)
		s.connectedClients--
		c := s.connectedClients
		if c < 0 {
			c = 0
		}
		s.mu.Unlock()
		s.emitStatus(c)
	}()

	secret := s.store.GetSecret()
	if secret == "" {
		if !allowEmptySecret.Load() {
			return
		}
	} else {
		var auth AuthMessage
		if err := conn.ReadJSON(&auth); err != nil {
			return
		}
		if auth.Secret != secret {
			s.notifyAuthFailed()
			return
		}
		if s.markPaired() {
			s.NotifyPaired()
		}
	}

	// Confirm auth so the extension can transition to "connected" immediately,
	// without waiting for the first download_ack (which would never arrive
	// because interception stays disabled until connected — a deadlock).
	authAck, err := json.Marshal(map[string]string{"type": MsgTypeAuthAck})
	if err != nil {
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, authAck)

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var msg struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		if msg.Type == MsgTypeDownload {
			var req DownloadRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				s.writeAck(conn, DownloadResponse{
					Type:    MsgTypeDownloadAck,
					Success: false,
					Error:   "invalid request",
				})
				continue
			}
			resp := s.handleDownload(req)
			s.writeAck(conn, resp)
		}
	}
}

// handleDownload forwards the request to TaskAdder (tasks.Service), never rpc.DownloadEngine.
func (s *Server) handleDownload(req DownloadRequest) DownloadResponse {
	if s.taskAdder == nil {
		return DownloadResponse{Type: MsgTypeDownloadAck, Success: false, Error: "no task service"}
	}
	gid, err := s.taskAdder.AddUriFromExtension(req)
	if err != nil {
		return DownloadResponse{Type: MsgTypeDownloadAck, Success: false, Error: err.Error()}
	}
	return DownloadResponse{Type: MsgTypeDownloadAck, Success: true, GID: gid}
}

func (s *Server) writeAck(conn *websocket.Conn, resp DownloadResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, data)
}

// GetStatus returns the current server status for the frontend.
func (s *Server) GetStatus() ExtensionStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status := "disconnected"
	if s.listener != nil {
		status = "listening"
	}
	if s.paired {
		status = "paired"
	}
	return ExtensionStatus{
		Status:           status,
		WSPort:           s.wsPort,
		ConnectedClients: s.connectedClients,
		Paired:           s.paired,
	}
}

// Stop closes the listener, HTTP server, and all active WebSocket connections.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.httpServer != nil {
		_ = s.httpServer.Close()
		s.httpServer = nil
	}
	if s.listener != nil {
		_ = s.listener.Close()
		s.listener = nil
	}
	for conn := range s.activeConns {
		_ = conn.Close()
		delete(s.activeConns, conn)
	}
}

// emitStatus pushes extension:status to the frontend via EventHub.
func (s *Server) emitStatus(connectedClients int) {
	if s.eventHub == nil {
		return
	}
	s.mu.RLock()
	port := s.wsPort
	listening := s.listener != nil
	paired := s.paired
	s.mu.RUnlock()
	status := "disconnected"
	if listening {
		status = "listening"
	}
	if paired {
		status = "paired"
	}
	s.eventHub.EmitExtensionStatus(ExtensionStatus{
		Status:           status,
		WSPort:           port,
		ConnectedClients: connectedClients,
		Paired:           paired,
	})
}

// markPaired transitions paired to true; returns true only on the first transition.
func (s *Server) markPaired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paired {
		return false
	}
	s.paired = true
	return true
}

// NotifyPaired emits extension:paired and shuts down the pairing service.
// Re-checks paired under the lock to narrow the window; a concurrent unpair
// could still flip paired between RUnlock and the emit (benign UI flicker).
func (s *Server) NotifyPaired() {
	s.mu.RLock()
	paired := s.paired
	s.mu.RUnlock()
	if paired && s.eventHub != nil {
		s.eventHub.EmitExtensionPaired()
	}
	s.mu.Lock()
	ps := s.pairingService
	s.mu.Unlock()
	if ps != nil {
		ps.Stop()
	}
}

// NotifyUnpaired rotates the secret (never clears), disconnects active connections,
// emits extension:unpaired. Rotation ensures the old extension can no longer auth.
func (s *Server) NotifyUnpaired() {
	s.mu.Lock()
	s.paired = false
	conns := make([]*websocket.Conn, 0, len(s.activeConns))
	for conn := range s.activeConns {
		conns = append(conns, conn)
		delete(s.activeConns, conn)
	}
	s.mu.Unlock()

	for _, conn := range conns {
		_ = conn.Close()
	}

	if s.store != nil {
		newSecret := s.store.GenerateSecret()
		if newSecret != "" {
			s.store.SetSecret(newSecret)
			config.Update(func(c *config.AppConfig) { c.ExtensionSecret = newSecret })
		} else {
			log.Printf("[Extension] secret rotation failed; keeping old secret")
		}
	}
	if s.eventHub != nil {
		s.eventHub.EmitExtensionUnpaired()
	}
}

// notifyAuthFailed emits extension:auth_failed to the desktop frontend only.
// It is never sent back over the WS to the untrusted client (no auth oracle).
func (s *Server) notifyAuthFailed() {
	if s.eventHub != nil {
		s.eventHub.EmitExtensionAuthFailed()
	}
	if authFailedSink != nil {
		authFailedSink()
	}
}

// checkOrigin only allows browser extension origins.
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	return isExtensionOrigin(origin)
}

func isExtensionOrigin(origin string) bool {
	return startsWith(origin, "chrome-extension://") || startsWith(origin, "moz-extension://")
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
