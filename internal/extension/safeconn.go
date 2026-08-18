package extension

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var wsWriteTimeout = 5 * time.Second

// safeConn serializes Write and Close. Reads stay unlocked (single reader).
type safeConn struct {
	conn     *websocket.Conn
	mu       sync.Mutex
	closed   bool
	inFlight atomic.Int32
	// grantedCaps is the auth_ack snapshot for this connection; never re-read
	// live Ready() or store secret to admit extractor messages.
	grantedCaps []string
}

func newSafeConn(conn *websocket.Conn) *safeConn {
	return &safeConn{conn: conn}
}

func (c *safeConn) writeJSON(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return c.conn.WriteJSON(v)
}

func (c *safeConn) writeRaw(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

func (c *safeConn) tryAcquireInFlight() bool {
	max := int32(perConnInFlightMax)
	for {
		cur := c.inFlight.Load()
		if cur >= max {
			return false
		}
		if c.inFlight.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

func (c *safeConn) releaseInFlight() {
	c.inFlight.Add(-1)
}

func (c *safeConn) hasGranted(cap string) bool {
	for _, x := range c.grantedCaps {
		if x == cap {
			return true
		}
	}
	return false
}

func (c *safeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}

func (c *safeConn) SetReadLimit(limit int64) {
	c.conn.SetReadLimit(limit)
}

func (c *safeConn) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}

func (c *safeConn) ReadJSON(v any) error {
	return c.conn.ReadJSON(v)
}

func (c *safeConn) ReadMessage() (int, []byte, error) {
	return c.conn.ReadMessage()
}
