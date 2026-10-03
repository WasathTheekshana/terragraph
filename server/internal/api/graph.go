package api

import (
	"errors"
	"net/http"

	"github.com/WasathTheekshana/terragraph/server/internal/graph"
)

// getGraph serves the dependency graph of every project and module, or of what one project, repo,
// or module connects to.
func (h *handler) getGraph(w http.ResponseWriter, r *http.Request) {
	opts, err := graph.OptionsFromQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	in, err := graph.Load(r.Context(), h.store)
	if err != nil {
		h.internalError(w, r, "loading the graph's data", err)
		return
	}

	g, err := graph.Build(in, opts)
	switch {
	case errors.Is(err, graph.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, graph.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		h.internalError(w, r, "building the graph", err)
	default:
		writeJSON(w, http.StatusOK, g)
	}
}
