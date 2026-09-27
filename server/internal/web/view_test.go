package web

import (
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

func ptr[T any](v T) *T { return &v }

func TestUsageStatus(t *testing.T) {
	mod := ptr(int64(1))
	tests := []struct {
		name string
		u    store.Usage
		want status
	}{
		{"local", store.Usage{}, status{"Local module", toneMuted}},
		{"constraint only", store.Usage{ModuleID: mod}, status{"No exact version", toneMuted}},
		{"module repo not scanned", store.Usage{ModuleID: mod, PinnedVersion: ptr("1.0.0")}, status{"No release data", toneMuted}},
		{"one major behind", store.Usage{ModuleID: mod, PinnedVersion: ptr("1.0.0"), LatestVersion: ptr("2.0.0"), MajorsBehind: ptr(1), Outdated: true},
			status{"1 major version behind", toneBad}},
		{"two majors behind", store.Usage{ModuleID: mod, PinnedVersion: ptr("1.0.0"), LatestVersion: ptr("3.0.0"), MajorsBehind: ptr(2), Outdated: true},
			status{"2 major versions behind", toneBad}},
		{"minor behind", store.Usage{ModuleID: mod, PinnedVersion: ptr("2.0.0"), LatestVersion: ptr("2.1.0"), MajorsBehind: ptr(0), Outdated: true},
			status{"Outdated", toneWarn}},
		{"current", store.Usage{ModuleID: mod, PinnedVersion: ptr("2.1.0"), LatestVersion: ptr("2.1.0"), MajorsBehind: ptr(0)},
			status{"Up to date", toneOK}},
	}
	for _, tt := range tests {
		if got := usageStatus(tt.u); got != tt.want {
			t.Errorf("%s: usageStatus = %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestRunStatus(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		run         store.Run
		want        string
		wantRefresh int
	}{
		{"progressing", store.Run{Status: store.RunRunning, UpdatedAt: now.Add(-time.Minute)}, "Running", 2},
		{"stalled", store.Run{Status: store.RunRunning, UpdatedAt: now.Add(-11 * time.Minute)}, "Stalled", 0},
		{"cancelled", store.Run{Status: store.RunCancelled}, "Cancelled", 0},
		{"failures", store.Run{Status: store.RunFinished, Failed: 1}, "Finished with failures", 0},
		{"clean", store.Run{Status: store.RunFinished}, "Finished", 0},
	}
	for _, tt := range tests {
		if got := runStatus(tt.run, now).Label; got != tt.want {
			t.Errorf("%s: runStatus = %q, want %q", tt.name, got, tt.want)
		}
		if got := runRefresh(tt.run, now); got != tt.wantRefresh {
			t.Errorf("%s: runRefresh = %d, want %d", tt.name, got, tt.wantRefresh)
		}
	}
}

func TestItemStatus(t *testing.T) {
	tests := map[string]store.RunItem{
		"Waiting":       {Status: store.ItemPending},
		"Failed":        {Status: store.ItemFailed},
		"Done":          {Status: store.ItemDone, Applied: ptr(true)},
		"Recorded only": {Status: store.ItemDone, Applied: ptr(false)},
	}
	for want, it := range tests {
		if got := itemStatus(it).Label; got != want {
			t.Errorf("itemStatus(%+v) = %q, want %q", it, got, want)
		}
	}
}

func TestRepoName(t *testing.T) {
	tests := map[string]string{
		"git@github.com:acme/platform-network.git":           "platform-network",
		"https://github.com/acme/api.git":                    "api",
		"https://github.com/acme/api":                        "api",
		"https://github.com/acme/api/":                       "api",
		"ssh://git@gitlab.example.com:2222/platform/vpc.git": "vpc",
		"https://dev.azure.com/org/proj/_git/infra-live":     "infra-live",
		"git@github.com:solo.git":                            "solo",
		"file://laptop/C:/work/next-projects":                "next-projects",
		"":                                                   "",
	}
	for in, want := range tests {
		if got := repoName(in); got != want {
			t.Errorf("repoName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModuleName(t *testing.T) {
	tests := []struct{ key, kind, want string }{
		{"github.com/terraform-aws-modules/terraform-aws-vpc", "git", "terraform-aws-vpc"},
		{"gitlab.example.com/platform/networking/vpc", "git", "vpc"},
		{"registry.terraform.io/terraform-aws-modules/s3-bucket/aws", "registry", "s3-bucket/aws"},
		{"app.terraform.io/acme/eks/azurerm", "registry", "eks/azurerm"},
		{"s3.amazonaws.com/bucket/vpc.zip", "other", "vpc.zip"},
	}
	for _, tt := range tests {
		if got := moduleName(tt.key, tt.kind); got != tt.want {
			t.Errorf("moduleName(%q, %q) = %q, want %q", tt.key, tt.kind, got, tt.want)
		}
	}
	if got := usageModuleName(store.Usage{ModuleKey: ptr("registry.terraform.io/x/eks/aws"), ModuleKind: ptr("registry")}); got != "eks/aws" {
		t.Errorf("usageModuleName = %q", got)
	}
	if got := usageModuleName(store.Usage{}); got != "-" {
		t.Errorf("usageModuleName of a local module = %q", got)
	}
}

func TestModulesSortByDisplayedName(t *testing.T) {
	ms := []store.Module{
		{Key: "github.com/org/zeta", Kind: "git"},
		{Key: "registry.terraform.io/x/alb/aws", Kind: "registry"},
		{Key: "github.com/org/Mid", Kind: "git"},
	}
	var got []string
	for _, m := range filterSortModules(ms, listQuery{Sort: "name"}) {
		got = append(got, moduleName(m.Key, m.Kind))
	}
	if want := []string{"alb/aws", "Mid", "zeta"}; !slices.Equal(got, want) {
		t.Errorf("sorted = %q, want %q", got, want)
	}
}

func TestProjectsSortByDisplayedName(t *testing.T) {
	ps := []store.Project{
		{RepoURL: "https://github.com/org/zeta.git"},
		{RepoURL: "git@github.com:org/Alpha.git"},
		{RepoURL: "git@github.com:org/mid.git", Path: "envs/prod"},
		{RepoURL: "git@github.com:org/mid.git", Path: "envs/dev"},
	}
	var got []string
	for _, p := range filterSortProjects(ps, listQuery{Sort: "name"}) {
		got = append(got, repoName(p.RepoURL)+" "+p.Path)
	}
	want := []string{"Alpha ", "mid envs/dev", "mid envs/prod", "zeta "}
	if !slices.Equal(got, want) {
		t.Errorf("sorted = %q, want %q", got, want)
	}
}

func TestEveryToneHasBadgeClasses(t *testing.T) {
	for _, tn := range []tone{toneOK, toneWarn, toneBad, toneMuted} {
		if badgeClasses[tn] == "" {
			t.Errorf("tone %q has no badge classes", tn)
		}
	}
}

func TestParseListQuery(t *testing.T) {
	q := parseListQuery(url.Values{"q": {"  vpc "}, "sort": {"calls"}, "dir": {"desc"}}, projectSorts)
	if q != (listQuery{Q: "vpc", Sort: "calls", Desc: true}) {
		t.Errorf("q = %+v", q)
	}
	q = parseListQuery(url.Values{"sort": {"'; drop table"}, "dir": {"desc"}}, projectSorts)
	if q != (listQuery{Sort: "name"}) {
		t.Errorf("unknown sort should fall back to name ascending, got %+v", q)
	}
}

func TestSortURL(t *testing.T) {
	q := listQuery{Q: "a b", Sort: "calls"}
	if got := q.sortURL("/", "calls"); got != "/?dir=desc&q=a+b&sort=calls" {
		t.Errorf("active ascending column should flip to desc, got %s", got)
	}
	q.Desc = true
	if got := q.sortURL("/", "calls"); got != "/?q=a+b&sort=calls" {
		t.Errorf("active descending column should flip to asc, got %s", got)
	}
	if got := q.sortURL("/modules", "name"); got != "/modules?q=a+b&sort=name" {
		t.Errorf("other column should sort asc, got %s", got)
	}
}

func TestFilterSortProjects(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ps := []store.Project{
		{RepoURL: "github.com/org/b", ModuleCalls: 5, LastScanAt: &t1},
		{RepoURL: "github.com/org/a", ModuleCalls: 5},
		{RepoURL: "gitlab.com/org/c", ModuleCalls: 9},
	}
	names := func(ps []store.Project) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.RepoURL)
		}
		return out
	}

	got := names(filterSortProjects(ps, listQuery{Sort: "calls", Desc: true}))
	if want := []string{"gitlab.com/org/c", "github.com/org/b", "github.com/org/a"}; !slices.Equal(got, want) {
		t.Errorf("by calls desc = %v, want %v", got, want)
	}
	got = names(filterSortProjects(ps, listQuery{Q: "GITHUB", Sort: "name"}))
	if want := []string{"github.com/org/a", "github.com/org/b"}; !slices.Equal(got, want) {
		t.Errorf("filtered = %v, want %v", got, want)
	}
	got = names(filterSortProjects(ps, listQuery{Sort: "scanned"}))
	if got[len(got)-1] != "github.com/org/b" {
		t.Errorf("never-scanned projects should sort before scanned ones, got %v", got)
	}
	if ps[0].RepoURL != "github.com/org/b" {
		t.Error("sorting modified the input slice")
	}
}

func TestFilterModulesMatchesSource(t *testing.T) {
	ms := []store.Module{
		{Key: "github.com/org/vpc", Source: "git::https://github.com/org/vpc.git"},
		{Key: "registry.terraform.io/x/eks/aws", Source: "x/eks/aws"},
	}
	got := filterSortModules(ms, listQuery{Q: "https://", Sort: "name"})
	if len(got) != 1 || got[0].Key != "github.com/org/vpc" {
		t.Errorf("got %+v", got)
	}
}

func TestVersionsInUse(t *testing.T) {
	mod := ptr(int64(1))
	us := []store.Usage{
		{ModuleID: mod, PinnedVersion: ptr("1.0.0"), LatestVersion: ptr("2.0.0"), MajorsBehind: ptr(1), Outdated: true},
		{ModuleID: mod, PinnedVersion: ptr("2.0.0"), LatestVersion: ptr("2.0.0"), MajorsBehind: ptr(0)},
		{ModuleID: mod, PinnedVersion: ptr("1.0.0"), LatestVersion: ptr("2.0.0"), MajorsBehind: ptr(1), Outdated: true},
		{ModuleID: mod},
	}
	got := versionsInUse(us)
	want := []versionShare{
		{"1.0.0", 2, status{"1 major version behind", toneBad}},
		{"2.0.0", 1, status{"Up to date", toneOK}},
		{"No exact version", 1, status{"No exact version", toneMuted}},
	}
	if !slices.Equal(got, want) {
		t.Errorf("versionsInUse =\n%+v\nwant\n%+v", got, want)
	}
}

func TestSummarizeUsages(t *testing.T) {
	mod := ptr(int64(1))
	s := summarizeUsages([]store.Usage{
		{},
		{ModuleID: mod, PinnedVersion: ptr("1.0.0"), LatestVersion: ptr("2.0.0"), MajorsBehind: ptr(1), Outdated: true},
		{ModuleID: mod, PinnedVersion: ptr("2.0.0"), LatestVersion: ptr("2.1.0"), MajorsBehind: ptr(0), Outdated: true},
		{ModuleID: mod, PinnedVersion: ptr("2.1.0"), LatestVersion: ptr("2.1.0"), MajorsBehind: ptr(0)},
	})
	if s != (usageSummary{Calls: 4, UpToDate: 1, Outdated: 2, Behind: 1, Unknown: 1}) {
		t.Errorf("summary = %+v", s)
	}
}

func TestFormatting(t *testing.T) {
	ts := time.Date(2026, 9, 27, 10, 33, 0, 0, time.FixedZone("IST", 5*3600+1800))
	if got := formatTime(&ts); got != "27 Sep 2026, 05:03 UTC" {
		t.Errorf("formatTime = %q", got)
	}
	if formatTime(nil) != "Never" || shortSHA("908ac9f4c548") != "908ac9f" || shortSHA("abc") != "abc" {
		t.Error("formatting helpers")
	}
	if location(store.Usage{File: "main.tf", Line: 3}) != "main.tf:3" || location(store.Usage{}) != "-" {
		t.Error("location")
	}
}
