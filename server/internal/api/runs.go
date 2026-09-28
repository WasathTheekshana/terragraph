package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/WasathTheekshana/terragraph/server/internal/auth"
	"github.com/WasathTheekshana/terragraph/server/internal/report"
	"github.com/WasathTheekshana/terragraph/server/internal/source"
	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

const (
	maxRunItems     = 20000
	maxLabelLen     = 500
	maxItemErrorLen = 2000
	defaultRunList  = 50
	maxRunList      = 200
)

type createRunRequest struct {
	Label string             `json:"label"`
	Items []store.NewRunItem `json:"items"`
}

func (h *handler) createRun(w http.ResponseWriter, r *http.Request) {
	var req createRunRequest
	if _, ok := readJSON(w, r, maxReportBytes, &req); !ok {
		return
	}
	items, err := normalizeRunItems(req)
	if err != nil {
		writeInvalid(w, "invalid run", err)
		return
	}
	if err := itemsInScope(r, items); err != nil {
		writeJSON(w, http.StatusForbidden, errorBody{Error: "these credentials can't submit scans for some items", Details: strings.Split(err.Error(), "\n")})
		return
	}
	run, created, err := h.store.CreateRun(r.Context(), req.Label, r.Header.Get("Idempotency-Key"), items)
	if err != nil {
		h.internalError(w, r, "creating run", err)
		return
	}
	h.log.InfoContext(r.Context(), "run created", "run_id", run.ID, "label", run.Label, "items", len(created))
	writeJSON(w, http.StatusCreated, map[string]any{"run": run, "items": created})
}

// normalizeRunItems validates a run request and puts item paths in the
// canonical form reports use, so they match on submission.
func normalizeRunItems(req createRunRequest) ([]store.NewRunItem, error) {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if req.Label == "" || len(req.Label) > maxLabelLen {
		add("label is required and at most %d characters", maxLabelLen)
	}
	if len(req.Items) == 0 || len(req.Items) > maxRunItems {
		add("a run needs between 1 and %d items", maxRunItems)
	}

	out := make([]store.NewRunItem, 0, len(req.Items))
	seen := make(map[store.NewRunItem]bool)
	for i, it := range req.Items {
		switch it.Kind {
		case store.ItemKindProject:
			if !report.ValidPath(it.Path) {
				add("items[%d].path %q must be a relative, slash-separated path inside the repo", i, it.Path)
				continue
			}
			it.Path = report.Subject{Path: it.Path}.ProjectPath()
		case store.ItemKindModuleRepo:
			it.Path = ""
		default:
			add("items[%d].kind must be %q or %q", i, store.ItemKindProject, store.ItemKindModuleRepo)
			continue
		}
		if it.RepoURL == "" || source.RepoKey(it.RepoURL) == "" {
			add("items[%d].repo_url is required", i)
			continue
		}
		if seen[it] {
			add("items[%d] duplicates an earlier item", i)
			continue
		}
		seen[it] = true
		out = append(out, it)
	}
	return out, errors.Join(errs...)
}

// itemsInScope checks project items against the principal's repo scope.
// Module repo items only record a repo's public version tags, so any
// ingest credential may submit them.
func itemsInScope(r *http.Request, items []store.NewRunItem) error {
	p, _ := auth.FromContext(r.Context())
	var errs []error
	for _, it := range items {
		if it.Kind == store.ItemKindProject && !p.MayIngestRepo(source.RepoKey(it.RepoURL)) {
			errs = append(errs, fmt.Errorf("%s is outside this credential's repos", it.RepoURL))
		}
	}
	return errors.Join(errs...)
}

func (h *handler) submitRunItem(w http.ResponseWriter, r *http.Request) {
	runID, itemID, ok := runItemIDs(w, r)
	if !ok {
		return
	}
	scan, ok := h.readScan(w, r)
	if !ok {
		return
	}
	res, err := h.store.IngestRunItem(r.Context(), runID, itemID, scan)
	if err != nil {
		h.runError(w, r, "submitting run item", err)
		return
	}
	h.logIngest(r, scan, res)
	status := http.StatusCreated
	if res.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, res)
}

func (h *handler) failRunItem(w http.ResponseWriter, r *http.Request) {
	runID, itemID, ok := runItemIDs(w, r)
	if !ok {
		return
	}
	var req struct {
		Error string `json:"error"`
	}
	if _, ok := readJSON(w, r, 64<<10, &req); !ok {
		return
	}
	msg := []rune(req.Error)
	if len(msg) > maxItemErrorLen {
		msg = msg[:maxItemErrorLen]
	}
	if err := h.store.FailRunItem(r.Context(), runID, itemID, string(msg)); err != nil {
		h.runError(w, r, "failing run item", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) finishRun(w http.ResponseWriter, r *http.Request) {
	runID, ok := pathInt(w, r, "id", "run")
	if !ok {
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if _, ok := readJSON(w, r, 64<<10, &req); !ok {
		return
	}
	if req.Status != store.RunFinished && req.Status != store.RunCancelled {
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("status must be %q or %q", store.RunFinished, store.RunCancelled))
		return
	}
	run, err := h.store.FinishRun(r.Context(), runID, req.Status)
	if err != nil {
		h.runError(w, r, "finishing run", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run})
}

func (h *handler) listRuns(w http.ResponseWriter, r *http.Request) {
	limit := defaultRunList
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxRunList {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", maxRunList))
			return
		}
		limit = n
	}
	runs, err := h.store.ListRuns(r.Context(), limit)
	if err != nil {
		h.internalError(w, r, "listing runs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": nonNil(runs)})
}

func (h *handler) getRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id", "run")
	if !ok {
		return
	}
	run, err := h.store.GetRun(r.Context(), id)
	if err != nil {
		h.runError(w, r, "getting run", err)
		return
	}
	items, err := h.store.RunItems(r.Context(), id)
	if err != nil {
		h.runError(w, r, "getting run items", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run, "items": nonNil(items)})
}

func (h *handler) runError(w http.ResponseWriter, r *http.Request, msg string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "run or run item not found")
	case errors.Is(err, store.ErrRunClosed):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrItemMismatch):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		h.internalError(w, r, msg, err)
	}
}

func runItemIDs(w http.ResponseWriter, r *http.Request) (runID, itemID int64, ok bool) {
	if runID, ok = pathInt(w, r, "id", "run"); !ok {
		return 0, 0, false
	}
	if itemID, ok = pathInt(w, r, "item", "item"); !ok {
		return 0, 0, false
	}
	return runID, itemID, true
}

func pathInt(w http.ResponseWriter, r *http.Request, name, resource string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, resource+" id must be a positive integer")
		return 0, false
	}
	return id, true
}
