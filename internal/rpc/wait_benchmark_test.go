package rpc

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

type mockTransport struct {
	deadline time.Time
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if time.Now().Before(m.deadline) {
		return nil, errors.New("connection refused")
	}

	// Return valid JSON response
	json := `{"jsonrpc": "2.0", "result": {"downloadSpeed": "1000"}, "id": "goaria"}`
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(json)),
		Header:     make(http.Header),
	}, nil
}

func BenchmarkWaitForReady(b *testing.B) {
	// Save original client
	originalClient := httpClient
	defer func() { httpClient = originalClient }()

	// Create a client with mock transport
	mock := &mockTransport{}
	httpClient = &http.Client{
		Transport: mock,
		Timeout:   100 * time.Millisecond,
	}

	// Setup Init to set currentURL (doesn't matter what it is since we mock Transport)
	Init("6800", "secret")

	// Delay for readiness
	delay := 200 * time.Millisecond

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		mock.deadline = time.Now().Add(delay)

		err := WaitForReady(2 * time.Second)
		if err != nil {
			b.Fatalf("WaitForReady failed: %v", err)
		}
	}
}
