package extractor

import (
	"context"
	"sync/atomic"
)

type extractorContextKey int

const (
	browserCookiesContextKey extractorContextKey = iota
	lastHTTPFetchStatusContextKey
)

// WithBrowserCookies copies cookies onto ctx for this request only.
func WithBrowserCookies(ctx context.Context, cookies []SessionCookie) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	copied := append([]SessionCookie(nil), cookies...)
	ctx = context.WithValue(ctx, browserCookiesContextKey, copied)
	return withLastHTTPFetchStatusSlot(ctx)
}

// LastHTTPFetchStatus returns the final non-redirect fetch status for ctx, or 0.
func LastHTTPFetchStatus(ctx context.Context) int {
	slot := lastHTTPFetchStatusSlot(ctx)
	if slot == nil {
		return 0
	}

	return int(slot.Load())
}

func browserCookiesFromContext(ctx context.Context) []SessionCookie {
	if ctx == nil {
		return nil
	}
	cookies, _ := ctx.Value(browserCookiesContextKey).([]SessionCookie)

	return cookies
}

func withLastHTTPFetchStatusSlot(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if lastHTTPFetchStatusSlot(ctx) != nil {
		return ctx
	}

	return context.WithValue(ctx, lastHTTPFetchStatusContextKey, &atomic.Int32{})
}

func lastHTTPFetchStatusSlot(ctx context.Context) *atomic.Int32 {
	if ctx == nil {
		return nil
	}
	slot, _ := ctx.Value(lastHTTPFetchStatusContextKey).(*atomic.Int32)

	return slot
}

func resetLastHTTPFetchStatus(ctx context.Context) {
	if slot := lastHTTPFetchStatusSlot(ctx); slot != nil {
		slot.Store(0)
	}
}

func recordLastHTTPFetchStatus(ctx context.Context, statusCode int) {
	if slot := lastHTTPFetchStatusSlot(ctx); slot != nil {
		slot.Store(int32(statusCode))
	}
}
