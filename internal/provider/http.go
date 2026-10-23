package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type HTTPError struct {
	Service    string
	StatusCode int
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string {
	if e.StatusCode == http.StatusTooManyRequests {
		return e.Service + ": rate limit reached (HTTP 429)"
	}
	return fmt.Sprintf("%s: HTTP %d %s", e.Service, e.StatusCode, http.StatusText(e.StatusCode))
}

func Retryable(err error) bool {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == 429 || httpErr.StatusCode == 408 || httpErr.StatusCode >= 500
	}
	// Transport failures and invalid responses can be transient too.
	return !errors.Is(err, context.Canceled)
}

func RetryAfter(err error) time.Duration {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.RetryAfter
	}
	return 0
}

func getJSON(ctx context.Context, client *http.Client, endpoint, service string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "aerolatch-cli/0.1")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", service, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &HTTPError{Service: service, StatusCode: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	}
	// A targeted lookup should be small. Bound responses, including malformed ones.
	const limit = 2 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("%s: read response: %w", service, err)
	}
	if len(body) > limit {
		return fmt.Errorf("%s: response exceeds 2 MiB", service)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("%s: invalid JSON response: %w", service, err)
	}
	return nil
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}
