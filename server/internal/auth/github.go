package auth

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

const githubIssuer = "https://token.actions.githubusercontent.com"

type githubClaims struct {
	Repository      string `json:"repository"`
	RepositoryOwner string `json:"repository_owner"`
	Ref             string `json:"ref"`
}

// verifyGitHub accepts an ID token GitHub Actions issued to a workflow in
// one of the allowed owners. The workflow may only submit scans for its own
// repository, and its branch is taken from the signed token.
func (a *Authenticator) verifyGitHub(ctx context.Context, raw string) (Principal, error) {
	provider, err := a.github.get(ctx)
	if err != nil {
		return Principal{}, err
	}
	tok, err := provider.Verifier(&oidc.Config{ClientID: a.cfg.GitHubOIDC.Audience}).Verify(ctx, raw)
	if err != nil {
		return Principal{}, err
	}
	var c githubClaims
	if err := tok.Claims(&c); err != nil {
		return Principal{}, err
	}
	owner := strings.ToLower(c.RepositoryOwner)
	if !slices.Contains(a.cfg.GitHubOIDC.Owners, owner) {
		return Principal{}, fmt.Errorf("repository owner %q isn't allowed", c.RepositoryOwner)
	}
	repo := strings.ToLower(c.Repository)
	if !strings.HasPrefix(repo, owner+"/") {
		return Principal{}, fmt.Errorf("repository %q doesn't belong to %q", c.Repository, c.RepositoryOwner)
	}
	branch, isBranch := strings.CutPrefix(c.Ref, "refs/heads/")
	if !isBranch {
		branch = ""
	}
	return Principal{
		Kind:           KindGitHub,
		Name:           c.Repository,
		CanIngest:      true,
		RepoPatterns:   []string{"github.com/" + repo},
		Branch:         branch,
		BranchVerified: true,
	}, nil
}
