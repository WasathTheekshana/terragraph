package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func fakeJWT(exp time.Time) string {
	payload, _ := json.Marshal(map[string]int64{"exp": exp.Unix()})
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestGitHubOIDCToken(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer req-token" {
			t.Errorf("request token not sent")
		}
		if r.URL.Query().Get("audience") != "terragraph" || r.URL.Query().Get("api-version") != "2.0" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"value": fakeJWT(now.Add(5 * time.Minute))})
	}))
	defer gh.Close()

	g := &GitHubOIDC{Audience: "terragraph", RequestURL: gh.URL + "?api-version=2.0", RequestToken: "req-token", HTTP: gh.Client(), Now: func() time.Time { return now }}

	first, err := g.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second, _ := g.Token(context.Background()); second != first || calls.Load() != 1 {
		t.Errorf("token wasn't cached: %d calls", calls.Load())
	}

	now = now.Add(4*time.Minute + 30*time.Second)
	if _, err := g.Token(context.Background()); err != nil || calls.Load() != 2 {
		t.Errorf("token wasn't refreshed near expiry: %d calls, err %v", calls.Load(), err)
	}
}

func TestGitHubOIDCErrors(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer gh.Close()

	g := &GitHubOIDC{Audience: "terragraph", RequestURL: gh.URL, RequestToken: "x", HTTP: gh.Client(), Now: time.Now}
	if _, err := g.Token(context.Background()); err == nil {
		t.Error("expected an error from a 403")
	}

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")
	if _, err := GitHubOIDCFromEnv("terragraph"); err == nil {
		t.Error("expected an error outside GitHub Actions")
	}
}

func TestClientUsesTokenSource(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	n := 0
	c.TokenSource = func(context.Context) (string, error) {
		n++
		return fmt.Sprintf("jwt-%d", n), nil
	}
	if err := c.FinishRun(context.Background(), 1, RunFinished); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer jwt-1" {
		t.Errorf("Authorization = %q", got)
	}
}
