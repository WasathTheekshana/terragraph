package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// GitHubOIDC fetches GitHub Actions ID tokens for the server to verify, so a
// workflow needs no stored secret. The job must have "id-token: write".
type GitHubOIDC struct {
	Audience   string
	RequestURL string
	// RequestToken authorizes the request to GitHub; it is not sent to TerraGraph.
	RequestToken string
	HTTP         *http.Client
	Now          func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// GitHubOIDCFromEnv reads the variables GitHub Actions sets for jobs allowed
// to request ID tokens.
func GitHubOIDCFromEnv(audience string) (*GitHubOIDC, error) {
	reqURL, reqToken := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"), os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if reqURL == "" || reqToken == "" {
		return nil, errors.New("GitHub Actions ID tokens aren't available: run inside GitHub Actions with \"permissions: id-token: write\"")
	}
	return &GitHubOIDC{
		Audience:     audience,
		RequestURL:   reqURL,
		RequestToken: reqToken,
		HTTP:         &http.Client{Timeout: 30 * time.Second},
		Now:          time.Now,
	}, nil
}

// Token returns a cached ID token, fetching a new one shortly before it expires.
func (g *GitHubOIDC) Token(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token != "" && g.Now().Before(g.expires.Add(-time.Minute)) {
		return g.token, nil
	}

	u, err := url.Parse(g.RequestURL)
	if err != nil {
		return "", fmt.Errorf("parsing ACTIONS_ID_TOKEN_REQUEST_URL: %w", err)
	}
	q := u.Query()
	q.Set("audience", g.Audience)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+g.RequestToken)
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Value == "" {
		return "", errors.New("GitHub returned no ID token")
	}

	g.token = out.Value
	g.expires = expiry(out.Value, g.Now())
	return g.token, nil
}

// expiry reads a JWT's exp claim without verifying it; the server verifies
// the token. Unreadable tokens are treated as short lived.
func expiry(jwt string, now time.Time) time.Time {
	parts := strings.Split(jwt, ".")
	if len(parts) == 3 {
		if payload, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			var c struct {
				Exp int64 `json:"exp"`
			}
			if json.Unmarshal(payload, &c) == nil && c.Exp > 0 {
				return time.Unix(c.Exp, 0)
			}
		}
	}
	return now.Add(2 * time.Minute)
}
