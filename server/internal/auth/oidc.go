package auth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	loginCookie  = "tg_login"
	loginTimeout = 10 * time.Minute
	callbackPath = "/auth/callback"
)

// lazyProvider discovers an OIDC issuer on first use, so the server starts
// even while the identity provider is briefly unreachable.
type lazyProvider struct {
	issuer string
	mu     sync.Mutex
	p      *oidc.Provider
}

func (l *lazyProvider) get(ctx context.Context) (*oidc.Provider, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.p != nil {
		return l.p, nil
	}
	// The provider keeps this context for fetching signing keys later, so it
	// mustn't end with the request; the client's timeout bounds each call.
	ctx = oidc.ClientContext(context.WithoutCancel(ctx), &http.Client{Timeout: 10 * time.Second})
	p, err := oidc.NewProvider(ctx, l.issuer)
	if err != nil {
		return nil, fmt.Errorf("discovering %s: %w", l.issuer, err)
	}
	l.p = p
	return p, nil
}

func (a *Authenticator) oauthConfig(p *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     a.cfg.OIDC.ClientID,
		ClientSecret: a.cfg.OIDC.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  a.publicURL.String() + callbackPath,
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
}

// Login sends the browser to the identity provider.
func (a *Authenticator) Login(w http.ResponseWriter, r *http.Request) {
	returnTo := safeReturnTo(r.URL.Query().Get("return_to"))
	if a.cfg.Disabled {
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}
	provider, err := a.oidc.get(r.Context())
	if err != nil {
		a.log.ErrorContext(r.Context(), "sign-in unavailable", "error", err)
		a.errorPage(w, r, http.StatusBadGateway, "Sign-in unavailable", "The identity provider couldn't be reached. Try again shortly.")
		return
	}

	state, nonce, verifier := randomString(), randomString(), oauth2.GenerateVerifier()
	if err := a.store.CreateLoginAttempt(r.Context(), state, verifier, nonce, returnTo, a.now().Add(loginTimeout)); err != nil {
		a.log.ErrorContext(r.Context(), "starting sign-in", "error", err)
		a.errorPage(w, r, http.StatusInternalServerError, "Something went wrong", "The error has been logged.")
		return
	}
	// Binds the attempt to this browser, so a sign-in started elsewhere
	// can't be completed here (login CSRF).
	http.SetCookie(w, &http.Cookie{
		Name: loginCookie, Value: state, Path: callbackPath, MaxAge: int(loginTimeout.Seconds()),
		HttpOnly: true, Secure: a.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, a.oauthConfig(provider).AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)), http.StatusFound)
}

type idClaims struct {
	Email         string `json:"email"`
	EmailVerified *bool  `json:"email_verified"`
	Name          string `json:"name"`
}

// Callback completes sign-in: it checks the attempt, exchanges the code,
// verifies the ID token, applies the access rules, and starts a session.
func (a *Authenticator) Callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		a.errorPage(w, r, http.StatusUnauthorized, "Sign-in failed", fmt.Sprintf("The identity provider said: %s %s", e, q.Get("error_description")))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Path: callbackPath, MaxAge: -1, HttpOnly: true, Secure: a.secureCookies()})

	state := q.Get("state")
	c, err := r.Cookie(loginCookie)
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		a.errorPage(w, r, http.StatusBadRequest, "Sign-in expired", "This sign-in didn't start in this browser, or took too long. Please sign in again.")
		return
	}
	verifier, nonce, returnTo, err := a.store.TakeLoginAttempt(r.Context(), state, a.now())
	if err != nil {
		a.errorPage(w, r, http.StatusBadRequest, "Sign-in expired", "This sign-in has already been used or took too long. Please sign in again.")
		return
	}

	provider, err := a.oidc.get(r.Context())
	if err != nil {
		a.log.ErrorContext(r.Context(), "sign-in unavailable", "error", err)
		a.errorPage(w, r, http.StatusBadGateway, "Sign-in unavailable", "The identity provider couldn't be reached. Try again shortly.")
		return
	}
	tok, err := a.oauthConfig(provider).Exchange(r.Context(), q.Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		a.log.WarnContext(r.Context(), "exchanging sign-in code", "error", err)
		a.errorPage(w, r, http.StatusUnauthorized, "Sign-in failed", "The identity provider didn't accept the sign-in. Please try again.")
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	idToken, err := provider.Verifier(&oidc.Config{ClientID: a.cfg.OIDC.ClientID}).Verify(r.Context(), rawID)
	if err != nil || subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(nonce)) != 1 {
		a.log.WarnContext(r.Context(), "invalid id token", "error", err)
		a.errorPage(w, r, http.StatusUnauthorized, "Sign-in failed", "The identity provider's response couldn't be verified.")
		return
	}

	var claims idClaims
	var all map[string]any
	if err := idToken.Claims(&claims); err != nil || idToken.Claims(&all) != nil {
		a.errorPage(w, r, http.StatusUnauthorized, "Sign-in failed", "The identity provider's response couldn't be read.")
		return
	}
	email := strings.ToLower(claims.Email)
	if email == "" || (claims.EmailVerified != nil && !*claims.EmailVerified) {
		a.errorPage(w, r, http.StatusForbidden, "Not allowed", "Your account has no verified email address.")
		return
	}
	if !a.domainAllowed(email) {
		a.log.InfoContext(r.Context(), "sign-in from a domain that isn't allowed", "email", email)
		a.errorPage(w, r, http.StatusForbidden, "Not allowed", fmt.Sprintf("%s isn't allowed to use terragraph.", email))
		return
	}

	user, err := a.store.UpsertUser(r.Context(), idToken.Issuer+"|"+idToken.Subject, email, claims.Name)
	if err != nil {
		a.log.ErrorContext(r.Context(), "recording user", "error", err)
		a.errorPage(w, r, http.StatusInternalServerError, "Something went wrong", "The error has been logged.")
		return
	}
	sessionID := randomString()
	admin := a.isAdmin(email, all)
	if err := a.store.CreateSession(r.Context(), HashToken(sessionID), user.ID, admin, randomString(), a.now().Add(a.cfg.SessionTTL)); err != nil {
		a.log.ErrorContext(r.Context(), "creating session", "error", err)
		a.errorPage(w, r, http.StatusInternalServerError, "Something went wrong", "The error has been logged.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: sessionID, Path: "/", MaxAge: int(a.cfg.SessionTTL.Seconds()),
		HttpOnly: true, Secure: a.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
	a.log.InfoContext(r.Context(), "signed in", "email", email, "admin", admin)
	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}

// Logout ends the session. It's a form post with a CSRF token, so another
// site can't sign people out.
func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) {
	p, ok := a.Authenticate(r)
	if ok && p.Kind == KindUser {
		if !a.ValidForm(r, p) {
			a.errorPage(w, r, http.StatusForbidden, "Not allowed", "The sign-out form expired. Go back and try again.")
			return
		}
		if c, err := r.Cookie(sessionCookie); err == nil {
			if err := a.store.DeleteSession(r.Context(), HashToken(c.Value)); err != nil {
				a.log.ErrorContext(r.Context(), "deleting session", "error", err)
			}
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.secureCookies()})
	http.Redirect(w, r, "/signed-out", http.StatusSeeOther)
}

func (a *Authenticator) domainAllowed(email string) bool {
	domains := a.cfg.OIDC.AllowedDomains
	if len(domains) == 0 {
		return true
	}
	_, domain, _ := strings.Cut(email, "@")
	return slices.Contains(domains, domain)
}

func (a *Authenticator) isAdmin(email string, claims map[string]any) bool {
	if slices.Contains(a.cfg.AdminEmails, email) {
		return true
	}
	group := a.cfg.OIDC.AdminGroup
	if group == "" {
		return false
	}
	groups, _ := claims[a.cfg.OIDC.GroupsClaim].([]any)
	for _, g := range groups {
		if s, ok := g.(string); ok && s == group {
			return true
		}
	}
	return false
}

// secureCookies is off only for plain-http development servers.
func (a *Authenticator) secureCookies() bool {
	return a.publicURL == nil || a.publicURL.Scheme == "https"
}

// safeReturnTo keeps post-sign-in redirects on this server.
func safeReturnTo(s string) string {
	if !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.HasPrefix(s, "/\\") {
		return "/"
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" || strings.HasPrefix(u.Path, "/login") || strings.HasPrefix(u.Path, callbackPath) {
		return "/"
	}
	return s
}
