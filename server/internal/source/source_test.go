package source

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		src      string
		wantKind Kind
		wantKey  string
	}{
		{"./modules/helpers", KindLocal, ""},
		{"../shared/vpc", KindLocal, ""},

		{"git::https://github.com/Org/tf-module-vpc.git?ref=v2.1.0", KindGit, "github.com/org/tf-module-vpc"},
		{"git::ssh://git@github.com/org/tf-module-vpc.git//modules/base?ref=v1.4.2", KindGit, "github.com/org/tf-module-vpc"},
		{"git::ssh://git@gitlab.example.com:2222/platform/vpc.git", KindGit, "gitlab.example.com/platform/vpc"},
		{"git@github.com:org/tf-module-vpc.git?ref=v1.0.0", KindGit, "github.com/org/tf-module-vpc"},
		{"git::git@github.com:org/tf-module-vpc.git//sub?ref=v1.0.0", KindGit, "github.com/org/tf-module-vpc"},
		{"github.com/org/tf-module-vpc?ref=v1.0.0", KindGit, "github.com/org/tf-module-vpc"},
		{"bitbucket.org/org/tf-module-vpc", KindGit, "bitbucket.org/org/tf-module-vpc"},

		{"terraform-aws-modules/s3-bucket/aws", KindRegistry, "registry.terraform.io/terraform-aws-modules/s3-bucket/aws"},
		{"terraform-aws-modules/eks/aws//modules/karpenter", KindRegistry, "registry.terraform.io/terraform-aws-modules/eks/aws"},
		{"registry.terraform.io/terraform-aws-modules/s3-bucket/aws", KindRegistry, "registry.terraform.io/terraform-aws-modules/s3-bucket/aws"},
		{"app.terraform.io/Example-Org/vpc/aws", KindRegistry, "app.terraform.io/example-org/vpc/aws"},

		{"s3::https://s3.amazonaws.com/bucket/vpc.zip", KindOther, "s3.amazonaws.com/bucket/vpc.zip"},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			kind, key := Parse(tt.src)
			if kind != tt.wantKind || key != tt.wantKey {
				t.Errorf("Parse(%q) = %q, %q; want %q, %q", tt.src, kind, key, tt.wantKind, tt.wantKey)
			}
		})
	}
}

func TestRepoKeyMatchesAcrossForms(t *testing.T) {
	forms := []string{
		"https://github.com/org/tf-module-vpc.git",
		"https://github.com/org/tf-module-vpc",
		"git@github.com:org/tf-module-vpc.git",
		"ssh://git@github.com/org/tf-module-vpc.git",
		"git::https://github.com/org/tf-module-vpc.git?ref=v3.0.0",
		"HTTPS://GitHub.com/Org/tf-module-vpc.git/",
	}
	const want = "github.com/org/tf-module-vpc"
	for _, f := range forms {
		if got := RepoKey(f); got != want {
			t.Errorf("RepoKey(%q) = %q, want %q", f, got, want)
		}
	}
}
