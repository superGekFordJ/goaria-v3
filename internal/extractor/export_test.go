package extractor

import (
	"context"
	"net/http"
)

func LoadRemotePackLockWithClientForTest(ctx context.Context, rawLockURL string, client *http.Client) (RuntimePackCandidate, error) {
	return loadRemotePackLockWithClient(ctx, rawLockURL, client)
}

func NewPrivateIPGuardedTransportForTest(resolver ipResolver, dialer dialContextFunc) *http.Transport {
	return newPrivateIPGuardedTransport(resolver, dialer)
}
