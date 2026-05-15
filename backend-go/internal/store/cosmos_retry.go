package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
)

// Cosmos throttle handling.
//
// The Azure SDK for Cosmos DB does not retry on 429 (Too Many Requests)
// automatically — that's the caller's job. When Cosmos throttles, the
// response includes an `x-ms-retry-after-ms` header recommending how long
// to wait before the next request. We honor that header, falling back to
// a small default if it's absent or malformed.
//
// retryOnCosmosThrottle wraps a single Cosmos operation with a bounded
// retry loop. It returns the operation's eventual result (success or a
// non-throttle error). On exhausted retries, it wraps the last throttle
// error with attempt count for visibility.

const (
	defaultThrottleBackoff   = 100 * time.Millisecond
	maxThrottleBackoff       = 5 * time.Second
	maxThrottleRetryAttempts = 5
)

func isCosmosThrottled(err error) bool {
	var respErr *azcore.ResponseError
	return errors.As(err, &respErr) && respErr.StatusCode == http.StatusTooManyRequests
}

// cosmosRetryAfter returns the duration Cosmos recommends waiting before
// the next request, or defaultThrottleBackoff if the header is missing or
// malformed. Caller should clamp to maxThrottleBackoff if appropriate.
func cosmosRetryAfter(err error) time.Duration {
	var respErr *azcore.ResponseError
	if !errors.As(err, &respErr) || respErr.RawResponse == nil {
		return defaultThrottleBackoff
	}
	header := respErr.RawResponse.Header.Get("x-ms-retry-after-ms")
	if header == "" {
		return defaultThrottleBackoff
	}
	ms, parseErr := strconv.Atoi(header)
	if parseErr != nil || ms <= 0 {
		return defaultThrottleBackoff
	}
	delay := time.Duration(ms) * time.Millisecond
	if delay > maxThrottleBackoff {
		delay = maxThrottleBackoff
	}
	return delay
}

// retryOnCosmosThrottle invokes op up to maxThrottleRetryAttempts times,
// sleeping for the Cosmos-recommended retry-after duration between
// attempts when op returns a 429. Returns immediately on success or on
// any non-throttle error.
func retryOnCosmosThrottle(ctx context.Context, op func() error) error {
	var lastErr error
	for attempt := 0; attempt < maxThrottleRetryAttempts; attempt++ {
		err := op()
		if err == nil {
			return nil
		}
		if !isCosmosThrottled(err) {
			return err
		}
		lastErr = err
		delay := cosmosRetryAfter(err)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("cosmos throttled after %d retries: %w", maxThrottleRetryAttempts, lastErr)
}
