// Package client talks to the TerraGraph server's run API. Every call is
// safe to repeat, so transient failures are retried with backoff.
package client

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/WasathTheekshana/terragraph/scanner/internal/report"
)

const (
	ItemKindProject    = "project"
	ItemKindModuleRepo = "module_repo"

	RunFinished  = "finished"
	RunCancelled = "cancelled"
)

type Client struct {
	BaseURL     string
	Token       string
	HTTP        *http.Client
	MaxAttempts int
	// BaseDelay is the first retry's delay; each retry doubles it.
	BaseDelay time.Duration
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL:     baseURL,
		Token:       token,
		HTTP:        &http.Client{Timeout: 60 * time.Second},
		MaxAttempts: 6,
		BaseDelay:   500 * time.Millisecond,
	}
}

// APIError is a response the server rejected; retrying it won't help.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("server returned %d: %s", e.Status, e.Message)
}

type ItemSpec struct {
	Kind    string `json:"kind"`
	RepoURL string `json:"repo_url"`
	Path    string `json:"path"`
}

type Item struct {
	ID int64 `json:"id"`
	ItemSpec
}

type Run struct {
	ID    int64
	Items []Item
}

type SubmitResult struct {
	ScanID    int64 `json:"scan_id"`
	Applied   bool  `json:"applied"`
	Duplicate bool  `json:"duplicate"`
}

// CreateRun registers a run and the items it will scan.
func (c *Client) CreateRun(ctx context.Context, label string, items []ItemSpec) (Run, error) {
	var out struct {
		Run struct {
			ID int64 `json:"id"`
		} `json:"run"`
		Items []Item `json:"items"`
	}
	body := map[string]any{"label": label, "items": items}
	if err := c.do(ctx, http.MethodPost, "/api/v1/runs", newIdempotencyKey(), body, &out); err != nil {
		return Run{}, fmt.Errorf("creating run: %w", err)
	}
	return Run{ID: out.Run.ID, Items: out.Items}, nil
}

func (c *Client) SubmitItem(ctx context.Context, runID, itemID int64, r report.ScanReport) (SubmitResult, error) {
	var out SubmitResult
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/runs/%d/items/%d/scan", runID, itemID), "", r, &out)
	return out, err
}

func (c *Client) FailItem(ctx context.Context, runID, itemID int64, msg string) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/runs/%d/items/%d/fail", runID, itemID), "",
		map[string]string{"error": msg}, nil)
}

func (c *Client) FinishRun(ctx context.Context, runID int64, status string) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/runs/%d/finish", runID), "",
		map[string]string{"status": status}, nil)
}

func (c *Client) do(ctx context.Context, method, path, idempotencyKey string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}

	var lastErr error
	for attempt := 0; attempt < max(c.MaxAttempts, 1); attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, c.backoff(attempt, lastErr)); err != nil {
				return err
			}
		}
		lastErr = c.once(ctx, method, path, idempotencyKey, payload, out)
		if lastErr == nil || !retryable(lastErr) || ctx.Err() != nil {
			return lastErr
		}
	}
	return lastErr
}

func (c *Client) once(ctx context.Context, method, path, idempotencyKey string, payload []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return &transientError{err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &transientError{err: err}
	}

	if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return &transientError{
			err:        &APIError{Status: resp.StatusCode, Message: errorMessage(body)},
			retryAfter: retryAfter(resp.Header.Get("Retry-After")),
		}
	}
	if resp.StatusCode/100 != 2 {
		return &APIError{Status: resp.StatusCode, Message: errorMessage(body)}
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}

// transientError is a failure worth retrying: the server was unreachable or
// temporarily unable to handle the request.
type transientError struct {
	err        error
	retryAfter time.Duration
}

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

func retryable(err error) bool {
	var t *transientError
	return errors.As(err, &t)
}

// backoff doubles from BaseDelay with up to 50% jitter, capped at 30s, unless
// the server said how long to wait.
func (c *Client) backoff(attempt int, lastErr error) time.Duration {
	var t *transientError
	if errors.As(lastErr, &t) && t.retryAfter > 0 {
		return t.retryAfter
	}
	d := c.BaseDelay << (attempt - 1)
	if d <= 0 || d > 30*time.Second {
		d = 30 * time.Second
	}
	return d + time.Duration(rand.Int64N(int64(d)/2+1))
}

func retryAfter(v string) time.Duration {
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return min(time.Duration(secs)*time.Second, time.Minute)
	}
	return 0
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func errorMessage(body []byte) string {
	var e struct {
		Error   string   `json:"error"`
		Details []string `json:"details"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		if len(e.Details) > 0 {
			return fmt.Sprintf("%s: %v", e.Error, e.Details)
		}
		return e.Error
	}
	return string(bytes.TrimSpace(body))
}

func newIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = crand.Read(b)
	return hex.EncodeToString(b)
}
