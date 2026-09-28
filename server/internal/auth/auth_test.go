package auth

import (
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WasathTheekshana/terragraph/server/internal/config"
	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

type memStore struct {
	mu       sync.Mutex
	users    map[string]store.User
	sessions map[string]store.Session
	attempts map[string][4]string
	tokens   map[string]store.APIToken
	touched  []int64
}

func newMemStore() *memStore {
	return &memStore{users: map[string]store.User{}, sessions: map[string]store.Session{},
		attempts: map[string][4]string{}, tokens: map[string]store.APIToken{}}
}

func (m *memStore) UpsertUser(_ context.Context, subject, email, name string) (store.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[subject]
	if !ok {
		u.ID = int64(len(m.users) + 1)
	}
	u.Email, u.Name = email, name
	m.users[subject] = u
	return u, nil
}

func (m *memStore) CreateSession(_ context.Context, idHash []byte, userID int64, isAdmin bool, csrf string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var user store.User
	for _, u := range m.users {
		if u.ID == userID {
			user = u
		}
	}
	m.sessions[hex.EncodeToString(idHash)] = store.Session{User: user, IsAdmin: isAdmin, CSRFToken: csrf, ExpiresAt: expiresAt}
	return nil
}

func (m *memStore) SessionByHash(_ context.Context, idHash []byte, now time.Time) (store.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[hex.EncodeToString(idHash)]
	if !ok || !s.ExpiresAt.After(now) {
		return store.Session{}, store.ErrNotFound
	}
	return s, nil
}

func (m *memStore) DeleteSession(_ context.Context, idHash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, hex.EncodeToString(idHash))
	return nil
}

func (m *memStore) CreateLoginAttempt(_ context.Context, state, verifier, nonce, returnTo string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts[state] = [4]string{verifier, nonce, returnTo}
	return nil
}

func (m *memStore) TakeLoginAttempt(_ context.Context, state string, _ time.Time) (string, string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.attempts[state]
	delete(m.attempts, state)
	if !ok {
		return "", "", "", store.ErrNotFound
	}
	return a[0], a[1], a[2], nil
}

func (m *memStore) APITokenByHash(_ context.Context, hash []byte, _ time.Time) (store.APIToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[hex.EncodeToString(hash)]
	if !ok || t.RevokedAt != nil {
		return store.APIToken{}, store.ErrNotFound
	}
	return t, nil
}

func (m *memStore) TouchAPIToken(_ context.Context, id int64, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.touched = append(m.touched, id)
	return nil
}

func (m *memStore) addToken(t store.APIToken) string {
	token, hash, _ := NewAPIToken()
	m.tokens[hex.EncodeToString(hash)] = t
	return token
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func openConfig() config.Config {
	return config.Config{Auth: config.Auth{Disabled: true, SessionTTL: time.Hour}}
}

func ssoConfig(issuer string) config.Config {
	return config.Config{
		PublicURL: "https://tg.acme.io",
		Auth: config.Auth{
			SessionTTL:  time.Hour,
			AdminEmails: []string{"boss@acme.io"},
			OIDC: &config.OIDC{Issuer: issuer, ClientID: "tg", ClientSecret: "s",
				AllowedDomains: []string{"acme.io"}, AdminGroup: "tg-admins", GroupsClaim: "groups"},
		},
	}
}

func bearer(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func TestAPITokens(t *testing.T) {
	st := newMemStore()
	a := New(ssoConfig("https://idp.invalid"), st, quiet)
	token := st.addToken(store.APIToken{ID: 4, Name: "ci", CanIngest: true, RepoPatterns: []string{"github.com/acme/*"}})

	p, ok := a.Authenticate(bearer(token))
	if !ok || p.Kind != KindToken || p.Name != "ci" || p.CanRead || !p.CanIngest || len(st.touched) != 1 {
		t.Fatalf("principal = %+v, %v (touched %v)", p, ok, st.touched)
	}
	if !p.MayIngestRepo("github.com/acme/infra") || p.MayIngestRepo("github.com/other/infra") {
		t.Error("repo scope not applied")
	}
	for _, bad := range []string{"tg_wrong", "", "Bearer"} {
		if _, ok := a.Authenticate(bearer(bad)); ok {
			t.Errorf("token %q authenticated", bad)
		}
	}
	if _, ok := a.Authenticate(httptest.NewRequest(http.MethodGet, "/", nil)); ok {
		t.Error("no credentials authenticated while sign-in is on")
	}
}

func TestStaticToken(t *testing.T) {
	cfg := ssoConfig("https://idp.invalid")
	cfg.IngestToken = "bootstrap-secret"
	a := New(cfg, newMemStore(), quiet)
	if p, ok := a.Authenticate(bearer("bootstrap-secret")); !ok || !p.CanRead || !p.CanIngest || !p.MayIngestRepo("any/repo") {
		t.Errorf("static token = %+v, %v", p, ok)
	}
	if _, ok := a.Authenticate(bearer("bootstrap-secre")); ok {
		t.Error("near-miss static token authenticated")
	}
}

func TestDisabledIsOpen(t *testing.T) {
	a := New(openConfig(), newMemStore(), quiet)
	p, ok := a.Authenticate(httptest.NewRequest(http.MethodGet, "/", nil))
	if !ok || p.Kind != KindOpen || !p.Admin || !p.CanIngest || p.CSRFToken == "" || a.Enabled() {
		t.Errorf("open principal = %+v, %v", p, ok)
	}
}

func TestRequireAPI(t *testing.T) {
	st := newMemStore()
	a := New(ssoConfig("https://idp.invalid"), st, quiet)
	reader := st.addToken(store.APIToken{ID: 1, Name: "reader", CanRead: true})
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, _ := FromContext(r.Context()); p.Name != "reader" {
			t.Error("principal not in context")
		}
		w.WriteHeader(http.StatusTeapot)
	})

	tests := []struct {
		perm Permission
		req  *http.Request
		want int
	}{
		{Read, bearer(reader), http.StatusTeapot},
		{Ingest, bearer(reader), http.StatusForbidden},
		{Read, httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil), http.StatusUnauthorized},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		a.RequireAPI(tt.perm, ok).ServeHTTP(rec, tt.req)
		if rec.Code != tt.want {
			t.Errorf("perm %d: status %d, want %d", tt.perm, rec.Code, tt.want)
		}
		if rec.Code == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
			t.Error("401 without WWW-Authenticate")
		}
	}
}

func TestRequireUIRedirectsToSignIn(t *testing.T) {
	a := New(ssoConfig("https://idp.invalid"), newMemStore(), quiet)
	rec := httptest.NewRecorder()
	a.RequireUI(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/modules?q=vpc", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?return_to=%2Fmodules%3Fq%3Dvpc" {
		t.Errorf("status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
}

// signIn runs the browser sign-in against idp and returns the response
// from the callback.
func signIn(t *testing.T, a *Authenticator, idp *fakeIdP, claims map[string]any, tamper func(state, nonce *string)) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	a.Login(rec, httptest.NewRequest(http.MethodGet, "/login?return_to=/modules", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("login = %d %s", rec.Code, rec.Body)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	q := loc.Query()
	if !strings.HasPrefix(loc.String(), idp.URL+"/authorize") || q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != "https://tg.acme.io/auth/callback" {
		t.Fatalf("authorize redirect = %s", loc)
	}
	state, nonce := q.Get("state"), q.Get("nonce")
	var bound *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == loginCookie {
			bound = c
		}
	}
	if bound == nil || bound.Value != state || !bound.HttpOnly || !bound.Secure {
		t.Fatalf("login cookie = %+v", bound)
	}
	if tamper != nil {
		tamper(&state, &nonce)
	}

	all := map[string]any{"sub": "u-1", "aud": "tg", "nonce": nonce}
	for k, v := range claims {
		all[k] = v
	}
	idp.prepareCode("code-1", q.Get("code_challenge"), all)
	cb := httptest.NewRequest(http.MethodGet, "/auth/callback?code=code-1&state="+url.QueryEscape(state), nil)
	cb.AddCookie(bound)
	rec = httptest.NewRecorder()
	a.Callback(rec, cb)
	return rec
}

func sessionFrom(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	return nil
}

func TestSignIn(t *testing.T) {
	idp := newFakeIdP(t)
	st := newMemStore()
	a := New(ssoConfig(idp.URL), st, quiet)

	rec := signIn(t, a, idp, map[string]any{"email": "Alice@Acme.io", "email_verified": true, "name": "Alice"}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/modules" {
		t.Fatalf("callback = %d %q %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	cookie := sessionFrom(rec)
	if cookie == nil || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie = %+v", cookie)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(cookie)
	p, ok := a.Authenticate(r)
	if !ok || p.Kind != KindUser || p.Name != "alice@acme.io" || p.Admin || !p.CanRead || p.CanIngest {
		t.Errorf("principal = %+v, %v", p, ok)
	}
}

func TestSignInAdmins(t *testing.T) {
	idp := newFakeIdP(t)
	a := New(ssoConfig(idp.URL), newMemStore(), quiet)
	for name, claims := range map[string]map[string]any{
		"by email": {"email": "boss@acme.io"},
		"by group": {"email": "carol@acme.io", "groups": []string{"eng", "tg-admins"}},
	} {
		rec := signIn(t, a, idp, claims, nil)
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if c := sessionFrom(rec); c != nil {
			r.AddCookie(c)
		}
		if p, _ := a.Authenticate(r); !p.Admin {
			t.Errorf("%s: not admin (%d %s)", name, rec.Code, rec.Body)
		}
	}
}

func TestSignInRejects(t *testing.T) {
	idp := newFakeIdP(t)
	tests := []struct {
		name   string
		claims map[string]any
		tamper func(state, nonce *string)
		want   int
	}{
		{"other domain", map[string]any{"email": "mallory@evil.io"}, nil, http.StatusForbidden},
		{"unverified email", map[string]any{"email": "alice@acme.io", "email_verified": false}, nil, http.StatusForbidden},
		{"no email", map[string]any{}, nil, http.StatusForbidden},
		{"wrong nonce", map[string]any{"email": "alice@acme.io"}, func(_, n *string) { *n = "forged" }, http.StatusUnauthorized},
		{"state from another browser", map[string]any{"email": "alice@acme.io"}, func(s, _ *string) { *s = "other" }, http.StatusBadRequest},
		{"wrong audience", map[string]any{"email": "alice@acme.io", "aud": "someone-else"}, nil, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := New(ssoConfig(idp.URL), newMemStore(), quiet)
			rec := signIn(t, a, idp, tt.claims, tt.tamper)
			if rec.Code != tt.want || sessionFrom(rec) != nil {
				t.Errorf("status %d (want %d), session %v", rec.Code, tt.want, sessionFrom(rec))
			}
		})
	}
}

func TestSignInCannotBeReplayed(t *testing.T) {
	idp := newFakeIdP(t)
	a := New(ssoConfig(idp.URL), newMemStore(), quiet)

	rec := httptest.NewRecorder()
	a.Login(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	state := loc.Query().Get("state")
	cookie := rec.Result().Cookies()[0]
	idp.prepareCode("c", loc.Query().Get("code_challenge"), map[string]any{"sub": "u", "aud": "tg", "email": "a@acme.io", "nonce": loc.Query().Get("nonce")})

	for i, want := range []int{http.StatusSeeOther, http.StatusBadRequest} {
		r := httptest.NewRequest(http.MethodGet, "/auth/callback?code=c&state="+state, nil)
		r.AddCookie(cookie)
		rec := httptest.NewRecorder()
		a.Callback(rec, r)
		if rec.Code != want {
			t.Errorf("attempt %d: status %d, want %d", i+1, rec.Code, want)
		}
	}
}

func TestLogout(t *testing.T) {
	idp := newFakeIdP(t)
	st := newMemStore()
	a := New(ssoConfig(idp.URL), st, quiet)
	cookie := sessionFrom(signIn(t, a, idp, map[string]any{"email": "alice@acme.io"}, nil))
	signedIn := httptest.NewRequest(http.MethodGet, "/", nil)
	signedIn.AddCookie(cookie)
	p, _ := a.Authenticate(signedIn)

	post := func(csrf, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(url.Values{"csrf": {csrf}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
		r.AddCookie(cookie)
		rec := httptest.NewRecorder()
		a.Logout(rec, r)
		return rec
	}
	if rec := post("forged", "https://tg.acme.io"); rec.Code != http.StatusForbidden {
		t.Errorf("forged csrf = %d, want 403", rec.Code)
	}
	if rec := post(p.CSRFToken, "https://evil.io"); rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin = %d, want 403", rec.Code)
	}
	if rec := post(p.CSRFToken, "https://tg.acme.io"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/signed-out" {
		t.Errorf("logout = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if _, ok := a.Authenticate(signedIn); ok {
		t.Error("session still valid after sign-out")
	}
}

func TestGitHubOIDC(t *testing.T) {
	idp := newFakeIdP(t)
	cfg := ssoConfig("https://idp.invalid")
	cfg.Auth.GitHubOIDC = &config.GitHubOIDC{Owners: []string{"acme"}, Audience: "terragraph"}
	a := New(cfg, newMemStore(), quiet)
	a.github = &lazyProvider{issuer: idp.URL}

	token := func(claims map[string]any) string {
		all := map[string]any{"sub": "repo:Acme/infra:ref:refs/heads/main", "aud": "terragraph",
			"repository": "Acme/infra", "repository_owner": "Acme", "ref": "refs/heads/main"}
		for k, v := range claims {
			all[k] = v
		}
		return idp.sign(t, all)
	}

	p, ok := a.Authenticate(bearer(token(nil)))
	if !ok || p.Kind != KindGitHub || !p.CanIngest || p.CanRead || p.Branch != "main" || !p.BranchVerified {
		t.Fatalf("principal = %+v, %v", p, ok)
	}
	if !p.MayIngestRepo("github.com/acme/infra") || p.MayIngestRepo("github.com/acme/other") {
		t.Error("a workflow must only submit its own repository")
	}
	if p, _ := a.Authenticate(bearer(token(map[string]any{"ref": "refs/pull/7/merge"}))); p.Branch != "" || !p.BranchVerified {
		t.Errorf("pull request run: branch %q, verified %v", p.Branch, p.BranchVerified)
	}
	for name, claims := range map[string]map[string]any{
		"other owner":      {"repository": "evil/infra", "repository_owner": "evil"},
		"mismatched owner": {"repository": "evil/infra"},
		"wrong audience":   {"aud": "someone-else"},
		"expired":          {"exp": time.Now().Add(-time.Minute).Unix()},
	} {
		if _, ok := a.Authenticate(bearer(token(claims))); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSafeReturnTo(t *testing.T) {
	tests := map[string]string{
		"/modules?q=x":        "/modules?q=x",
		"":                    "/",
		"https://evil.io":     "/",
		"//evil.io":           "/",
		"/\\evil.io":          "/",
		"/login?return_to=/x": "/",
		"/auth/callback":      "/",
	}
	for in, want := range tests {
		if got := safeReturnTo(in); got != want {
			t.Errorf("safeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewAPIToken(t *testing.T) {
	token, hash, prefix := NewAPIToken()
	other, _, _ := NewAPIToken()
	if !strings.HasPrefix(token, "tg_") || len(token) < 40 || token == other || !strings.HasPrefix(token, prefix) || len(prefix) != 11 {
		t.Errorf("token %q prefix %q", token, prefix)
	}
	if string(HashToken(token)) != string(hash) {
		t.Error("hash mismatch")
	}
}
