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

var projectSorts = []string{"name", "calls", "outdated", "behind", "scanned"}

func filterSortProjects(ps []store.Project, q listQuery) []store.Project {
	out := slices.DeleteFunc(slices.Clone(ps), func(p store.Project) bool { return !matches(q.Q, p.RepoURL) })
	slices.SortStableFunc(out, func(a, b store.Project) int {
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
			c = cmp.Compare(a.RepoURL, b.RepoURL)
		}
		if q.Desc {
			return -c
		}
		return c
	})
	return out
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
			c = cmp.Compare(a.Key, b.Key)
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

type projectsSummary struct {
	Projects, Calls, Outdated, Behind int
}

func summarizeProjects(ps []store.Project) projectsSummary {
	s := projectsSummary{Projects: len(ps)}
	for _, p := range ps {
		s.Calls += p.ModuleCalls
		s.Outdated += p.OutdatedCalls
		s.Behind += p.MajorBehindCalls
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
