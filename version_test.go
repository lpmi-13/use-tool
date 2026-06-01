package main

import (
	"os"
	"strings"
	"testing"
)

func TestResolveVersionPrefersInjectedReleaseTag(t *testing.T) {
	got := resolveVersion("v0.6.1", "v0.6.0")
	if got != "0.6.1" {
		t.Fatalf("resolveVersion() = %q, want %q", got, "0.6.1")
	}
}

func TestResolveVersionFallsBackToModuleVersion(t *testing.T) {
	got := resolveVersion("dev", "v0.6.1")
	if got != "0.6.1" {
		t.Fatalf("resolveVersion() = %q, want %q", got, "0.6.1")
	}
}

func TestResolveVersionDefaultsToDev(t *testing.T) {
	got := resolveVersion("dev", "(devel)")
	if got != "dev" {
		t.Fatalf("resolveVersion() = %q, want %q", got, "dev")
	}
}

func TestDisplayVersionOnlyStripsSemverTagPrefix(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"v0.6.1", "0.6.1"},
		{"0.6.1", "0.6.1"},
		{"version-check", "version-check"},
		{"(devel)", ""},
	}
	for _, tc := range cases {
		if got := displayVersion(tc.input); got != tc.want {
			t.Fatalf("displayVersion(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestVersionCommandUsesResolvedVersion(t *testing.T) {
	oldArgs := os.Args
	oldVersion := version
	defer func() {
		os.Args = oldArgs
		version = oldVersion
	}()

	os.Args = []string{"use-tool", "version"}
	version = "v1.2.3"

	out := captureStdout(main)
	got := strings.TrimSpace(out)
	if got != "use-tool 1.2.3" {
		t.Fatalf("version output = %q, want %q", got, "use-tool 1.2.3")
	}
}
