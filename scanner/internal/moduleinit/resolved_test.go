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

func TestResolve(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	root := t.TempDir()
	initRepo(t, root)
	gitSHA := initRepo(t, filepath.Join(root, ".terraform", "modules", "git_mod"))
	registrySHA := initRepo(t, filepath.Join(root, ".terraform", "modules", "registry_mod"))
	for _, dir := range []string{
		filepath.Join(root, ".terraform", "modules", "archive_mod"),
		filepath.Join(root, "modules", "helpers"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	manifest := `{"Modules":[
		{"Key":"","Source":"","Dir":"."},
		{"Key":"git_mod","Source":"git::https://example.com/git_mod.git?ref=v1.0.0","Dir":".terraform/modules/git_mod"},
		{"Key":"registry_mod","Source":"registry.terraform.io/example/mod/aws","Version":"4.11.0","Dir":".terraform/modules/registry_mod"},
		{"Key":"archive_mod","Source":"registry.terraform.io/example/archive/aws","Version":"2.0.1","Dir":".terraform/modules/archive_mod"},
		{"Key":"local","Source":"./modules/helpers","Dir":"modules/helpers"}
	]}`
	if err := os.WriteFile(filepath.Join(root, ".terraform", "modules", "modules.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		call   string
		want   Resolution
		wantOK bool
	}{
		{"git_mod", Resolution{Commit: gitSHA}, true},
		{"registry_mod", Resolution{Commit: registrySHA, Version: "4.11.0"}, true},
		{"archive_mod", Resolution{Version: "2.0.1"}, true},
		// A local module sits inside the project's repo; its commit is the
		// project's, not the module's.
		{"local", Resolution{}, false},
		{"missing", Resolution{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.call, func(t *testing.T) {
			got, ok := m.Resolve(tt.call)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("Resolve(%q) = %+v, %v; want %+v, %v", tt.call, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestResolveNilManifest(t *testing.T) {
	var m *Manifest
	if _, ok := m.Resolve("anything"); ok {
		t.Error("nil manifest resolved a module")
	}
	if _, ok := m.Dir("anything"); ok {
		t.Error("nil manifest has a module dir")
	}
}

func TestDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".terraform", "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"Modules":[
		{"Key":"eks","Source":"x","Dir":".terraform/modules/eks"},
		{"Key":"eks.kms","Source":"y","Dir":".terraform/modules/eks.kms"}
	]}`
	if err := os.WriteFile(filepath.Join(root, ".terraform", "modules", "modules.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if dir, ok := m.Dir("eks.kms"); !ok || dir != filepath.Join(root, ".terraform", "modules", "eks.kms") {
		t.Errorf("Dir(eks.kms) = %q, %v", dir, ok)
	}
	if _, ok := m.Dir("missing"); ok {
		t.Error("Dir of an unknown call")
	}
}
