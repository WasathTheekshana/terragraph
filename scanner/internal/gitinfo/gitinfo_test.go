package gitinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindRoot(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "org", "app")
	nested := filepath.Join(repo, "envs", "prod")
	worktree := filepath.Join(base, "wt")
	for _, d := range []string{filepath.Join(repo, ".git"), nested, worktree} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: elsewhere"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		dir, want string
		ok        bool
	}{
		{nested, repo, true},
		{repo, repo, true},
		{worktree, worktree, true},
		{filepath.Join(base, "org"), "", false},
	}
	for _, tt := range tests {
		got, ok := FindRoot(tt.dir)
		// base may itself sit inside a checkout on some machines; only
		// check results within base.
		if !tt.ok && ok && !isWithin(got, base) {
			continue
		}
		if got != tt.want || ok != tt.ok {
			t.Errorf("FindRoot(%s) = %q, %v; want %q, %v", tt.dir, got, ok, tt.want, tt.ok)
		}
	}
}

func isWithin(p, base string) bool {
	rel, err := filepath.Rel(base, p)
	return err == nil && !strings.HasPrefix(rel, "..")
}

func TestRead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"remote", "add", "origin", "git@github.com:org/app.git"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	r := Read(dir)
	if r.RemoteURL != "git@github.com:org/app.git" || r.Branch != "main" || len(r.CommitSHA) != 40 {
		t.Errorf("Read = %+v", r)
	}
}
