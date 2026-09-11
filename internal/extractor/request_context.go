package extractor

import (
	"context"
	"sync/atomic"
)

type extractorContextKey int

const (
	browserContextKey extractorContextKey = iota
	lastHTTPFetchStatusContextKey
)

// BrowserRequestContext carries the browser-derived, request-scoped inputs
// for one resolve: cookies, typed UA/language/referer fields, and validated
// header grants. It lives only on the request context; nothing here is
// persisted or echoed back to the caller. Once attached, treat it as
// immutable — ctx values are shared by all readers on the chain.
type BrowserRequestContext struct {
	Cookies        []SessionCookie
	UserAgent      string
	AcceptLanguage string
	RefererOrigin  string
	Grants         []BrowserHeaderGrant
}

// WithBrowserContext copies bc onto ctx for this request only and installs
// the last-status slot, matching WithBrowserCookies semantics.
func WithBrowserContext(ctx context.Context, bc BrowserRequestContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	bc.Cookies = append([]SessionCookie(nil), bc.Cookies...)
	if bc.Grants != nil {
		grants := make([]BrowserHeaderGrant, len(bc.Grants))
		for i, grant := range bc.Grants {
			grants[i] = grant
			grants[i].Headers = append([]BrowserHeader(nil), grant.Headers...)
		}
		bc.Grants = grants
	}
	ctx = context.WithValue(ctx, browserContextKey, bc)

	return withLastHTTPFetchStatusSlot(ctx)
}

// WithBrowserCookies copies cookies onto ctx for this request only.
func WithBrowserCookies(ctx context.Context, cookies []SessionCookie) context.Context {
	return WithBrowserContext(ctx, BrowserRequestContext{Cookies: cookies})
}

// LastHTTPFetchStatus returns the final non-redirect fetch status for ctx, or 0.
func LastHTTPFetchStatus(ctx context.Context) int {
	if slot := lastHTTPFetchStatusSlot(ctx); slot != nil {
		return int(slot.Load())
	}

	return 0
}

// browserContextFromContext returns the stored context value. The returned
// struct shares slice backings with the stored copy: treat it as immutable.
// Producers copy at attach time (WithBrowserContext); consumers must not
// mutate.
func browserContextFromContext(ctx context.Context) BrowserRequestContext {
	if ctx == nil {
		return BrowserRequestContext{}
	}
	bc, _ := ctx.Value(browserContextKey).(BrowserRequestContext)

	return bc
}

func browserCookiesFromContext(ctx context.Context) []SessionCookie {
	return browserContextFromContext(ctx).Cookies
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
