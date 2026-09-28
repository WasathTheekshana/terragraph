package web

import (
	"cmp"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

type tone string

const (
	toneOK    tone = "ok"
	toneWarn  tone = "warn"
	toneBad   tone = "bad"
	toneMuted tone = "muted"
)

// The full class strings must appear literally so Tailwind finds them.
var badgeClasses = map[tone]string{
	toneOK:    "bg-emerald-50 text-emerald-700 ring-emerald-600/20 dark:bg-emerald-500/10 dark:text-emerald-400 dark:ring-emerald-500/20",
	toneWarn:  "bg-amber-50 text-amber-800 ring-amber-600/20 dark:bg-amber-500/10 dark:text-amber-400 dark:ring-amber-500/20",
	toneBad:   "bg-rose-50 text-rose-700 ring-rose-600/20 dark:bg-rose-500/10 dark:text-rose-400 dark:ring-rose-500/20",
	toneMuted: "bg-slate-50 text-slate-600 ring-slate-500/20 dark:bg-slate-500/10 dark:text-slate-400 dark:ring-slate-500/20",
}

type status struct {
	Label string
	Tone  tone
}

func usageStatus(u store.Usage) status {
	switch {
	case u.ModuleID == nil:
		return status{"Local module", toneMuted}
	case u.PinnedVersion == nil:
		return status{"No exact version", toneMuted}
	case u.LatestVersion == nil:
		return status{"No release data", toneMuted}
	case u.MajorsBehind != nil && *u.MajorsBehind > 0:
		return status{plural(*u.MajorsBehind, "major version behind", "major versions behind"), toneBad}
	case u.Outdated:
		return status{"Outdated", toneWarn}
	default:
		return status{"Up to date", toneOK}
	}
}

// staleAfter is how long a running scan can go without progress before it's
// shown as stalled: its scanner was most likely stopped.
const staleAfter = 10 * time.Minute

func runStalled(r store.Run, now time.Time) bool {
	return r.Status == store.RunRunning && now.Sub(r.UpdatedAt) > staleAfter
}

// runStatus is how a run is shown, given the time now.
func runStatus(r store.Run, now time.Time) status {
	switch {
	case runStalled(r, now):
		return status{"Stalled", toneBad}
	case r.Status == store.RunRunning:
		return status{"Running", toneWarn}
	case r.Status == store.RunCancelled:
		return status{"Cancelled", toneMuted}
	case r.Failed > 0:
		return status{"Finished with failures", toneBad}
	default:
		return status{"Finished", toneOK}
	}
}

// runRefresh is how often, in seconds, a run's page reloads: only while it
// is making progress.
func runRefresh(r store.Run, now time.Time) int {
	if r.Status == store.RunRunning && !runStalled(r, now) {
		return 2
	}
	return 0
}

func itemStatus(it store.RunItem) status {
	switch {
	case it.Status == store.ItemFailed:
		return status{"Failed", toneBad}
	case it.Status == store.ItemPending:
		return status{"Waiting", toneMuted}
	case it.Applied != nil && !*it.Applied:
		return status{"Recorded only", toneMuted}
	default:
		return status{"Done", toneOK}
	}
}

func itemKind(it store.RunItem) string {
	if it.Kind == store.ItemKindModuleRepo {
		return "Module versions"
	}
	return "Project"
}

// itemLink is where a finished item's results are, or "" if nowhere yet.
func itemLink(it store.RunItem) templ.SafeURL {
	switch {
	case it.ProjectID != nil:
		return projectURL(*it.ProjectID)
	case it.ModuleID != nil:
		return moduleURL(*it.ModuleID)
	default:
		return ""
	}
}

func runURL(id int64) templ.SafeURL { return templ.SafeURL(fmt.Sprintf("/runs/%d", id)) }

// repoName is the short name a repo is shown by: the last segment of its URL
// without ".git", e.g. "platform-network" for
// git@github.com:acme/platform-network.git. The full URL stays available as
// a tooltip, since names can repeat across organizations.
func repoName(url string) string {
	s := strings.TrimSuffix(strings.TrimRight(url, "/"), ".git")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	if s == "" {
		return url
	}
	return s
}

// moduleName is the short name a module is shown by. A registry module keeps
// its provider ("s3-bucket/aws"), since that's part of its identity; git and
// other modules show their repo or file name.
func moduleName(key, kind string) string {
	if kind == "registry" {
		if parts := strings.Split(key, "/"); len(parts) == 4 {
			return parts[2] + "/" + parts[3]
		}
	}
	return repoName(key)
}

func usageModuleName(u store.Usage) string {
	if u.ModuleKey == nil {
		return "-"
	}
	kind := ""
	if u.ModuleKind != nil {
		kind = *u.ModuleKind
	}
	return moduleName(*u.ModuleKey, kind)
}

// projectPath is shown under a project's repo; the repo root needs no label.
func projectPath(p string) string {
	if p == "." {
		return ""
	}
	return p
}

// rootLabel names a project within its repo.
func rootLabel(p string) string {
	if p == "." || p == "" {
		return "repository root"
	}
	return p
}

func pathPrefix(p string) string {
	if p = projectPath(p); p == "" {
		return ""
	}
	return "Path " + p + " · "
}

// countTone colors a count of problems: zero is fine, anything else isn't.
func countTone(n int, problem tone) tone {
	if n == 0 {
		return toneMuted
	}
	return problem
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func formatTime(t *time.Time) string {
	if t == nil {
		return "Never"
	}
	return t.UTC().Format("2 Jan 2006, 15:04 UTC")
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func orDash(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return *s
}

func location(u store.Usage) string {
	if u.File == "" {
		return "-"
	}
	return fmt.Sprintf("%s:%d", u.File, u.Line)
}

func projectURL(id int64) templ.SafeURL { return templ.SafeURL(fmt.Sprintf("/projects/%d", id)) }
func moduleURL(id int64) templ.SafeURL  { return templ.SafeURL(fmt.Sprintf("/modules/%d", id)) }

// listQuery is a list page's search and sort state, read from the URL.
type listQuery struct {
	Q    string
	Sort string
	Desc bool
}

func parseListQuery(v url.Values, sorts []string) listQuery {
	q := listQuery{Q: strings.TrimSpace(v.Get("q")), Sort: v.Get("sort"), Desc: v.Get("dir") == "desc"}
	if !slices.Contains(sorts, q.Sort) {
		q.Sort, q.Desc = sorts[0], false
	}
	return q
}

// sortURL links to the list sorted by key, flipping the direction when key
// is already the active sort.
func (q listQuery) sortURL(path, key string) templ.SafeURL {
	v := url.Values{"sort": {key}}
	if key == q.Sort && !q.Desc {
		v.Set("dir", "desc")
	}
	if q.Q != "" {
		v.Set("q", q.Q)
	}
	return templ.SafeURL(path + "?" + v.Encode())
}

func (q listQuery) sortIndicator(key string) string {
	switch {
	case key != q.Sort:
		return ""
	case q.Desc:
		return "↓"
	default:
		return "↑"
	}
}

func matches(q string, fields ...string) bool {
	if q == "" {
		return true
	}
	q = strings.ToLower(q)
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

var repoSorts = []string{"name", "calls", "outdated", "behind", "scanned"}

func filterSortRepos(rs []store.Repo, q listQuery) []store.Repo {
	out := slices.DeleteFunc(slices.Clone(rs), func(r store.Repo) bool { return !matches(q.Q, r.RepoURL) })
	slices.SortStableFunc(out, func(a, b store.Repo) int {
		var c int
		switch q.Sort {
		case "calls":
			c = cmp.Compare(a.ModuleCalls, b.ModuleCalls)
		case "outdated":
			c = cmp.Compare(a.OutdatedCalls, b.OutdatedCalls)
		case "behind":
			c = cmp.Compare(a.MajorBehindCalls, b.MajorBehindCalls)
		case "scanned":
			c = compareTime(a.LastScanAt, b.LastScanAt)
		}
		if c == 0 {
			c = cmp.Or(
				cmp.Compare(strings.ToLower(repoName(a.RepoURL)), strings.ToLower(repoName(b.RepoURL))),
				cmp.Compare(a.RepoURL, b.RepoURL),
			)
		}
		if q.Desc {
			return -c
		}
		return c
	})
	return out
}

// splitRepos separates repos deployed as projects from repos that are shared
// modules' sources, which are shown under Modules instead.
func splitRepos(rs []store.Repo) (projects []store.Repo, moduleSources int) {
	for _, r := range rs {
		if r.ModuleID != nil {
			moduleSources++
			continue
		}
		projects = append(projects, r)
	}
	return projects, moduleSources
}

// repoLink goes straight to a repo's project when it has only one.
func repoLink(r store.Repo) templ.SafeURL {
	if r.ProjectID != nil {
		return projectURL(*r.ProjectID)
	}
	return repoURL(r.ID)
}

func repoURL(id int64) templ.SafeURL { return templ.SafeURL(fmt.Sprintf("/repos/%d", id)) }

func repoBranch(r store.Repo) string {
	switch {
	case r.SeveralBranches:
		return "several"
	case r.Branch == "":
		return "-"
	default:
		return r.Branch
	}
}

// callRow is a module call placed in its tree: Depth is how many module
// calls it's nested in.
type callRow struct {
	store.Usage
	Depth int
}

// callTree orders module calls so each call is followed by the calls nested
// inside it.
func callTree(us []store.Usage) []callRow {
	children := map[string][]store.Usage{}
	for _, u := range us {
		children[u.Parent] = append(children[u.Parent], u)
	}
	out := make([]callRow, 0, len(us))
	placed := map[string]bool{}
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		for _, u := range children[parent] {
			addr := address(u)
			if placed[addr] {
				continue
			}
			placed[addr] = true
			out = append(out, callRow{Usage: u, Depth: depth})
			walk(addr, depth+1)
		}
	}
	walk("", 0)
	// Calls whose parent wasn't reported still get shown.
	for _, u := range us {
		if !placed[address(u)] {
			out = append(out, callRow{Usage: u, Depth: strings.Count(u.Parent, ".") + 1})
		}
	}
	return out
}

func address(u store.Usage) string {
	if u.Parent == "" {
		return u.CallName
	}
	return u.Parent + "." + u.CallName
}

// Tailwind needs literal class names, so indentation is a fixed set.
var depthIndent = []string{"", "ml-5", "ml-10", "ml-15", "ml-20"}

func indent(depth int) string {
	return depthIndent[min(depth, len(depthIndent)-1)]
}

// via describes where a nested call sits, e.g. "inside addons".
func via(parent string) string {
	if parent == "" {
		return ""
	}
	return "inside " + strings.ReplaceAll(parent, ".", " › ")
}

func nestedCount(us []store.Usage) int {
	n := 0
	for _, u := range us {
		if u.Parent != "" {
			n++
		}
	}
	return n
}

// hasUnexpandedRemoteModules reports whether some remote module's own calls
// couldn't be listed because the project wasn't terraform init'd.
func hasUnexpandedRemoteModules(us []store.Usage) bool {
	for _, u := range us {
		if u.Parent == "" && u.ModuleID != nil && u.ResolutionSource != "modules-json" {
			return true
		}
	}
	return false
}

// pathGroup is one Terraform root's calls, for repos holding several.
type pathGroup struct {
	Path string
	Rows []callRow
}

func groupByPath(us []store.Usage) []pathGroup {
	var groups []pathGroup
	byPath := map[string][]store.Usage{}
	for _, u := range us {
		if _, ok := byPath[u.ProjectPath]; !ok {
			groups = append(groups, pathGroup{Path: u.ProjectPath})
		}
		byPath[u.ProjectPath] = append(byPath[u.ProjectPath], u)
	}
	for i := range groups {
		groups[i].Rows = callTree(byPath[groups[i].Path])
	}
	return groups
}

var moduleSorts = []string{"name", "consumers", "outdated", "scanned"}

func filterSortModules(ms []store.Module, q listQuery) []store.Module {
	out := slices.DeleteFunc(slices.Clone(ms), func(m store.Module) bool { return !matches(q.Q, m.Key, m.Source) })
	slices.SortStableFunc(out, func(a, b store.Module) int {
		var c int
		switch q.Sort {
		case "consumers":
			c = cmp.Compare(a.Consumers, b.Consumers)
		case "outdated":
			c = cmp.Compare(a.OutdatedConsumers, b.OutdatedConsumers)
		case "scanned":
			c = compareTime(a.VersionsScannedAt, b.VersionsScannedAt)
		}
		if c == 0 {
			c = cmp.Or(
				cmp.Compare(strings.ToLower(moduleName(a.Key, a.Kind)), strings.ToLower(moduleName(b.Key, b.Kind))),
				cmp.Compare(a.Key, b.Key),
			)
		}
		if q.Desc {
			return -c
		}
		return c
	})
	return out
}

// compareTime orders missing times first.
func compareTime(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	default:
		return a.Compare(*b)
	}
}

type reposSummary struct {
	Repos, Projects, Calls, Outdated, Behind int
}

func summarizeRepos(rs []store.Repo) reposSummary {
	s := reposSummary{Repos: len(rs)}
	for _, r := range rs {
		s.Projects += r.Projects
		s.Calls += r.ModuleCalls
		s.Outdated += r.OutdatedCalls
		s.Behind += r.MajorBehindCalls
	}
	return s
}

// usageSummary counts like the server does: Outdated is every call behind the
// latest release, and Behind the subset at least one major version behind.
type usageSummary struct {
	Calls, UpToDate, Outdated, Behind, Unknown int
}

func summarizeUsages(us []store.Usage) usageSummary {
	s := usageSummary{Calls: len(us)}
	for _, u := range us {
		switch usageStatus(u).Tone {
		case toneOK:
			s.UpToDate++
		case toneWarn:
			s.Outdated++
		case toneBad:
			s.Outdated++
			s.Behind++
		default:
			s.Unknown++
		}
	}
	return s
}

// versionShare is how many module calls pin one version of a module.
type versionShare struct {
	Version string
	Calls   int
	Status  status
}

// versionsInUse groups a module's consumers by pinned version, most used
// first.
func versionsInUse(us []store.Usage) []versionShare {
	byVersion := map[string]*versionShare{}
	var order []string
	for _, u := range us {
		v := orDash(u.PinnedVersion)
		if v == "-" {
			v = "No exact version"
		}
		if byVersion[v] == nil {
			byVersion[v] = &versionShare{Version: v, Status: usageStatus(u)}
			order = append(order, v)
		}
		byVersion[v].Calls++
	}
	out := make([]versionShare, len(order))
	for i, v := range order {
		out[i] = *byVersion[v]
	}
	slices.SortStableFunc(out, func(a, b versionShare) int {
		return cmp.Or(cmp.Compare(b.Calls, a.Calls), cmp.Compare(a.Version, b.Version))
	})
	return out
}
