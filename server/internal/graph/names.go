package graph

import (
	"strconv"
	"strings"

	"github.com/WasathTheekshana/terragraph/server/internal/source"
	"github.com/WasathTheekshana/terragraph/server/internal/store"
)

// repoLabel is a repository's short name, whatever form its URL is written in.
func repoLabel(url string) string {
	key := source.RepoKey(url)
	parts := strings.Split(key, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return key
}

// projectLabel is the repo's name, followed by the root's folder when it isn't the repo root.
func projectLabel(p store.Project) string {
	name := repoLabel(p.RepoURL)
	if dir := folderSubdir(p.Path); dir != "" {
		return name + "/" + dir
	}
	return name
}

// moduleLabel is a module's short name, followed by "//subdir" for a module that lives in a
// folder of a larger repo, the way Terraform writes it.
func moduleLabel(key, kind, subdir string) string {
	name := key
	parts := strings.Split(key, "/")
	switch {
	case key == "":
		name = "module"
	case kind == "registry" && len(parts) >= 3:
		// host/namespace/name/provider
		name = parts[len(parts)-2]
	default:
		name = parts[len(parts)-1]
	}
	if subdir != "" {
		return name + "//" + subdir
	}
	return name
}

// localName is how a local module is described when it is folded into its caller.
func localName(u store.Usage) string {
	if u.Source != "" {
		return u.Source
	}
	return u.CallName
}

// compareVersions orders versions, newest greatest. Real versions come before refs such as
// "main" and "unpinned", and a release comes after its own prereleases.
func compareVersions(a, b string) int {
	na, aok := versionNumbers(a)
	nb, bok := versionNumbers(b)
	switch {
	case aok && !bok:
		return 1
	case !aok && bok:
		return -1
	case !aok && !bok:
		// "unpinned" says the least, so it goes last among things that aren't versions.
		if a == unpinned || b == unpinned {
			if a == b {
				return 0
			}
			if a == unpinned {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	}
	for i := 0; i < len(na) && i < len(nb); i++ {
		if na[i] != nb[i] {
			if na[i] > nb[i] {
				return 1
			}
			return -1
		}
	}
	if len(na) != len(nb) {
		if len(na) > len(nb) {
			return 1
		}
		return -1
	}
	aPre, bPre := strings.Contains(a, "-"), strings.Contains(b, "-")
	if aPre != bPre {
		if aPre {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func versionNumbers(v string) ([]int, bool) {
	v = strings.TrimPrefix(v, "v")
	core, _, _ := strings.Cut(v, "-")
	core, _, _ = strings.Cut(core, "+")
	if core == "" {
		return nil, false
	}
	var out []int
	for _, part := range strings.Split(core, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}
