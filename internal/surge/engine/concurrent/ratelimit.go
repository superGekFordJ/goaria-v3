package concurrent

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrRateLimited = errors.New("rate limited")

type rateLimitError struct {
	retryAfter time.Duration
	explicit   bool
}

// FORK-PATCH: include "429/503" in the string so isConnLimitError
// matches it for post-loop IncrConnErrors capacity shrinkage.
func (e *rateLimitError) Error() string {
	if e.explicit && e.retryAfter > 0 {
		return fmt.Sprintf("rate limited (429/503): retry-after %v", e.retryAfter)
	}
	return "rate limited (429/503)"
}

func (e *rateLimitError) Unwrap() error {
	return ErrRateLimited
}

func interruptibleSleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	select {
	case <-ctx.Done():
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		return false
	case <-timer.C:
		return true
	}
}
