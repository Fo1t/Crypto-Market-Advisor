// Package httpx provides the shared HTTP client used for every outbound call:
// timeouts, retries with exponential backoff, 429 handling and rate limiting.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// ErrRateLimited is returned when the upstream keeps answering 429.
var ErrRateLimited = errors.New("upstream rate limited")

// StatusError carries a non-2xx response.
type StatusError struct {
	StatusCode int
	Body       string
	URL        string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("http %d for %s: %s", e.StatusCode, e.URL, truncate(e.Body, 300))
}

// Retryable reports whether retrying the request could plausibly help.
func (e *StatusError) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Options configures a Client.
type Options struct {
	Timeout       time.Duration
	MaxRetries    int
	RetryBaseWait time.Duration
	RateLimitRPM  int
	UserAgent     string
	Logger        *slog.Logger
}

// Client is a JSON-oriented HTTP client with retry and rate limiting.
type Client struct {
	http    *http.Client
	opts    Options
	limiter *limiter
	log     *slog.Logger
}

// New builds a Client.
func New(opts Options) *Client {
	if opts.Timeout <= 0 {
		opts.Timeout = 20 * time.Second
	}
	if opts.MaxRetries < 0 {
		opts.MaxRetries = 0
	}
	if opts.RetryBaseWait <= 0 {
		opts.RetryBaseWait = time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Client{
		http:    &http.Client{Timeout: opts.Timeout},
		opts:    opts,
		limiter: newLimiter(opts.RateLimitRPM),
		log:     opts.Logger,
	}
}

// GetJSON performs a GET and decodes the JSON body into out.
func (c *Client) GetJSON(ctx context.Context, url string, headers map[string]string, out any) error {
	body, err := c.Get(ctx, url, headers)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode json from %s: %w", url, err)
	}
	return nil
}

// Get performs a GET with retries and returns the raw body.
func (c *Client) Get(ctx context.Context, url string, headers map[string]string) ([]byte, error) {
	var lastErr error

	for attempt := 0; attempt <= c.opts.MaxRetries; attempt++ {
		if attempt > 0 {
			wait := c.backoff(attempt, lastErr)
			c.log.Debug("retrying request",
				slog.String("url", url),
				slog.Int("attempt", attempt),
				slog.Duration("wait", wait),
			)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}

		if err := c.limiter.wait(ctx); err != nil {
			return nil, err
		}

		body, err := c.do(ctx, url, headers)
		if err == nil {
			return body, nil
		}
		lastErr = err

		var statusErr *StatusError
		if errors.As(err, &statusErr) && !statusErr.Retryable() {
			return nil, err
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
	}

	var statusErr *StatusError
	if errors.As(lastErr, &statusErr) && statusErr.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w: %s", ErrRateLimited, url)
	}
	return nil, fmt.Errorf("request failed after %d attempts: %w", c.opts.MaxRetries+1, lastErr)
}

func (c *Client) do(ctx context.Context, url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.opts.UserAgent != "" {
		req.Header.Set("User-Agent", c.opts.UserAgent)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		statusErr := &StatusError{StatusCode: resp.StatusCode, Body: string(body), URL: url}
		if retryAfter := parseRetryAfter(resp.Header.Get("Retry-After")); retryAfter > 0 {
			c.limiter.pause(retryAfter)
		}
		return nil, statusErr
	}
	return body, nil
}

// backoff grows exponentially with jitter, honouring Retry-After when present.
func (c *Client) backoff(attempt int, lastErr error) time.Duration {
	base := float64(c.opts.RetryBaseWait) * math.Pow(2, float64(attempt-1))

	var statusErr *StatusError
	if errors.As(lastErr, &statusErr) && statusErr.StatusCode == http.StatusTooManyRequests {
		base *= 2
	}
	jitter := 1 + (rand.Float64()-0.5)*0.4 //nolint:gosec // jitter needs no crypto randomness
	wait := time.Duration(base * jitter)

	if wait > 60*time.Second {
		wait = 60 * time.Second
	}
	return wait
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// limiter spaces requests to stay under a requests-per-minute budget.
type limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newLimiter(rpm int) *limiter {
	if rpm <= 0 {
		return &limiter{}
	}
	return &limiter{interval: time.Minute / time.Duration(rpm)}
}

func (l *limiter) wait(ctx context.Context) error {
	if l.interval == 0 {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	wait := l.next.Sub(now)
	l.next = l.next.Add(l.interval)
	l.mu.Unlock()

	if wait <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// pause delays the next allowed request, used after a 429 with Retry-After.
func (l *limiter) pause(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	until := time.Now().Add(d)
	if until.After(l.next) {
		l.next = until
	}
}
