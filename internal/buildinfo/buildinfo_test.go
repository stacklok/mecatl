package buildinfo

import (
	"bytes"
	"runtime/debug"
	"testing"
)

func TestResolveBuildID(t *testing.T) {
	revision := "0123456789abcdef0123456789abcdef01234567"
	for _, tt := range []struct {
		name     string
		buildID  string
		settings []debug.BuildSetting
		want     string
	}{
		{name: "explicit release wins", buildID: "v1.2.3", want: "v1.2.3"},
		{name: "explicit development stamp wins", buildID: "dev", settings: []debug.BuildSetting{{Key: "vcs.revision", Value: revision}}, want: "dev"},
		{name: "explicit non-release wins", buildID: "custom", want: "custom"},
		{name: "unstamped without metadata", want: "dev"},
		{name: "unstamped clean revision", settings: []debug.BuildSetting{{Key: "vcs.revision", Value: revision}}, want: "dev+0123456789ab"},
		{name: "unstamped dirty revision", settings: []debug.BuildSetting{{Key: "vcs.revision", Value: revision}, {Key: "vcs.modified", Value: "true"}}, want: "dev+0123456789ab.dirty"},
		{name: "unstamped short revision", settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "0123456789a"}}, want: "dev"},
		{name: "unstamped malformed revision", settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "0123456789ab-not-a-revision"}}, want: "dev"},
		{name: "VCS time is ignored", settings: []debug.BuildSetting{{Key: "vcs.time", Value: "2026-01-01T00:00:00Z"}}, want: "dev"},
		{name: "disabled metadata", settings: []debug.BuildSetting{{Key: "vcs.revision", Value: ""}}, want: "dev"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveBuildID(tt.buildID, tt.settings); got != tt.want {
				t.Errorf("resolveBuildID(%q, %v) = %q, want %q", tt.buildID, tt.settings, got, tt.want)
			}
		})
	}
}

func TestVersionActionIsExactAndSideEffectFree(t *testing.T) {
	for _, args := range [][]string{{"mecated", "--version"}, {"mecak8s", "--version"}, {"mecatequi", "--version"}, {"mecademo", "--version"}, {"mecatui", "--version"}} {
		if !IsVersion(args) {
			t.Fatalf("IsVersion(%q) = false, want true", args)
		}
	}
	for _, args := range [][]string{{"mecated"}, {"mecated", "--version", "extra"}, {"mecated", "--VERSION"}, {"mecated", "run", "--version"}} {
		if IsVersion(args) {
			t.Errorf("IsVersion(%q) = true, want false", args)
		}
	}

	old := BuildID
	BuildID = "test-build"
	t.Cleanup(func() { BuildID = old })
	for _, name := range []string{"mecated", "mecak8s", "mecatequi", "mecademo", "mecatui"} {
		var out bytes.Buffer
		PrintVersion(&out, name)
		if got, want := out.String(), name+" test-build\n"; got != want {
			t.Errorf("PrintVersion(%q) = %q, want %q", name, got, want)
		}
	}

	BuildID = "dev+0123456789ab.dirty"
	var out bytes.Buffer
	PrintVersion(&out, "mecatui")
	if got, want := out.String(), "mecatui dev+0123456789ab.dirty\n"; got != want {
		t.Errorf("PrintVersion resolved development build = %q, want %q", got, want)
	}
}
