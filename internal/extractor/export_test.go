package extractor

import (
	"context"
	"io"
	"net/http"
)

func LoadRemotePackLockWithClientForTest(ctx context.Context, rawLockURL string, client *http.Client) (RuntimePackCandidate, error) {
	return loadRemotePackLockWithClient(ctx, rawLockURL, client)
}

func NewRuntimePackHTTPClientForTest(transport http.RoundTripper) *http.Client {
	return newRuntimePackHTTPClient(transport)
}

func CloneHTTPClientWithRedirectCheckForTest(client *http.Client, sameOrigin bool) *http.Client {
	return cloneHTTPClientWithRedirectCheck(client, sameOrigin)
}

func NewPrivateIPGuardedTransportForTest(resolver ipResolver, dialer dialContextFunc) *http.Transport {
	return newPrivateIPGuardedTransport(resolver, dialer)
}

func ReadLimitedBytesForTest(reader io.Reader, limit int64, label string) ([]byte, error) {
	return readLimitedBytes(reader, limit, label)
}
