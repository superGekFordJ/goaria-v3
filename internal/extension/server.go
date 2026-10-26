package extension

import (
	"context"
	"encoding/json"
	"errors"
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

const wsReadLimit int64 = 1 << 20

var (
	allowEmptySecret atomic.Bool

	authReadTimeout    = 5 * time.Second
	resolveConcurrency = 2
	batchConcurrency   = 1
	perConnInFlightMax = 4
)

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
	activeConns      map[*safeConn]struct{}
	hostVersion      string
	linkage          Linkage
	extractorGen     uint64
	resolveInFlight  atomic.Int32
	batchInFlight    atomic.Int32
	idemp            *idempotencyCache
	// opCtx is cancelled on unpair/Stop and replaced for the next pairing
	// generation. Dropping one connection does not cancel it.
	opCtx    context.Context
	opCancel context.CancelFunc
}

// NewServer creates a new extension WebSocket server.
// taskAdder is the access-layer contract: downloads go through tasks.Service.
func NewServer(eventHub *events.Hub, taskAdder TaskAdder, store *SecretStore) *Server {
	opCtx, opCancel := context.WithCancel(context.Background())
	return &Server{
		eventHub:  eventHub,
		taskAdder: taskAdder,
		store:     store,
		upgrader: websocket.Upgrader{
			CheckOrigin:  checkOrigin,
			Subprotocols: []string{"goaria-extension"},
		},
		activeConns:  make(map[*safeConn]struct{}),
		idemp:        newIdempotencyCache(),
		extractorGen: 1,
		opCtx:        opCtx,
		opCancel:     opCancel,
	}
}

// SetPairingService injects the pairing service for shut-down-after-use.
func (s *Server) SetPairingService(ps *PairingService) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairingService = ps
}

// SetHostVersion records the desktop app version echoed in auth_ack.
func (s *Server) SetHostVersion(version string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hostVersion = version
}

// SetLinkage injects optional extractor seams after NewServer.
// Already-authed connections keep the capability snapshot from auth_ack.
func (s *Server) SetLinkage(l Linkage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.linkage = l
	if s.extractorGen == 0 {
		s.extractorGen = 1
	}
}

// ReplaceExtractorLinkage atomically replaces the Extractor linkage fields,
// advances the Extractor linkage generation, retires old Extractor idempotency,
// invalidates the old resolver, and disconnects current clients.
func (s *Server) ReplaceExtractorLinkage(l Linkage) {
	s.mu.Lock()
	oldResolver := s.linkage.Resolver
	oldGen := s.extractorGen
	if oldGen == 0 {
		oldGen = 1
	}
	s.linkage.Resolver = l.Resolver
	s.linkage.Digests = l.Digests
	s.linkage.Committer = l.Committer
	s.extractorGen = oldGen + 1

	conns := make([]*safeConn, 0, len(s.activeConns))
	for conn := range s.activeConns {
		conns = append(conns, conn)
	}
	s.mu.Unlock()

	if oldResolver != nil && oldResolver != l.Resolver {
		oldResolver.Invalidate()
	}
	if s.idemp != nil {
		s.idemp.retireExtractorGeneration(oldGen)
	}
	for _, conn := range conns {
		go func(c *safeConn) {
			_ = c.Close()
		}(conn)
	}
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
		return "", errors.New("no active pairing service")
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

// handleConn validates Origin and Secret, then dispatches post-auth messages.
// Empty secrets require GOARIA_EXTENSION_ALLOW_EMPTY_SECRET=1.
func (s *Server) handleConn(conn *websocket.Conn) {
	sc := newSafeConn(conn)
	defer sc.Close()

	s.mu.Lock()
	s.activeConns[sc] = struct{}{}
	s.connectedClients++
	count := s.connectedClients
	s.mu.Unlock()
	s.emitStatus(count)

	defer func() {
		s.mu.Lock()
		delete(s.activeConns, sc)
		s.connectedClients--
		c := max(s.connectedClients, 0)
		s.mu.Unlock()
		s.emitStatus(c)
	}()

	sc.SetReadLimit(wsReadLimit)

	secret := s.store.GetSecret()
	if secret == "" {
		if !allowEmptySecret.Load() {
			return
		}
		// Explicit test/development mode skips the initial auth frame.
	} else {
		_ = sc.SetReadDeadline(time.Now().Add(authReadTimeout))
		var auth AuthMessage
		if err := sc.ReadJSON(&auth); err != nil {
			return
		}
		if auth.Type != MsgTypeAuth || auth.Secret != secret {
			if auth.Type == MsgTypeAuth && auth.Secret != secret {
				s.notifyAuthFailed()
			}
			return
		}
		_ = sc.SetReadDeadline(time.Time{})
		if s.markPaired() {
			s.NotifyPaired()
		}
	}

	s.mu.RLock()
	hostVersion := s.hostVersion
	connLinkage := s.linkage
	connExtractorGen := s.extractorGen
	s.mu.RUnlock()

	sc.setLinkageSnapshot(connLinkage, connExtractorGen)

	caps := computeConnectionCapabilities(secret, connLinkage)
	sc.setGrantedCaps(caps)
	ack := AuthAck{
		Type:            MsgTypeAuthAck,
		ProtocolVersion: ProtocolVersion,
		HostVersion:     hostVersion,
		Capabilities:    caps,

		Match: matchWireFromProvider(caps, connLinkage.Digests),
	}
	if err := sc.writeJSON(ack); err != nil {
		return
	}

	connCtx, connCancel := context.WithCancel(context.Background())
	defer connCancel()
	opCtx := s.operationContext()

	for {
		_, raw, err := sc.ReadMessage()
		if err != nil {
			return
		}

		var env RequestEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}

		switch env.Type {
		case MsgTypeDownload, MsgTypeHLS:
			s.dispatchDownload(sc, env, raw)
		case MsgTypeExtractorResolve:
			s.dispatchAsync(opCtx, connCtx, sc, env, raw, MsgTypeExtractorResolveAck, CapExtractorResolve, true)
		case MsgTypeBatchDownload:
			s.dispatchAsync(opCtx, connCtx, sc, env, raw, MsgTypeBatchDownloadAck, CapExtractorBatch, false)
		case MsgTypeDownloadBatch:
			s.dispatchDirectBatch(opCtx, connCtx, sc, env, raw)
		case MsgTypeDownloadBatchStatus:
			s.dispatchDirectBatchStatus(sc, env, raw)
		case MsgTypeAuth, MsgTypePing:
			// Post-handshake no-ops: the real client always sends auth after open,
			// including in explicit test/development mode; tests also write ping first.
		default:
			_ = sc.writeJSON(ProtocolError{
				Type:      MsgTypeProtocolError,
				RequestID: echoRequestID(env.RequestID),
				ErrorCode: ErrCodeUnsupported,
			})
		}
	}
}

func (s *Server) operationContext() context.Context {
	s.mu.RLock()
	ctx := s.opCtx
	s.mu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (s *Server) replaceOpContext() {
	s.mu.Lock()
	old := s.opCancel
	s.opCtx, s.opCancel = context.WithCancel(context.Background())
	s.mu.Unlock()
	if old != nil {
		old()
	}
}

func tryAcquireCount(counter *atomic.Int32, max int) bool {
	limit := int32(max)
	for {
		cur := counter.Load()
		if cur >= limit {
			return false
		}
		if counter.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

func (s *Server) tryAcquireGate(isResolve bool) bool {
	if isResolve {
		return tryAcquireCount(&s.resolveInFlight, resolveConcurrency)
	}
	return tryAcquireCount(&s.batchInFlight, batchConcurrency)
}

func (s *Server) releaseGate(isResolve bool) {
	if isResolve {
		s.resolveInFlight.Add(-1)
		return
	}
	s.batchInFlight.Add(-1)
}

func (s *Server) invalidateResolver() {
	s.mu.RLock()
	r := s.linkage.Resolver
	s.mu.RUnlock()
	if r != nil {
		r.Invalidate()
	}
}

func (s *Server) invalidateDirect() {
	s.mu.RLock()
	d := s.linkage.DirectCommitter
	s.mu.RUnlock()
	if d != nil {
		d.Invalidate()
	}
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
	if s.httpServer != nil {
		_ = s.httpServer.Close()
		s.httpServer = nil
	}
	if s.listener != nil {
		_ = s.listener.Close()
		s.listener = nil
	}
	conns := make([]*safeConn, 0, len(s.activeConns))
	for conn := range s.activeConns {
		conns = append(conns, conn)
		delete(s.activeConns, conn)
	}
	s.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
	s.replaceOpContext()
	s.invalidateResolver()
	s.invalidateDirect()
	if s.idemp != nil {
		s.idemp.clear()
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
	conns := make([]*safeConn, 0, len(s.activeConns))
	for conn := range s.activeConns {
		conns = append(conns, conn)
		delete(s.activeConns, conn)
	}
	s.mu.Unlock()

	for _, conn := range conns {
		_ = conn.Close()
	}

	s.replaceOpContext()
	s.invalidateResolver()
	s.invalidateDirect()

	if s.store != nil {
		newSecret := s.store.GenerateSecret()
		if newSecret != "" {
			s.store.SetSecret(newSecret)
			config.Update(func(c *config.AppConfig) { c.ExtensionSecret = newSecret })
		} else {
			log.Printf("[Extension] secret rotation failed; keeping old secret")
		}
	}
	if s.idemp != nil {
		s.idemp.clear()
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
