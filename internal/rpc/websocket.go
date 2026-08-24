package rpc

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"goaria-v3/internal/events"

	"github.com/gorilla/websocket"
)

type Aria2Notification struct {
	Method string `json:"method"`
	Params []struct {
		GID string `json:"gid"`
	} `json:"params"`
}

type Notifier struct {
	hub       *events.Hub
	conn      *websocket.Conn
	url       string
	stopChan  chan struct{}
	connected atomic.Bool

	mu     sync.Mutex
	stopMu sync.Once
}

var notifier *Notifier

func InitNotifier(hub *events.Hub, port, secret string) {
	if notifier != nil {
		StopNotifier()
	}
	url := fmt.Sprintf("ws://127.0.0.1:%s/jsonrpc", port)
	notifier = &Notifier{
		hub:      hub,
		url:      url,
		stopChan: make(chan struct{}),
	}
	go notifier.connectWithRetry()
	_ = secret
}

// IsConnected returns whether the notifier is currently connected to Aria2.
func (n *Notifier) IsConnected() bool {
	if n == nil {
		return false
	}
	return n.connected.Load()
}

// IsAria2Connected returns the global Aria2 websocket connection status.
func IsAria2Connected() bool {
	if notifier == nil {
		return false
	}
	return notifier.IsConnected()
}

func StopNotifier() {
	if notifier == nil {
		return
	}
	notifier.connected.Store(false)
	notifier.stopMu.Do(func() {
		close(notifier.stopChan)
	})
	notifier.mu.Lock()
	if notifier.conn != nil {
		_ = notifier.conn.Close()
		notifier.conn = nil
	}
	notifier.mu.Unlock()
	notifier = nil
}

func (n *Notifier) connectWithRetry() {
	wasConnected := false
	for {
		select {
		case <-n.stopChan:
			return
		default:
		}

		if err := n.connect(); err != nil {
			// Only emit false if this notifier is still the active one
			select {
			case <-n.stopChan:
				return
			default:
				n.hub.EmitConnectionStatus(false)
			}

			log.Printf("[WS] Connection failed: %v, retrying in 3s...", err)
			select {
			case <-n.stopChan:
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}

		n.connected.Store(true)
		n.hub.EmitConnectionStatus(true)
		if wasConnected {
			n.hub.EmitFullSync()
		}
		wasConnected = true

		n.listen()
		n.connected.Store(false)

		// If this notifier was stopped (e.g. port switch), do NOT emit false to overwrite new connection
		select {
		case <-n.stopChan:
			return
		default:
			n.hub.EmitConnectionStatus(false)
		}
	}
}

func (n *Notifier) connect() error {
	n.mu.Lock()
	defer n.mu.Unlock()

	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, resp, err := dialer.Dial(n.url, nil)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return err
	}
	n.conn = conn
	log.Println("[WS] Connected to Aria2")
	return nil
}

func (n *Notifier) listen() {
	defer func() {
		n.mu.Lock()
		if n.conn != nil {
			_ = n.conn.Close()
			n.conn = nil
		}
		n.mu.Unlock()
	}()

	for {
		select {
		case <-n.stopChan:
			return
		default:
		}

		_, msg, err := n.conn.ReadMessage()
		if err != nil {
			log.Printf("[WS] Read error: %v", err)
			return
		}

		var notification Aria2Notification
		if err := json.Unmarshal(msg, &notification); err != nil {
			continue
		}
		if len(notification.Params) == 0 {
			continue
		}

		gid := notification.Params[0].GID
		n.handleNotification(notification.Method, gid)
	}
}

func (n *Notifier) handleNotification(method, gid string) {
	var deltaType string
	switch method {
	case "aria2.onDownloadStart":
		deltaType = "add"
	case "aria2.onDownloadPause":
		deltaType = "pause"
	case "aria2.onDownloadStop":
		deltaType = "remove"
	case "aria2.onDownloadComplete", "aria2.onBtDownloadComplete":
		deltaType = "complete"
	case "aria2.onDownloadError":
		deltaType = "error"
	default:
		return
	}

	log.Printf("[WS] Sensor: %s -> %s (gid: %s)", method, deltaType, gid)
	n.hub.NotifyInternal(events.TaskDelta{Type: deltaType, GID: gid})
}
