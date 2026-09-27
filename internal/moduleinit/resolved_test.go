package moduleinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	sha, err := git(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

func TestResolvedCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	root := t.TempDir()
	projectSHA := initRepo(t, root)
	remoteSHA := initRepo(t, filepath.Join(root, ".terraform", "modules", "remote"))
	if err := os.MkdirAll(filepath.Join(root, "modules", "helpers"), 0o755); err != nil {
		t.Fatal(err)
	}

	manifest := `{"Modules":[
		{"Key":"","Source":"","Dir":"."},
		{"Key":"remote","Source":"git::https://example.com/remote.git?ref=v1.0.0","Dir":".terraform/modules/remote"},
		{"Key":"local","Source":"./modules/helpers","Dir":"modules/helpers"}
	]}`
	if err := os.WriteFile(filepath.Join(root, ".terraform", "modules", "modules.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}

	if got := m.ResolvedCommit("remote"); got != remoteSHA {
		t.Errorf("remote: got %q, want %q", got, remoteSHA)
	}
	if got := m.ResolvedCommit("local"); got != "" {
		t.Errorf("local: got %q (project sha is %q), want empty", got, projectSHA)
	}
	if got := m.ResolvedCommit("missing"); got != "" {
		t.Errorf("missing: got %q, want empty", got)
	}
}
