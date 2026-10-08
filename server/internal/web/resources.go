package web

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/a-h/templ"

	"github.com/WasathTheekshana/terragraph/server/internal/envmatrix"
	"github.com/WasathTheekshana/terragraph/server/internal/graph"
	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

const resourcesPerPage = 100

// resourcesView is the resources page: what matched, and what the filter can be narrowed by.
type resourcesView struct {
	// Query is the filter as asked for, to build the links that change it.
	Query  url.Values
	Filter store.ResourceFilter
	Rows   []store.Resource
	Total  int
	Page   int
	Pages  int
	Facets store.Facets
}

func (h *handler) resources(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, err := store.ResourceFilterFromQuery(q)
	if err != nil {
		h.render(w, r, http.StatusBadRequest, errorPage(http.StatusBadRequest, "That filter doesn't work", err.Error()))
		return
	}
	page := 1
	if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 1 {
		page = p
	}
	f.Limit, f.Offset = resourcesPerPage, (page-1)*resourcesPerPage

	ctx := r.Context()
	total, err := h.store.CountResources(ctx, f)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	rows, err := h.store.ListResources(ctx, f)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	facets, err := h.store.ResourceFacets(ctx, f)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	pages := max(1, (total+resourcesPerPage-1)/resourcesPerPage)
	h.render(w, r, http.StatusOK, resourcesPage(resourcesView{Query: q, Filter: f, Rows: rows, Total: total, Page: min(page, pages), Pages: pages, Facets: facets}))
}

// resourcesLink is the resources page with the query's parameters.
func resourcesLink(q url.Values) templ.SafeURL {
	q = cloneQuery(q)
	q.Del("page")
	if enc := q.Encode(); enc != "" {
		return templ.SafeURL("/resources?" + enc)
	}
	return "/resources"
}

func cloneQuery(q url.Values) url.Values {
	out := make(url.Values, len(q))
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// with is the page's filter with one parameter set. Tags add to the ones already there.
func (v resourcesView) with(key, value string) templ.SafeURL {
	q := cloneQuery(v.Query)
	if key == "tag" {
		if name, _, hasValue := strings.Cut(value, "="); hasValue {
			var keep []string
			for _, t := range q["tag"] {
				if t != name {
					keep = append(keep, t)
				}
			}
			q["tag"] = keep
		}
		if !contains(q["tag"], value) {
			q.Add("tag", value)
		}
	} else {
		q.Set(key, value)
	}
	return resourcesLink(q)
}

// without is the page's filter with one parameter removed.
func (v resourcesView) without(key, value string) templ.SafeURL {
	q := cloneQuery(v.Query)
	if key == "tag" {
		var keep []string
		for _, t := range q["tag"] {
			if t != value {
				keep = append(keep, t)
			}
		}
		q["tag"] = keep
		if len(keep) == 0 {
			q.Del("tag")
		}
	} else {
		q.Del(key)
	}
	return resourcesLink(q)
}

func (v resourcesView) pageLink(n int) templ.SafeURL {
	q := cloneQuery(v.Query)
	if n > 1 {
		q.Set("page", strconv.Itoa(n))
	} else {
		q.Del("page")
	}
	if enc := q.Encode(); enc != "" {
		return templ.SafeURL("/resources?" + enc)
	}
	return "/resources"
}

// activeFilters lists the filters in force as label and the link that removes each.
type activeFilter struct {
	Label string
	Undo  templ.SafeURL
}

func (v resourcesView) activeFilters() []activeFilter {
	var out []activeFilter
	add := func(key, label string) {
		out = append(out, activeFilter{Label: label, Undo: v.without(key, "")})
	}
	if v.Filter.Type != "" {
		add("type", "type "+v.Filter.Type)
	}
	if v.Filter.Mode != "" {
		add("mode", modeLabel(v.Filter.Mode))
	}
	if v.Filter.Enabled != "" {
		add("enabled", "enabled: "+v.Filter.Enabled)
	}
	if v.Filter.ProjectID != 0 {
		add("project", "one project")
	}
	if v.Filter.RepoID != 0 {
		add("repo", "one repository")
	}
	for _, t := range v.Query["tag"] {
		out = append(out, activeFilter{Label: "tag " + t, Undo: v.without("tag", t)})
	}
	return out
}

func modeLabel(mode string) string {
	if mode == "data" {
		return "looked up (data sources)"
	}
	return "created"
}

func (v resourcesView) from() int { return (v.Page-1)*resourcesPerPage + 1 }
func (v resourcesView) to() int   { return min(v.Page*resourcesPerPage, v.Total) }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// sortedTags lists a resource's tags in order.
func sortedTags(tags map[string]string) []string {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func tagFilter(k, v string) string { return k + "=" + v }

func resourceState(r store.Resource) status {
	switch r.Enabled {
	case "false":
		return status{"Off", toneMuted}
	case "unknown":
		return status{"Unknown", toneWarn}
	}
	if r.Instances != nil && *r.Instances > 1 {
		return status{strconv.Itoa(*r.Instances) + " copies", toneOK}
	}
	return status{"On", toneOK}
}

func displayName(r store.Resource) string {
	if n := r.Attrs["name"]; n != "" {
		return n
	}
	return ""
}

// environments compares the projects of one repository.
func (h *handler) environments(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		h.notFound(w, r)
		return
	}
	ctx := r.Context()
	repo, err := h.store.GetRepo(ctx, id)
	if err != nil {
		h.lookupError(w, r, err)
		return
	}
	projects, err := h.store.RepoProjects(ctx, id)
	if err != nil {
		h.lookupError(w, r, err)
		return
	}
	resources, err := h.store.ListResources(ctx, store.ResourceFilter{RepoID: id})
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	inventories, err := h.store.ListInventories(ctx)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	m := envmatrix.Compare(envmatrix.Input{Projects: projects, Resources: resources, Inventories: inventories})
	v := environmentsView{Repo: repo, Matrix: m, OnlyDifferences: r.URL.Query().Get("diff") == "1"}
	if v.OnlyDifferences {
		v.Matrix = onlyDifferences(m)
	}
	h.render(w, r, http.StatusOK, environmentsPage(v))
}

type environmentsView struct {
	Repo            store.Repo
	Matrix          envmatrix.Matrix
	OnlyDifferences bool
}

// onlyDifferences keeps the rows where the environments disagree.
func onlyDifferences(m envmatrix.Matrix) envmatrix.Matrix {
	out := m
	out.Resources = nil
	for _, r := range m.Resources {
		if r.Differs {
			out.Resources = append(out.Resources, r)
		}
	}
	out.Flags, out.Settings, out.Tags = nil, nil, nil
	for _, r := range m.Flags {
		if r.Differs {
			out.Flags = append(out.Flags, r)
		}
	}
	for _, r := range m.Settings {
		if r.Differs {
			out.Settings = append(out.Settings, r)
		}
	}
	for _, r := range m.Tags {
		if r.Differs || r.Varies {
			out.Tags = append(out.Tags, r)
		}
	}
	return out
}

func variableName(modulePath, name string) string {
	if modulePath == "" {
		return name
	}
	return "module." + strings.ReplaceAll(modulePath, ".", ".module.") + "." + name
}

func (e environmentsView) hasResources() bool {
	for _, env := range e.Matrix.Environments {
		if env.Scanned {
			return true
		}
	}
	return false
}

func environmentCellState(c envmatrix.ResourceCell) status {
	switch c.State {
	case envmatrix.StateEnabled:
		if c.Instances != nil && *c.Instances > 1 {
			return status{strconv.Itoa(*c.Instances) + " copies", toneOK}
		}
		return status{"On", toneOK}
	case envmatrix.StateDisabled:
		return status{"Off", toneMuted}
	case envmatrix.StateUnknown:
		return status{"Unknown", toneWarn}
	}
	return status{"", toneMuted}
}

// resourceGraphView is the resource graph page.
func (h *handler) resourceGraphPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, graphPage(newResourceGraphView(r.URL.Query())))
}

// resourceGraphData serves the resource graph as JSON to the page's script.
func (h *handler) resourceGraphData(w http.ResponseWriter, r *http.Request) {
	opts, err := graph.ResourceOptionsFromQuery(r.URL.Query())
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	in, err := graph.LoadResources(r.Context(), h.store)
	if err != nil {
		h.log.ErrorContext(r.Context(), "loading the resource graph's data", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	g, err := graph.BuildResources(in, opts)
	switch {
	case errors.Is(err, graph.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, graph.ErrInvalid):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		h.log.ErrorContext(r.Context(), "building the resource graph", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
	default:
		writeJSON(w, http.StatusOK, g)
	}
}

func joinList(items []string) string { return strings.Join(items, ", ") }
