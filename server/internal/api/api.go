// Package api serves the TerraGraph HTTP API.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/WasathTheekshana/terragraph/server/internal/report"
	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

const maxReportBytes = 10 << 20

type Store interface {
	Ingest(ctx context.Context, in store.Scan) (store.IngestResult, error)
	ListProjects(ctx context.Context) ([]store.Project, error)
	ProjectUsages(ctx context.Context, projectID int64) ([]store.Usage, error)
	ListModules(ctx context.Context) ([]store.Module, error)
	ModuleConsumers(ctx context.Context, moduleID int64) ([]store.Usage, error)
	Ping(ctx context.Context) error
}

type Config struct {
	IngestToken string
	// TrackedBranches are the branches whose project scans become the
	// project's current state.
	TrackedBranches []string
}

type handler struct {
	store     Store
	cfg       Config
	log       *slog.Logger
	tokenHash [sha256.Size]byte
}

func NewHandler(s Store, cfg Config, log *slog.Logger) http.Handler {
	h := &handler{store: s, cfg: cfg, log: log, tokenHash: sha256.Sum256([]byte(cfg.IngestToken))}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)
	mux.Handle("POST /api/v1/scans", h.requireToken(http.HandlerFunc(h.createScan)))
	mux.HandleFunc("GET /api/v1/projects", h.listProjects)
	mux.HandleFunc("GET /api/v1/projects/{id}/usages", h.projectUsages)
	mux.HandleFunc("GET /api/v1/modules", h.listModules)
	mux.HandleFunc("GET /api/v1/modules/{id}/consumers", h.moduleConsumers)

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
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxReportBytes))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "scan report exceeds 10 MiB")
			return
		}
		writeError(w, http.StatusBadRequest, "reading request body failed")
		return
	}

	var rep report.Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := rep.Validate(); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorBody{
			Error:   "invalid scan report",
			Details: strings.Split(err.Error(), "\n"),
		})
		return
	}

	tracked := slices.Contains(h.cfg.TrackedBranches, rep.Subject.Branch)
	res, err := h.store.Ingest(r.Context(), store.Scan{Report: rep, Raw: raw, Tracked: tracked})
	if err != nil {
		h.internalError(w, r, "ingesting scan", err)
		return
	}

	h.log.InfoContext(r.Context(), "scan ingested",
		"scan_id", res.ScanID, "applied", res.Applied, "scanner_type", rep.ScannerType,
		"repo_url", rep.Subject.RepoURL, "branch", rep.Subject.Branch, "facts", len(rep.Facts))
	writeJSON(w, http.StatusCreated, res)
}

func (h *handler) listProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := h.store.ListProjects(r.Context())
	if err != nil {
		h.internalError(w, r, "listing projects", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": nonNil(projects)})
}

func (h *handler) projectUsages(w http.ResponseWriter, r *http.Request) {
	h.usages(w, r, h.store.ProjectUsages, "project")
}

func (h *handler) listModules(w http.ResponseWriter, r *http.Request) {
	modules, err := h.store.ListModules(r.Context())
	if err != nil {
		h.internalError(w, r, "listing modules", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"modules": nonNil(modules)})
}

func (h *handler) moduleConsumers(w http.ResponseWriter, r *http.Request) {
	h.usages(w, r, h.store.ModuleConsumers, "module")
}

func (h *handler) usages(w http.ResponseWriter, r *http.Request,
	query func(context.Context, int64) ([]store.Usage, error), resource string,
) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, resource+" id must be a positive integer")
		return
	}
	usages, err := query(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, resource+" not found")
		return
	}
	if err != nil {
		h.internalError(w, r, "listing usages", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"usages": nonNil(usages)})
}

func (h *handler) internalError(w http.ResponseWriter, r *http.Request, msg string, err error) {
	h.log.ErrorContext(r.Context(), msg, "error", err, "method", r.Method, "path", r.URL.Path)
	writeError(w, http.StatusInternalServerError, "internal server error")
}

// requireToken compares SHA-256 digests so the comparison is constant time
// regardless of token length.
func (h *handler) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		got := sha256.Sum256([]byte(token))
		if !ok || subtle.ConstantTimeCompare(got[:], h.tokenHash[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="terragraph"`)
			writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
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
