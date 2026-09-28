// Package auth authenticates people (OIDC sign-in with server-side sessions)
// and machines (API tokens and GitHub Actions OIDC), and authorizes what
// each may do.
package auth

import (
	"context"
	"path"
)

type Kind string

const (
	KindUser   Kind = "user"
	KindToken  Kind = "token"
	KindGitHub Kind = "github"
	// KindOpen is everyone, when sign-in is disabled for local use.
	KindOpen Kind = "open"
)

// Principal is who a request is from and what they may do.
type Principal struct {
	Kind Kind
	// Name is an email, token name, or GitHub repository, for logs and the UI.
	Name      string
	UserID    int64
	Admin     bool
	CanRead   bool
	CanIngest bool
	// RepoPatterns limits which repos (by key) project scans may be
	// submitted for; empty means any.
	RepoPatterns []string
	// Branch is the branch a GitHub Actions run is on, as signed by GitHub.
	// When BranchVerified, it replaces whatever branch a report claims.
	Branch         string
	BranchVerified bool
	// CSRFToken is the token forms must echo back, for sessions and open mode.
	CSRFToken string
}

// MayIngestRepo reports whether p may submit project scans for the repo with
// the given key.
func (p Principal) MayIngestRepo(repoKey string) bool {
	if !p.CanIngest {
		return false
	}
	if len(p.RepoPatterns) == 0 {
		return true
	}
	for _, pattern := range p.RepoPatterns {
		if ok, _ := path.Match(pattern, repoKey); ok {
			return true
		}
	}
	return false
}

type ctxKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the request's principal, set by the middleware.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}
