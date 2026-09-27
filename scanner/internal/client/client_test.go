package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(url string) *Client {
	c := New(url, "tok")
	c.BaseDelay = time.Millisecond
	return c
}

func TestRetriesTransientFailures(t *testing.T) {
	var calls atomic.Int32
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing token")
		}
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"run":   map[string]any{"id": 7},
			"items": []map[string]any{{"id": 1, "kind": "project", "repo_url": "x/y", "path": "."}},
		})
	}))
	defer srv.Close()

	run, err := newTestClient(srv.URL).CreateRun(context.Background(), "label", []ItemSpec{{Kind: ItemKindProject, RepoURL: "x/y", Path: "."}})
	if err != nil {
		t.Fatal(err)
	}
	if run.ID != 7 || len(run.Items) != 1 || run.Items[0].ID != 1 || calls.Load() != 3 {
		t.Errorf("run = %+v after %d calls", run, calls.Load())
	}
	if keys[0] == "" || keys[0] != keys[1] || keys[1] != keys[2] {
		t.Errorf("idempotency keys = %q; want one key reused across retries", keys)
	}
}

func TestDoesNotRetryRejections(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error": "invalid scan report", "details": ["subject.repo_url is required"]}`))
	}))
	defer srv.Close()

	err := newTestClient(srv.URL).FinishRun(context.Background(), 1, RunFinished)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 422 || calls.Load() != 1 {
		t.Fatalf("err = %v after %d calls; want one 422 APIError", err, calls.Load())
	}
	if apiErr.Message != "invalid scan report: [subject.repo_url is required]" {
		t.Errorf("message = %q", apiErr.Message)
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	c.MaxAttempts = 4
	if err := c.FailItem(context.Background(), 1, 2, "x"); err == nil || calls.Load() != 4 {
		t.Errorf("err = %v after %d calls; want failure after 4", err, calls.Load())
	}
}

func TestRetriesUnreachableServer(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	c := newTestClient(url)
	c.MaxAttempts = 2
	err := c.FinishRun(context.Background(), 1, RunFinished)
	if !retryable(err) {
		t.Errorf("connection refused should be retryable, got %v", err)
	}
}

func TestStopsWhenCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	c.BaseDelay = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := c.FinishRun(ctx, 1, RunFinished); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the context's error", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("kept waiting after the context ended")
	}
}

func TestRetryAfter(t *testing.T) {
	if retryAfter("3") != 3*time.Second || retryAfter("") != 0 || retryAfter("soon") != 0 || retryAfter("600") != time.Minute {
		t.Error("retryAfter parsing")
	}
	c := newTestClient("")
	err := &transientError{err: errors.New("x"), retryAfter: 2 * time.Second}
	if c.backoff(1, err) != 2*time.Second {
		t.Error("backoff should honor Retry-After")
	}
	if d := c.backoff(3, errors.New("x")); d < 4*time.Millisecond || d > 6*time.Millisecond {
		t.Errorf("backoff(3) = %v, want 4ms plus up to 50%% jitter", d)
	}
}
