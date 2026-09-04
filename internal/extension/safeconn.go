package extension

import (
	"slices"
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
	grantedCaps  []string
	linkage      Linkage
	extractorGen uint64
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

func (c *safeConn) setGrantedCaps(caps []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.grantedCaps = append([]string(nil), caps...)
}

func (c *safeConn) hasGranted(cap string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Contains(c.grantedCaps, cap)
}

func (c *safeConn) setLinkageSnapshot(l Linkage, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.linkage = l
	c.extractorGen = gen
}

func (c *safeConn) snapshotLinkage() (Linkage, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.linkage, c.extractorGen
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
