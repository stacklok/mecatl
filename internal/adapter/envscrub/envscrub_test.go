package envscrub

import (
	"slices"
	"testing"
)

func TestIsSecretName(t *testing.T) {
	secret := []string{
		// exact harness credentials
		"OPENAI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY",
		"WEBSEARCH_API_KEY", "BRAVE_API_KEY", "EXA_API_KEY",
		"MECATL_AUTH_TOKEN", "MECATL_DRIVER_AUTH_TOKEN",
		"GH_TOKEN", "GITHUB_TOKEN",
		// secret-shaped (defence-in-depth)
		"SOME_VENDOR_API_KEY", "DB_PASSWORD", "DB_PASSWD", "JWT_SECRET",
		"CI_JOB_TOKEN", "NPM_TOKEN",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AZURE_CLIENT_SECRET",
		"GOOGLE_APPLICATION_CREDENTIALS",
	}
	for _, name := range secret {
		if !IsSecretName(name) {
			t.Errorf("IsSecretName(%q) = false, want true (should be scrubbed)", name)
		}
	}

	// Toolchain / benign vars MUST survive — scrubbing these breaks builds.
	keep := []string{
		"PATH", "HOME", "GOPATH", "GOCACHE", "GOMODCACHE", "TMPDIR", "TMP",
		"LANG", "LC_ALL", "USER", "SHELL", "GOFLAGS", "GOPROXY", "GOOS",
		"TERM", "PWD", "EDITOR", "XDG_CONFIG_HOME",
		// near-misses that must NOT be caught by the patterns:
		"TOKENIZER", "APIARY", "SECRETARY_NOTES", "KEYBOARD_LAYOUT",
	}
	for _, name := range keep {
		if IsSecretName(name) {
			t.Errorf("IsSecretName(%q) = true, want false (toolchain/benign var must survive)", name)
		}
	}
}

func TestScrubDropsSecretsKeepsToolchain(t *testing.T) {
	base := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/agent",
		"GOPATH=/home/agent/go",
		"OPENROUTER_API_KEY=sk-secret",
		"OPENAI_API_KEY=sk-other",
		"ANTHROPIC_API_KEY=sk-third",
		"GH_TOKEN=ghp_xxx",
		"DB_PASSWORD=hunter2",
		"AWS_SECRET_ACCESS_KEY=zzz",
		"LANG=en_US.UTF-8",
		"malformed-no-equals", // dropped defensively
	}
	got := Scrub(base)

	mustKeep := []string{
		"PATH=/usr/bin:/bin", "HOME=/home/agent",
		"GOPATH=/home/agent/go", "LANG=en_US.UTF-8",
	}
	for _, kv := range mustKeep {
		if !slices.Contains(got, kv) {
			t.Errorf("Scrub dropped toolchain entry %q; got %v", kv, got)
		}
	}

	mustDropPrefixes := []string{
		"OPENROUTER_API_KEY=", "OPENAI_API_KEY=", "ANTHROPIC_API_KEY=",
		"GH_TOKEN=", "DB_PASSWORD=", "AWS_SECRET_ACCESS_KEY=",
	}
	for _, kv := range got {
		for _, bad := range mustDropPrefixes {
			if len(kv) >= len(bad) && kv[:len(bad)] == bad {
				t.Errorf("Scrub leaked secret entry %q", kv)
			}
		}
		if kv == "malformed-no-equals" {
			t.Errorf("Scrub kept malformed (no '=') entry %q", kv)
		}
	}
}

func TestScrubDropsFutureMecatlNames(t *testing.T) {
	const future = "MECATL_MCP_OAUTH_CREDENTIAL"
	if _, listed := DenyExact[future]; listed {
		t.Fatalf("test fixture %q must remain absent from DenyExact", future)
	}
	if !IsSecretName(future) {
		t.Fatalf("IsSecretName(%q) = false, want true", future)
	}

	base := []string{
		future + "=oauth-secret",
		"PATH=/usr/bin:/bin",
		"GOPATH=/home/agent/go",
		"TERM=xterm-256color",
	}
	got := Scrub(base)

	if slices.Contains(got, future+"=oauth-secret") {
		t.Errorf("Scrub leaked future MECATL variable %q; got %v", future, got)
	}
	for _, keep := range []string{"PATH=/usr/bin:/bin", "GOPATH=/home/agent/go", "TERM=xterm-256color"} {
		if !slices.Contains(got, keep) {
			t.Errorf("Scrub dropped benign toolchain variable %q; got %v", keep, got)
		}
	}
}
