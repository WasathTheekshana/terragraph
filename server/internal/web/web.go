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

	"github.com/a-h/templ"

	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

//go:embed static
var staticFiles embed.FS

// cssURL carries a hash of the stylesheet so it can be cached forever and
// still change on deploy.
var cssURL = func() templ.SafeURL {
	css, err := staticFiles.ReadFile("static/app.css")
	if err != nil {
		panic("web: static/app.css is missing; run make generate")
	}
	sum := sha256.Sum256(css)
	return templ.SafeURL("/static/app.css?v=" + hex.EncodeToString(sum[:6]))
}()

// Pages load only the bundled stylesheet and images; no scripts at all.
const csp = "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

type Store interface {
	ListProjects(ctx context.Context) ([]store.Project, error)
	GetProject(ctx context.Context, id int64) (store.Project, error)
	ProjectUsages(ctx context.Context, projectID int64) ([]store.Usage, error)
	ListModules(ctx context.Context) ([]store.Module, error)
	GetModule(ctx context.Context, id int64) (store.Module, error)
	ModuleConsumers(ctx context.Context, moduleID int64) ([]store.Usage, error)
}

type handler struct {
	store Store
	log   *slog.Logger
}

func NewHandler(s Store, log *slog.Logger) http.Handler {
	h := &handler{store: s, log: log}

	mux := http.NewServeMux()
	mux.Handle("GET /static/", staticHandler())
	mux.HandleFunc("GET /{$}", h.projects)
	mux.HandleFunc("GET /projects/{id}", h.project)
	mux.HandleFunc("GET /modules", h.modules)
	mux.HandleFunc("GET /modules/{id}", h.module)
	mux.HandleFunc("/", h.notFound)

	return securityHeaders(mux)
}

func (h *handler) projects(w http.ResponseWriter, r *http.Request) {
	all, err := h.store.ListProjects(r.Context())
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	q := parseListQuery(r.URL.Query(), projectSorts)
	h.render(w, r, http.StatusOK, projectsPage(all, filterSortProjects(all, q), q))
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
	usages, err := h.store.ProjectUsages(r.Context(), id)
	if err != nil {
		h.lookupError(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, projectPage(p, usages))
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
	h.render(w, r, http.StatusOK, modulePage(m, consumers))
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
