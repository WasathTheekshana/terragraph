package web

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/WasathTheekshana/terragraph/server/internal/auth"
	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

const (
	maxTokenName     = 100
	maxRepoPatterns  = 50
	defaultTokenDays = "90"
)

type expiryOption struct {
	Value, Label string
	Days         int
}

var expiryOptions = []expiryOption{
	{"30", "30 days", 30},
	{"90", "90 days", 90},
	{"365", "1 year", 365},
	{"never", "Never", 0},
}

// tokenForm is the create-token form's input, kept to re-show it with errors.
type tokenForm struct {
	Name     string
	Read     bool
	Ingest   bool
	Repos    string
	Expiry   string
	Problems []string
}

func newTokenForm() tokenForm {
	return tokenForm{Ingest: true, Expiry: defaultTokenDays}
}

// validate returns the repo patterns and expiry the form asks for.
func (f *tokenForm) validate(now time.Time) ([]string, *time.Time) {
	if f.Name == "" || len(f.Name) > maxTokenName {
		f.Problems = append(f.Problems, fmt.Sprintf("Give the token a name of up to %d characters.", maxTokenName))
	}
	if !f.Read && !f.Ingest {
		f.Problems = append(f.Problems, "Choose what the token may do.")
	}
	var patterns []string
	for line := range strings.SplitSeq(f.Repos, "\n") {
		if line = strings.ToLower(strings.TrimSpace(line)); line == "" {
			continue
		}
		if _, err := path.Match(line, ""); err != nil {
			f.Problems = append(f.Problems, fmt.Sprintf("%q isn't a valid pattern.", line))
			continue
		}
		patterns = append(patterns, line)
	}
	if len(patterns) > maxRepoPatterns {
		f.Problems = append(f.Problems, fmt.Sprintf("Use at most %d repository patterns.", maxRepoPatterns))
	}
	i := slices.IndexFunc(expiryOptions, func(o expiryOption) bool { return o.Value == f.Expiry })
	if i < 0 {
		f.Problems = append(f.Problems, "Choose when the token expires.")
		return patterns, nil
	}
	if days := expiryOptions[i].Days; days > 0 {
		at := now.Add(time.Duration(days) * 24 * time.Hour)
		return patterns, &at
	}
	return patterns, nil
}

func (h *handler) tokens(w http.ResponseWriter, r *http.Request) {
	p, ok := h.admin(w, r)
	if !ok {
		return
	}
	h.renderTokens(w, r, http.StatusOK, p, newTokenForm(), "")
}

func (h *handler) createToken(w http.ResponseWriter, r *http.Request) {
	p, ok := h.admin(w, r)
	if !ok || !h.validForm(w, r, p) {
		return
	}
	form := tokenForm{
		Name:   strings.TrimSpace(r.PostFormValue("name")),
		Read:   r.PostFormValue("read") == "on",
		Ingest: r.PostFormValue("ingest") == "on",
		Repos:  r.PostFormValue("repos"),
		Expiry: r.PostFormValue("expiry"),
	}
	patterns, expires := form.validate(h.now())
	if len(form.Problems) > 0 {
		h.renderTokens(w, r, http.StatusUnprocessableEntity, p, form, "")
		return
	}

	token, hash, prefix := auth.NewAPIToken()
	var createdBy *int64
	if p.Kind == auth.KindUser {
		createdBy = &p.UserID
	}
	_, err := h.store.CreateAPIToken(r.Context(), store.NewAPIToken{
		Name: form.Name, Hash: hash, Prefix: prefix, CanRead: form.Read, CanIngest: form.Ingest,
		RepoPatterns: patterns, CreatedBy: createdBy, ExpiresAt: expires,
	})
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	h.log.InfoContext(r.Context(), "api token created", "name", form.Name, "by", p.Name, "read", form.Read, "ingest", form.Ingest)
	h.renderTokens(w, r, http.StatusCreated, p, newTokenForm(), token)
}

func (h *handler) revokeToken(w http.ResponseWriter, r *http.Request) {
	p, ok := h.admin(w, r)
	if !ok || !h.validForm(w, r, p) {
		return
	}
	id, ok := pathID(r)
	if !ok {
		h.notFound(w, r)
		return
	}
	if err := h.store.RevokeAPIToken(r.Context(), id, h.now()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.notFound(w, r)
			return
		}
		h.serverError(w, r, err)
		return
	}
	h.log.InfoContext(r.Context(), "api token revoked", "id", id, "by", p.Name)
	http.Redirect(w, r, "/settings/tokens", http.StatusSeeOther)
}

// renderTokens shows the tokens page; created is a just-made token, shown
// this once and kept out of every cache.
func (h *handler) renderTokens(w http.ResponseWriter, r *http.Request, status int, p auth.Principal, form tokenForm, created string) {
	tokens, err := h.store.ListAPITokens(r.Context())
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	if created != "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	h.render(w, r, status, tokensPage(tokens, form, created, p.CSRFToken, h.now()))
}

func (h *handler) admin(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, _ := auth.FromContext(r.Context())
	if !p.Admin {
		h.render(w, r, http.StatusForbidden, errorPage(http.StatusForbidden, "Admins only", "Ask a terragraph admin to manage API tokens."))
		return p, false
	}
	return p, true
}

func (h *handler) validForm(w http.ResponseWriter, r *http.Request, p auth.Principal) bool {
	if !h.auth.ValidForm(r, p) {
		h.render(w, r, http.StatusForbidden, errorPage(http.StatusForbidden, "Form expired", "Go back, reload the page, and try again."))
		return false
	}
	return true
}
