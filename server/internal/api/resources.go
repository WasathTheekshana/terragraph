package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/WasathTheekshana/terragraph/server/internal/envmatrix"
	"github.com/WasathTheekshana/terragraph/server/internal/graph"
	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

// listResources serves what projects create and look up, filtered and paged.
func (h *handler) listResources(w http.ResponseWriter, r *http.Request) {
	f, err := store.ResourceFilterFromQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resources, err := h.store.ListResources(r.Context(), f)
	if err != nil {
		h.internalError(w, r, "listing resources", err)
		return
	}
	total, err := h.store.CountResources(r.Context(), f)
	if err != nil {
		h.internalError(w, r, "counting resources", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": nonNil(resources), "total": total, "limit": f.Limit, "offset": f.Offset})
}

// resourceFacets serves the resource types and tags among the resources matching the filter.
func (h *handler) resourceFacets(w http.ResponseWriter, r *http.Request) {
	f, err := store.ResourceFilterFromQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	facets, err := h.store.ResourceFacets(r.Context(), f)
	if err != nil {
		h.internalError(w, r, "summarising resources", err)
		return
	}
	writeJSON(w, http.StatusOK, facets)
}

// getResourceGraph serves which projects create what other projects look up.
func (h *handler) getResourceGraph(w http.ResponseWriter, r *http.Request) {
	opts, err := graph.ResourceOptionsFromQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	in, err := graph.LoadResources(r.Context(), h.store)
	if err != nil {
		h.internalError(w, r, "loading the resource graph's data", err)
		return
	}
	g, err := graph.BuildResources(in, opts)
	switch {
	case errors.Is(err, graph.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, graph.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		h.internalError(w, r, "building the resource graph", err)
	default:
		writeJSON(w, http.StatusOK, g)
	}
}

// projectInventory serves a project's variables, outputs, and where its state is kept.
func (h *handler) projectInventory(w http.ResponseWriter, r *http.Request) {
	getByID(h, w, r, "project", "inventory", h.store.ProjectInventory)
}

// repoEnvironments serves the comparison of a repo's projects: what each creates, which switches
// are on, and how their settings and tags differ.
func (h *handler) repoEnvironments(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "repo id must be a positive integer")
		return
	}
	projects, err := h.store.RepoProjects(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "repo not found")
		return
	}
	if err != nil {
		h.internalError(w, r, "listing the repo's projects", err)
		return
	}
	resources, err := h.store.ListResources(r.Context(), store.ResourceFilter{RepoID: id})
	if err != nil {
		h.internalError(w, r, "listing the repo's resources", err)
		return
	}
	inventories, err := h.store.ListInventories(r.Context())
	if err != nil {
		h.internalError(w, r, "listing inventories", err)
		return
	}
	writeJSON(w, http.StatusOK, envmatrix.Compare(envmatrix.Input{Projects: projects, Resources: resources, Inventories: inventories}))
}
