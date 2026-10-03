// Package web serves the TerraGraph web UI: server-rendered pages built with
// templ and styled with Tailwind.
//
//go:generate go tool templ generate
package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/WasathTheekshana/terragraph/server/internal/auth"
	"github.com/WasathTheekshana/terragraph/server/internal/graph"
	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

//go:embed static
var staticFiles embed.FS

// assetURL carries a hash of a static file so it can be cached forever and
// still change on deploy.
func assetURL(name string) templ.SafeURL {
	data, err := staticFiles.ReadFile("static/" + name)
	if err != nil {
		panic("web: static/" + name + " is missing; run make generate")
	}
	sum := sha256.Sum256(data)
	return templ.SafeURL("/static/" + name + "?v=" + hex.EncodeToString(sum[:6]))
}

var (
	cssURL      = assetURL("app.css")
	graphCSSURL = assetURL("graph.css")
	graphJSURL  = assetURL("graph.js")
)

// Pages load only the bundled stylesheet, images, and the graph's script, which fetches its data
// from this server.
const csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

type Store interface {
	graph.Source
	GetRepo(ctx context.Context, id int64) (store.Repo, error)
	RepoProjects(ctx context.Context, repoID int64) ([]store.Project, error)
	GetProject(ctx context.Context, id int64) (store.Project, error)
	ProjectUsages(ctx context.Context, projectID int64) ([]store.Usage, error)
	GetModule(ctx context.Context, id int64) (store.Module, error)
	ModuleConsumers(ctx context.Context, moduleID int64) ([]store.Usage, error)
	ModuleDependencies(ctx context.Context, moduleID int64) ([]store.Usage, error)
	ListRuns(ctx context.Context, limit int) ([]store.Run, error)
	GetRun(ctx context.Context, id int64) (store.Run, error)
	RunItems(ctx context.Context, runID int64) ([]store.RunItem, error)
	ListAPITokens(ctx context.Context) ([]store.APIToken, error)
	CreateAPIToken(ctx context.Context, t store.NewAPIToken) (store.APIToken, error)
	RevokeAPIToken(ctx context.Context, id int64, now time.Time) error
}

// Authenticator signs people in and guards pages.
type Authenticator interface {
	RequireUI(next http.Handler) http.Handler
	Login(w http.ResponseWriter, r *http.Request)
	Callback(w http.ResponseWriter, r *http.Request)
	Logout(w http.ResponseWriter, r *http.Request)
	ValidForm(r *http.Request, p auth.Principal) bool
	SetErrorPage(f auth.ErrorPageFunc)
}

const runsShown = 50

type handler struct {
	store Store
	auth  Authenticator
	log   *slog.Logger
	now   func() time.Time
}

func NewHandler(s Store, a Authenticator, log *slog.Logger) http.Handler {
	return newHandler(s, a, log, time.Now)
}

func newHandler(s Store, a Authenticator, log *slog.Logger, now func() time.Time) http.Handler {
	h := &handler{store: s, auth: a, log: log, now: now}
	a.SetErrorPage(func(w http.ResponseWriter, r *http.Request, status int, title, message string) {
		h.render(w, r, status, errorPage(status, title, message))
	})
	page := func(f http.HandlerFunc) http.Handler { return a.RequireUI(f) }

	mux := http.NewServeMux()
	mux.Handle("GET /static/", staticHandler())
	mux.HandleFunc("GET /login", a.Login)
	mux.HandleFunc("GET /auth/callback", a.Callback)
	mux.HandleFunc("POST /logout", a.Logout)
	mux.HandleFunc("GET /signed-out", h.signedOut)
	mux.Handle("GET /{$}", page(h.projects))
	mux.Handle("GET /repos/{id}", page(h.repo))
	mux.Handle("GET /projects/{id}", page(h.project))
	mux.Handle("GET /modules", page(h.modules))
	mux.Handle("GET /modules/{id}", page(h.module))
	mux.Handle("GET /graph", page(h.graphPage))
	mux.Handle("GET /graph/data", page(h.graphData))
	mux.Handle("GET /runs", page(h.runs))
	mux.Handle("GET /runs/{id}", page(h.run))
	mux.Handle("GET /settings/tokens", page(h.tokens))
	mux.Handle("POST /settings/tokens", page(h.createToken))
	mux.Handle("POST /settings/tokens/{id}/revoke", page(h.revokeToken))
	mux.HandleFunc("/", h.notFound)

	return securityHeaders(mux)
}

func (h *handler) signedOut(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, signedOutPage())
}

func (h *handler) projects(w http.ResponseWriter, r *http.Request) {
	repos, err := h.store.ListRepos(r.Context())
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	all, moduleSources := splitRepos(repos)
	q := parseListQuery(r.URL.Query(), repoSorts)
	h.render(w, r, http.StatusOK, projectsPage(all, filterSortRepos(all, q), q, moduleSources))
}

func (h *handler) repo(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		h.notFound(w, r)
		return
	}
	repo, err := h.store.GetRepo(r.Context(), id)
	if err != nil {
		h.lookupError(w, r, err)
		return
	}
	projects, err := h.store.RepoProjects(r.Context(), id)
	if err != nil {
		h.lookupError(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, repoPage(repo, projects))
}

func (h *handler) project(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		h.notFound(w, r)
		return
	}
	p, err := h.store.GetProject(r.Context(), id)
	if err != nil {
		h.lookupError(w, r, err)
		return
	}
	repo, err := h.store.GetRepo(r.Context(), p.RepoID)
	if err != nil {
		h.lookupError(w, r, err)
		return
	}
	usages, err := h.store.ProjectUsages(r.Context(), id)
	if err != nil {
		h.lookupError(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, projectPage(p, repo, usages))
}

func (h *handler) modules(w http.ResponseWriter, r *http.Request) {
	all, err := h.store.ListModules(r.Context())
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	q := parseListQuery(r.URL.Query(), moduleSorts)
	h.render(w, r, http.StatusOK, modulesPage(all, filterSortModules(all, q), q))
}

func (h *handler) module(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		h.notFound(w, r)
		return
	}
	m, err := h.store.GetModule(r.Context(), id)
	if err != nil {
		h.lookupError(w, r, err)
		return
	}
	consumers, err := h.store.ModuleConsumers(r.Context(), id)
	if err != nil {
		h.lookupError(w, r, err)
		return
	}
	var deps []store.Usage
	if m.RepoID != nil {
		if deps, err = h.store.ModuleDependencies(r.Context(), id); err != nil {
			h.lookupError(w, r, err)
			return
		}
	}
	h.render(w, r, http.StatusOK, modulePage(m, consumers, deps))
}

func (h *handler) runs(w http.ResponseWriter, r *http.Request) {
	runs, err := h.store.ListRuns(r.Context(), runsShown)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, runsPage(runs, h.now()))
}

func (h *handler) run(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		h.notFound(w, r)
		return
	}
	run, err := h.store.GetRun(r.Context(), id)
	if err != nil {
		h.lookupError(w, r, err)
		return
	}
	items, err := h.store.RunItems(r.Context(), id)
	if err != nil {
		h.lookupError(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, runPage(run, items, h.now()))
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (h *handler) lookupError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		h.notFound(w, r)
		return
	}
	h.serverError(w, r, err)
}

func (h *handler) notFound(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusNotFound, errorPage(http.StatusNotFound, "Page not found", "There's nothing at this address."))
}

func (h *handler) serverError(w http.ResponseWriter, r *http.Request, err error) {
	h.log.ErrorContext(r.Context(), "rendering page", "error", err, "path", r.URL.Path)
	h.render(w, r, http.StatusInternalServerError, errorPage(http.StatusInternalServerError, "Something went wrong", "The error has been logged."))
}

// render buffers the page so a template error can't leave a half-written response.
func (h *handler) render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	var buf bytes.Buffer
	if err := c.Render(r.Context(), &buf); err != nil {
		h.log.ErrorContext(r.Context(), "rendering template", "error", err, "path", r.URL.Path)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	files := http.StripPrefix("/static/", http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Has("v") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
