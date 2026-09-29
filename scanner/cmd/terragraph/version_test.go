package main

import "testing"

func TestBuildVersionPrefersTheReleaseValue(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })

	version = "v1.2.3"
	if got := buildVersion(); got != "v1.2.3" {
		t.Errorf("buildVersion() = %q, want the injected version", got)
	}

	version = ""
	if got := buildVersion(); got == "" {
		t.Error("buildVersion() is empty without an injected version")
	}
}
