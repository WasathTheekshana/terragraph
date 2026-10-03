package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/WasathTheekshana/terragraph/server/internal/graph"
)

// graphView is what the graph page needs to draw its controls.
type graphView struct {
	// FocusKind is "project", "repo", or "module" when the graph is narrowed, and FocusID its id.
	FocusKind string
	FocusID   string
	Direction string
	Depth     string
	Level     string
	Locals    bool
	DataURL   string
}

func newGraphView(q url.Values) graphView {
	v := graphView{Direction: q.Get("direction"), Depth: q.Get("depth"), Level: q.Get("level"), Locals: q.Get("locals") == "true"}
	for _, kind := range []string{"project", "repo", "module"} {
		if id := q.Get(kind); id != "" {
			v.FocusKind, v.FocusID = kind, id
		}
	}
	v.DataURL = "/graph/data"
	if enc := q.Encode(); enc != "" {
		v.DataURL += "?" + enc
	}
	return v
}

// focusNode is the node to select when the page opens: the project or repo being looked at.
func (v graphView) focusNode() string {
	if v.FocusKind == "project" || v.FocusKind == "repo" {
		return v.FocusKind + ":" + v.FocusID
	}
	return ""
}

func graphSubtitle(v graphView) string {
	switch v.FocusKind {
	case "project":
		return "One project and the modules it depends on. Select a node for details."
	case "repo":
		return "One repository's projects and the modules they depend on. Select a node for details."
	case "module":
		return "One module, what it depends on, and what uses it. Select a node for details."
	}
	return "Every project and the modules it calls. Drag to pan, scroll to zoom, select a node for details."
}

func (h *handler) graphPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, graphPage(newGraphView(r.URL.Query())))
}

// graphData serves the graph as JSON to the page's script, under the same sign-in as the page.
func (h *handler) graphData(w http.ResponseWriter, r *http.Request) {
	opts, err := graph.OptionsFromQuery(r.URL.Query())
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	in, err := graph.Load(r.Context(), h.store)
	if err != nil {
		h.log.ErrorContext(r.Context(), "loading the graph's data", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	g, err := graph.Build(in, opts)
	switch {
	case errors.Is(err, graph.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, graph.ErrInvalid):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		h.log.ErrorContext(r.Context(), "building the graph", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
	default:
		writeJSON(w, http.StatusOK, g)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
