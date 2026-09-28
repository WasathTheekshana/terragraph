package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/WasathTheekshana/terragraph/server/internal/config"
	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

const sessionCookie = "tg_session"

type Store interface {
	UpsertUser(ctx context.Context, subject, email, name string) (store.User, error)
	CreateSession(ctx context.Context, idHash []byte, userID int64, isAdmin bool, csrf string, expiresAt time.Time) error
	SessionByHash(ctx context.Context, idHash []byte, now time.Time) (store.Session, error)
	DeleteSession(ctx context.Context, idHash []byte) error
	CreateLoginAttempt(ctx context.Context, state, verifier, nonce, returnTo string, expiresAt time.Time) error
	TakeLoginAttempt(ctx context.Context, state string, now time.Time) (verifier, nonce, returnTo string, err error)
	APITokenByHash(ctx context.Context, hash []byte, now time.Time) (store.APIToken, error)
	TouchAPIToken(ctx context.Context, id int64, now time.Time) error
}

// ErrorPageFunc renders an error page for failures in the browser sign-in
// flow; the web package supplies it so pages look consistent.
type ErrorPageFunc func(w http.ResponseWriter, r *http.Request, status int, title, message string)

type Authenticator struct {
	cfg         config.Auth
	publicURL   *url.URL
	staticToken []byte
	store       Store
	log         *slog.Logger
	now         func() time.Time
	errorPage   ErrorPageFunc

	oidc   *lazyProvider
	github *lazyProvider

	// openCSRF protects forms when sign-in is disabled and there's no session.
	openCSRF string
}

func New(cfg config.Config, st Store, log *slog.Logger) *Authenticator {
	a := &Authenticator{
		cfg:      cfg.Auth,
		store:    st,
		log:      log,
		now:      time.Now,
		openCSRF: randomString(),
		errorPage: func(w http.ResponseWriter, _ *http.Request, status int, title, message string) {
			http.Error(w, title+": "+message, status)
		},
	}
	if cfg.PublicURL != "" {
		a.publicURL, _ = url.Parse(cfg.PublicURL)
	}
	if cfg.IngestToken != "" {
		a.staticToken = HashToken(cfg.IngestToken)
	}
	if cfg.Auth.OIDC != nil {
		a.oidc = &lazyProvider{issuer: cfg.Auth.OIDC.Issuer}
	}
	if cfg.Auth.GitHubOIDC != nil {
		a.github = &lazyProvider{issuer: githubIssuer}
	}
	return a
}

// SetErrorPage sets how sign-in errors are shown to people.
func (a *Authenticator) SetErrorPage(f ErrorPageFunc) { a.errorPage = f }

// Enabled reports whether sign-in is on; when off, everyone has full access.
func (a *Authenticator) Enabled() bool { return !a.cfg.Disabled }

// Authenticate identifies a request from its bearer token or session cookie.
func (a *Authenticator) Authenticate(r *http.Request) (Principal, bool) {
	if raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && raw != "" {
		return a.bearer(r.Context(), raw)
	}
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		sess, err := a.store.SessionByHash(r.Context(), HashToken(c.Value), a.now())
		if err == nil {
			return Principal{
				Kind: KindUser, Name: sess.User.Email, UserID: sess.User.ID, Admin: sess.IsAdmin,
				CanRead: true, CSRFToken: sess.CSRFToken,
			}, true
		}
		if !errors.Is(err, store.ErrNotFound) {
			a.log.ErrorContext(r.Context(), "looking up session", "error", err)
		}
	}
	if a.cfg.Disabled {
		return Principal{Kind: KindOpen, Name: "everyone (sign-in disabled)", Admin: true, CanRead: true, CanIngest: true, CSRFToken: a.openCSRF}, true
	}
	return Principal{}, false
}

func (a *Authenticator) bearer(ctx context.Context, raw string) (Principal, bool) {
	switch {
	case isAPIToken(raw):
		tok, err := a.store.APITokenByHash(ctx, HashToken(raw), a.now())
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				a.log.ErrorContext(ctx, "looking up api token", "error", err)
			}
			return Principal{}, false
		}
		if err := a.store.TouchAPIToken(ctx, tok.ID, a.now()); err != nil {
			a.log.WarnContext(ctx, "recording api token use", "error", err)
		}
		return Principal{Kind: KindToken, Name: tok.Name, CanRead: tok.CanRead, CanIngest: tok.CanIngest, RepoPatterns: tok.RepoPatterns}, true
	case a.staticToken != nil && subtle.ConstantTimeCompare(HashToken(raw), a.staticToken) == 1:
		return Principal{Kind: KindToken, Name: "TERRAGRAPH_INGEST_TOKEN", CanRead: true, CanIngest: true}, true
	case a.github != nil && strings.Count(raw, ".") == 2:
		p, err := a.verifyGitHub(ctx, raw)
		if err != nil {
			a.log.InfoContext(ctx, "rejected github oidc token", "error", err)
			return Principal{}, false
		}
		return p, true
	}
	return Principal{}, false
}

type Permission int

const (
	Read Permission = iota
	Ingest
)

// RequireAPI lets through API requests whose principal has perm, answering
// others with JSON 401 or 403.
func (a *Authenticator) RequireAPI(perm Permission, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.Authenticate(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="terragraph"`)
			writeJSONError(w, http.StatusUnauthorized, "missing or invalid credentials")
			return
		}
		if (perm == Read && !p.CanRead) || (perm == Ingest && !p.CanIngest) {
			writeJSONError(w, http.StatusForbidden, "these credentials don't allow this")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// RequireUI lets through people who may read, sending everyone else to sign
// in and back to where they were going.
func (a *Authenticator) RequireUI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.Authenticate(r)
		if !ok {
			http.Redirect(w, r, "/login?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		if !p.CanRead {
			a.errorPage(w, r, http.StatusForbidden, "Not allowed", "These credentials can't view terragraph.")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// ValidForm checks a form post against CSRF: the form must echo the
// principal's CSRF token, and a browser-sent Origin must be this server.
func (a *Authenticator) ValidForm(r *http.Request, p Principal) bool {
	if origin := r.Header.Get("Origin"); origin != "" && !a.sameOrigin(r, origin) {
		return false
	}
	got := r.PostFormValue("csrf")
	return p.CSRFToken != "" && subtle.ConstantTimeCompare([]byte(got), []byte(p.CSRFToken)) == 1
}

func (a *Authenticator) sameOrigin(r *http.Request, origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if a.publicURL != nil {
		return u.Scheme == a.publicURL.Scheme && u.Host == a.publicURL.Host
	}
	return u.Host == r.Host
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
