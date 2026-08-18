package extension

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func startSafeConnPair(t *testing.T) (serverConn *safeConn, clientConn *websocket.Conn, cleanup func()) {
	t.Helper()
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	ready := make(chan *safeConn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		sc := newSafeConn(c)
		ready <- sc
		// Keep the handler alive until the test closes the client.
		_, _, _ = c.ReadMessage()
	}))
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	client, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		srv.Close()
		t.Fatalf("dial: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}
	select {
	case sc := <-ready:
		cleanup = func() {
			_ = sc.Close()
			_ = client.Close()
			srv.Close()
		}
		return sc, client, cleanup
	case <-time.After(2 * time.Second):
		_ = client.Close()
		srv.Close()
		t.Fatal("timed out waiting for upgrade")
	}
	return nil, nil, func() {}
}

func TestSafeConn_ConcurrentWrites(t *testing.T) {
	sc, client, cleanup := startSafeConnPair(t)
	defer cleanup()

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			errCh <- sc.writeJSON(map[string]any{"type": "probe", "n": n})
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("writeJSON: %v", err)
		}
	}

	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	for i := 0; i < 2; i++ {
		_, _, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
}

func TestSafeConn_DoubleClose(t *testing.T) {
	sc, _, cleanup := startSafeConnPair(t)
	defer cleanup()
	if err := sc.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := sc.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
