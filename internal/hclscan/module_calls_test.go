package hclscan

import "testing"

func TestScanSampleProject(t *testing.T) {
	calls, err := Scan("../../testdata/sample-project")
	if err != nil {
		t.Fatal(err)
	}

	want := []ModuleCall{
		{CallName: "vpc", RefDeclared: "v5.1.0", File: "main.tf", Line: 1},
		{CallName: "network_policies", RefDeclared: "v1.4.2", File: "main.tf", Line: 5},
		{CallName: "eks", RefDeclared: "~> 20.0", File: "main.tf", Line: 9},
		{CallName: "local_helpers", RefDeclared: "", File: "main.tf", Line: 14},
		{CallName: "s3_bucket", RefDeclared: "v3.15.1", File: "storage.tf", Line: 1},
	}

	if len(calls) != len(want) {
		t.Fatalf("got %d calls, want %d: %+v", len(calls), len(want), calls)
	}
	for i, w := range want {
		g := calls[i]
		if g.CallName != w.CallName || g.RefDeclared != w.RefDeclared || g.File != w.File || g.Line != w.Line {
			t.Errorf("call %d: got {%s %q %s:%d}, want {%s %q %s:%d}",
				i, g.CallName, g.RefDeclared, g.File, g.Line, w.CallName, w.RefDeclared, w.File, w.Line)
		}
	}
}
