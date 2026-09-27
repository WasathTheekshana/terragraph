// Package discover finds every Terraform root under a directory: a single
// repo, a subfolder of one, or a folder holding many repos at any depth.
package discover

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/WasathTheekshana/terragraph/scanner/internal/hclscan"
)

// skipDirs never hold roots worth reporting. Directories starting with "."
// (.git, .terraform, .terragrunt-cache, ...) are skipped too.
var skipDirs = map[string]bool{
	"node_modules": true,
	// Module repos keep usage examples here; they aren't deployed projects.
	"examples": true,
}

// Root is a directory Terraform would be run from.
type Root struct {
	Dir   string
	Calls []hclscan.ModuleCall
	// Err is set when the directory's configuration couldn't be parsed. The
	// root is still returned so the failure can be reported.
	Err error
}

type Options struct {
	// Exclude holds glob patterns (path.Match syntax) for directories to skip,
	// matched against the directory name and its slash path relative to the
	// scanned directory.
	Exclude []string
}

// Roots returns the Terraform roots under base, sorted by path. A directory
// containing .tf files is a root unless another directory uses it as a local
// module (source = "./modules/x").
func Roots(base string, opts Options) ([]Root, error) {
	base, err := filepath.Abs(base)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(base); err != nil {
		return nil, err
	} else if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", base)
	}
	for _, p := range opts.Exclude {
		if _, err := path.Match(p, ""); err != nil {
			return nil, fmt.Errorf("invalid exclude pattern %q: %w", p, err)
		}
	}

	tfDirs := map[string]bool{}
	err = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == base {
				return err
			}
			// An unreadable subdirectory shouldn't stop a scan of everything else.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != base && skip(base, p, d.Name(), opts.Exclude) {
				return fs.SkipDir
			}
			return nil
		}
		if name := d.Name(); strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tf.json") {
			tfDirs[filepath.Dir(p)] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	dirs := make([]string, 0, len(tfDirs))
	for d := range tfDirs {
		dirs = append(dirs, d)
	}
	slices.Sort(dirs)

	parsed := make([]Root, len(dirs))
	moduleDirs := map[string]bool{}
	for i, dir := range dirs {
		calls, err := hclscan.Scan(dir)
		parsed[i] = Root{Dir: dir, Calls: calls, Err: err}
		for _, c := range calls {
			if isLocal(c.Source) {
				moduleDirs[filepath.Clean(filepath.Join(dir, filepath.FromSlash(c.Source)))] = true
			}
		}
	}

	roots := parsed[:0]
	for _, r := range parsed {
		if !moduleDirs[r.Dir] {
			roots = append(roots, r)
		}
	}
	return roots, nil
}

func skip(base, dir, name string, exclude []string) bool {
	if strings.HasPrefix(name, ".") || skipDirs[name] {
		return true
	}
	rel, err := filepath.Rel(base, dir)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	for _, p := range exclude {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
		if ok, _ := path.Match(p, rel); ok {
			return true
		}
	}
	return false
}

func isLocal(source string) bool {
	return strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../")
}
