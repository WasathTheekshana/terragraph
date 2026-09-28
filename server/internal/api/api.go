// Package api serves the TerraGraph HTTP API.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/WasathTheekshana/terragraph/server/internal/auth"
	"github.com/WasathTheekshana/terragraph/server/internal/report"
	"github.com/WasathTheekshana/terragraph/server/internal/source"
	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

const maxReportBytes = 10 << 20

type Store interface {
	Ingest(ctx context.Context, in store.Scan) (store.IngestResult, error)
	ListProjects(ctx context.Context) ([]store.Project, error)
	GetProject(ctx context.Context, id int64) (store.Project, error)
	ProjectUsages(ctx context.Context, projectID int64) ([]store.Usage, error)
	ListModules(ctx context.Context) ([]store.Module, error)
	GetModule(ctx context.Context, id int64) (store.Module, error)
	ModuleConsumers(ctx context.Context, moduleID int64) ([]store.Usage, error)
	ModuleDependencies(ctx context.Context, moduleID int64) ([]store.Usage, error)
	ListRepos(ctx context.Context) ([]store.Repo, error)
	GetRepo(ctx context.Context, id int64) (store.Repo, error)
	RepoProjects(ctx context.Context, repoID int64) ([]store.Project, error)
	CreateRun(ctx context.Context, label, idempotencyKey string, items []store.NewRunItem) (store.Run, []store.RunItem, error)
	IngestRunItem(ctx context.Context, runID, itemID int64, in store.Scan) (store.IngestResult, error)
	FailRunItem(ctx context.Context, runID, itemID int64, msg string) error
	FinishRun(ctx context.Context, runID int64, status string) (store.Run, error)
	ListRuns(ctx context.Context, limit int) ([]store.Run, error)
	GetRun(ctx context.Context, id int64) (store.Run, error)
	RunItems(ctx context.Context, runID int64) ([]store.RunItem, error)
	Ping(ctx context.Context) error
}

type Config struct {
	// TrackedBranches are the branches whose project scans become the
	// project's current state.
	TrackedBranches []string
	// UI serves every path outside /api/, /healthz, and /readyz. Nil serves
	// the API only.
	UI http.Handler
}

// Authenticator guards routes: it lets through requests whose principal has
// the permission and puts that principal in the request context.
type Authenticator interface {
	RequireAPI(perm auth.Permission, next http.Handler) http.Handler
}

type handler struct {
	store Store
	cfg   Config
	log   *slog.Logger
}

func NewHandler(s Store, a Authenticator, cfg Config, log *slog.Logger) http.Handler {
	h := &handler{store: s, cfg: cfg, log: log}
	read := func(f http.HandlerFunc) http.Handler { return a.RequireAPI(auth.Read, f) }
	ingest := func(f http.HandlerFunc) http.Handler { return a.RequireAPI(auth.Ingest, f) }

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)
	mux.Handle("POST /api/v1/scans", ingest(h.createScan))
	mux.Handle("GET /api/v1/projects", read(h.listProjects))
	mux.Handle("GET /api/v1/projects/{id}", read(h.getProject))
	mux.Handle("GET /api/v1/projects/{id}/usages", read(h.projectUsages))
	mux.Handle("GET /api/v1/modules", read(h.listModules))
	mux.Handle("GET /api/v1/modules/{id}", read(h.getModule))
	mux.Handle("GET /api/v1/modules/{id}/consumers", read(h.moduleConsumers))
	mux.Handle("GET /api/v1/modules/{id}/dependencies", read(h.moduleDependencies))
	mux.Handle("GET /api/v1/repos", read(h.listRepos))
	mux.Handle("GET /api/v1/repos/{id}", read(h.getRepo))
	mux.Handle("POST /api/v1/runs", ingest(h.createRun))
	mux.Handle("GET /api/v1/runs", read(h.listRuns))
	mux.Handle("GET /api/v1/runs/{id}", read(h.getRun))
	mux.Handle("POST /api/v1/runs/{id}/items/{item}/scan", ingest(h.submitRunItem))
	mux.Handle("POST /api/v1/runs/{id}/items/{item}/fail", ingest(h.failRunItem))
	mux.Handle("POST /api/v1/runs/{id}/finish", ingest(h.finishRun))
	// Without this, unknown API paths would fall through to the UI and get HTML.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	if cfg.UI != nil {
		mux.Handle("/", cfg.UI)
	}

	return h.recoverPanics(h.logRequests(mux))
}

func (h *handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.store.Ping(ctx); err != nil {
		h.log.WarnContext(r.Context(), "readiness check failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) createScan(w http.ResponseWriter, r *http.Request) {
	scan, ok := h.readScan(w, r)
	if !ok {
		return
	}
	res, err := h.store.Ingest(r.Context(), scan)
	if err != nil {
		h.internalError(w, r, "ingesting scan", err)
		return
	}
	h.logIngest(r, scan, res)
	writeJSON(w, http.StatusCreated, res)
}

// readJSON decodes a JSON body of at most limit bytes into v, answering the
// request itself when that fails.
func readJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) ([]byte, bool) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return nil, false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body exceeds %d MiB", limit>>20))
			return nil, false
		}
		writeError(w, http.StatusBadRequest, "reading request body failed")
		return nil, false
	}
	if err := json.Unmarshal(raw, v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return nil, false
	}
	return raw, true
}

// readScan reads and validates a scan report from the request body.
func (h *handler) readScan(w http.ResponseWriter, r *http.Request) (store.Scan, bool) {
	var rep report.Report
	raw, ok := readJSON(w, r, maxReportBytes, &rep)
	if !ok {
		return store.Scan{}, false
	}
	if err := rep.Validate(); err != nil {
		writeInvalid(w, "invalid scan report", err)
		return store.Scan{}, false
	}

	p, _ := auth.FromContext(r.Context())
	if rep.ScannerType == report.ScannerTypeModuleUsage && !p.MayIngestRepo(source.RepoKey(rep.Subject.RepoURL)) {
		writeError(w, http.StatusForbidden, fmt.Sprintf("these credentials can't submit scans for %s", rep.Subject.RepoURL))
		return store.Scan{}, false
	}
	// A branch signed by the CI provider beats whatever the report claims.
	if p.BranchVerified {
		rep.Subject.Branch = p.Branch
	}
	return store.Scan{Report: rep, Raw: raw, Tracked: h.tracked(rep.Subject)}, true
}

// tracked reports whether a scan should become its project's current state.
// Folders outside git (identified by a file:// URL) have no branches, so
// only git repos are held to the tracked-branch list.
func (h *handler) tracked(s report.Subject) bool {
	if strings.HasPrefix(strings.ToLower(s.RepoURL), "file://") {
		return true
	}
	return slices.Contains(h.cfg.TrackedBranches, s.Branch)
}

func (h *handler) logIngest(r *http.Request, scan store.Scan, res store.IngestResult) {
	rep := scan.Report
	h.log.InfoContext(r.Context(), "scan ingested",
		"scan_id", res.ScanID, "applied", res.Applied, "duplicate", res.Duplicate, "scanner_type", rep.ScannerType,
		"repo_url", rep.Subject.RepoURL, "path", rep.Subject.ProjectPath(), "branch", rep.Subject.Branch, "facts", len(rep.Facts))
}

func writeInvalid(w http.ResponseWriter, msg string, err error) {
	writeJSON(w, http.StatusUnprocessableEntity, errorBody{Error: msg, Details: strings.Split(err.Error(), "\n")})
}

func (h *handler) listProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := h.store.ListProjects(r.Context())
	if err != nil {
		h.internalError(w, r, "listing projects", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": nonNil(projects)})
}

func (h *handler) getProject(w http.ResponseWriter, r *http.Request) {
	getByID(h, w, r, "project", "project", h.store.GetProject)
}

func (h *handler) projectUsages(w http.ResponseWriter, r *http.Request) {
	getByID(h, w, r, "project", "usages", nonNilUsages(h.store.ProjectUsages))
}

func (h *handler) listModules(w http.ResponseWriter, r *http.Request) {
	modules, err := h.store.ListModules(r.Context())
	if err != nil {
		h.internalError(w, r, "listing modules", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"modules": nonNil(modules)})
}

func (h *handler) getModule(w http.ResponseWriter, r *http.Request) {
	getByID(h, w, r, "module", "module", h.store.GetModule)
}

func (h *handler) moduleConsumers(w http.ResponseWriter, r *http.Request) {
	getByID(h, w, r, "module", "usages", nonNilUsages(h.store.ModuleConsumers))
}

func (h *handler) moduleDependencies(w http.ResponseWriter, r *http.Request) {
	getByID(h, w, r, "module", "usages", nonNilUsages(h.store.ModuleDependencies))
}

func (h *handler) listRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := h.store.ListRepos(r.Context())
	if err != nil {
		h.internalError(w, r, "listing repos", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": nonNil(repos)})
}

func (h *handler) getRepo(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id", "repo")
	if !ok {
		return
	}
	repo, err := h.store.GetRepo(r.Context(), id)
	if err == nil {
		var projects []store.Project
		if projects, err = h.store.RepoProjects(r.Context(), id); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"repo": repo, "projects": nonNil(projects)})
			return
		}
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "repo not found")
		return
	}
	h.internalError(w, r, "getting repo", err)
}

// getByID serves the result of get for the {id} path value as {key: result}.
func getByID[T any](h *handler, w http.ResponseWriter, r *http.Request, resource, key string,
	get func(context.Context, int64) (T, error),
) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, resource+" id must be a positive integer")
		return
	}
	v, err := get(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, resource+" not found")
		return
	}
	if err != nil {
		h.internalError(w, r, "getting "+resource, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{key: v})
}

func nonNilUsages(get func(context.Context, int64) ([]store.Usage, error)) func(context.Context, int64) ([]store.Usage, error) {
	return func(ctx context.Context, id int64) ([]store.Usage, error) {
		u, err := get(ctx, id)
		return nonNil(u), err
	}
}

func (h *handler) internalError(w http.ResponseWriter, r *http.Request, msg string, err error) {
	h.log.ErrorContext(r.Context(), msg, "error", err, "method", r.Method, "path", r.URL.Path)
	writeError(w, http.StatusInternalServerError, "internal server error")
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (h *handler) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		h.log.InfoContext(r.Context(), "request",
			"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration_ms", time.Since(start).Milliseconds())
	})
}

func (h *handler) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v)
			}
			h.log.ErrorContext(r.Context(), "panic serving request", "panic", v, "method", r.Method, "path", r.URL.Path)
			writeError(w, http.StatusInternalServerError, "internal server error")
		}()
		next.ServeHTTP(w, r)
	})
}

type errorBody struct {
	Error   string   `json:"error"`
	Details []string `json:"details,omitempty"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// nonNil makes empty lists encode as [] rather than null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
