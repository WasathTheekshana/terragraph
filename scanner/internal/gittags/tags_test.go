package gittags

import (
	"slices"
	"testing"
)

func TestSourceURL(t *testing.T) {
	tests := []struct {
		src  string
		want string
		ok   bool
	}{
		{"git::https://github.com/org/vpc.git?ref=v1.0.0", "https://github.com/org/vpc.git", true},
		{"git::ssh://git@github.com/org/vpc.git//modules/base?ref=v1.4.2", "ssh://git@github.com/org/vpc.git", true},
		{"git::git@gitlab.example.com:platform/vpc.git?ref=v2", "git@gitlab.example.com:platform/vpc.git", true},
		{"git@github.com:org/vpc.git//sub?ref=v1", "git@github.com:org/vpc.git", true},
		{"git::https://dev.azure.com/org/proj/_git/vpc?ref=v1", "https://dev.azure.com/org/proj/_git/vpc", true},
		{"github.com/org/vpc?ref=v1.0.0", "https://github.com/org/vpc.git", true},
		{"bitbucket.org/org/vpc.git//x", "https://bitbucket.org/org/vpc.git", true},
		{"./modules/net", "", false},
		{"../shared", "", false},
		{"terraform-aws-modules/vpc/aws", "", false},
		{"app.terraform.io/org/vpc/aws", "", false},
		{"https://example.com/vpc.zip", "", false},
		{"s3::https://s3.amazonaws.com/bucket/vpc.zip", "", false},
	}
	for _, tt := range tests {
		got, ok := SourceURL(tt.src)
		if got != tt.want || ok != tt.ok {
			t.Errorf("SourceURL(%q) = %q, %v; want %q, %v", tt.src, got, ok, tt.want, tt.ok)
		}
	}
}

func TestGitError(t *testing.T) {
	stderr := `git@github.com: Permission denied (publickey).
fatal: Could not read from remote repository.

Please make sure you have the correct access rights
and the repository exists.
`
	want := "git@github.com: Permission denied (publickey). fatal: Could not read from remote repository."
	if got := gitError(stderr); got != want {
		t.Errorf("gitError = %q, want %q", got, want)
	}
	if got := gitError("remote: Repository not found.\nfatal: repository 'https://github.com/x/y.git/' not found\n"); got != "remote: Repository not found. fatal: repository 'https://github.com/x/y.git/' not found" {
		t.Errorf("gitError = %q", got)
	}
}

func TestParse(t *testing.T) {
	out := `c3396b0aa1ad18d47677ffa2d825df03778e5d4e	refs/tags/v1.0.0
aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa	refs/tags/v2.0.0
bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb	refs/tags/v2.0.0^{}
cccccccccccccccccccccccccccccccccccccccc	refs/tags/latest
dddddddddddddddddddddddddddddddddddddddd	refs/heads/main
`
	tags := parse(out)
	var names []string
	for _, tg := range tags {
		names = append(names, tg.Name)
	}
	if !slices.Equal(names, []string{"v1.0.0", "v2.0.0", "latest"}) {
		t.Fatalf("names = %v", names)
	}
	if tags[1].CommitSHA != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("annotated tag should resolve to the peeled commit, got %s", tags[1].CommitSHA)
	}
	if tags[0].SemVer != "1.0.0" || tags[2].SemVer != "" {
		t.Errorf("semver = %q, %q", tags[0].SemVer, tags[2].SemVer)
	}
}
