// Package version parses the exact versions used to compare what a project
// pins against what a module has released.
package version

import (
	"strings"

	"github.com/Masterminds/semver/v3"
)

type Version struct {
	Major, Minor, Patch int
	// Prerelease is "" for a release.
	Prerelease string
}

// Parse accepts an exact MAJOR.MINOR.PATCH version with an optional "v"
// prefix, prerelease, and build metadata. Constraints ("~> 4.0"), partial
// versions ("4.1"), branches, and commit SHAs are rejected, since none of
// them name one release.
func Parse(s string) (Version, bool) {
	v, err := semver.StrictNewVersion(strings.TrimPrefix(strings.TrimSpace(s), "v"))
	if err != nil {
		return Version{}, false
	}
	const maxInt32 = 1<<31 - 1
	if v.Major() > maxInt32 || v.Minor() > maxInt32 || v.Patch() > maxInt32 {
		return Version{}, false
	}
	return Version{
		Major:      int(v.Major()),
		Minor:      int(v.Minor()),
		Patch:      int(v.Patch()),
		Prerelease: v.Prerelease(),
	}, true
}
