// Package envscrub builds the SECRET-neutralised process environment every
// agent-facing command shell runs with. It is a stdlib-only leaf — it imports
// nothing from the rest of the codebase — so the composition root can layer it
// under the git-neutralising gitenv.Scrub without creating an adapter→adapter
// edge.
//
// The threat it addresses (security review "Finding B"): the Shell tool runs a
// child shell, and under posture `auto`/`yolo` (allow-all) the model can run
// `echo $OPENROUTER_API_KEY` or `cat /proc/self/environ` and exfiltrate the
// provider/auth credentials the harness was started with via a tool result or a
// committed file. The osfs Read tool is workspace-confined, but the shell child
// inherited os.Environ() VERBATIM, so the prompt fence did not contain it. This
// package removes the harness's credentials from the child env BEFORE the shell
// ever sees them.
//
// Policy (a precise DENYLIST, not an allowlist): the child environment is
// os.Environ() MINUS
//
//   - every MECATL_* variable, including explicit harness credentials, so agent-facing
//     shells cannot inherit harness-owned configuration or credentials (the MECATL_ prefix); and
//   - any variable whose NAME matches a conservative secret-SHAPED pattern
//     (DenyPattern — *_API_KEY / *_TOKEN / *_SECRET / *_PASSWORD / *_PASSWD /
//     AWS_* / AZURE_* / GOOGLE_APPLICATION_CREDENTIALS) as defence-in-depth.
//
// A denylist (not an allowlist) is deliberate: a coding agent runs `go build`,
// `go test`, and `git`, which need PATH, HOME, GOPATH, GOCACHE, GOMODCACHE,
// TMPDIR, LANG and an open-ended set of toolchain variables to function. An
// allowlist would have to enumerate that set and would silently break a build the
// moment a tool needed an env var nobody listed. We are precise about the secrets
// WE injected (DenyExact) and conservative about secret-SHAPED names we did not
// inject (DenyPattern); everything else — the whole toolchain — survives.
package envscrub

import "strings"

// DenyExact is the canonical set of EXACT environment-variable names the harness
// reads as credentials or harness-owned configuration. These are the keys WE inject, so
// there is no guessing. Every MECATL_* name is scrubbed by IsSecretName, including future
// environment-backed credentials that are not individually listed here.
// The provider keys are the same names internal/cliconfig.ReadProviderKeys reads;
// the websearch/auth/driver-auth tokens are read by cmd/mecated. Keeping the list
// here (a leaf with no cmd dependency) lets the composition root scrub them out of
// the agent shell without importing cmd or cliconfig.
//
// They are matched case-SENSITIVELY (env-var names are conventionally
// upper-case and the harness reads these exact spellings).
var DenyExact = map[string]struct{}{
	// Provider credentials (cliconfig.ReadProviderKeys).
	"OPENAI_API_KEY":     {},
	"OPENROUTER_API_KEY": {},
	"ANTHROPIC_API_KEY":  {},
	"OPENCODE_API_KEY":   {},
	"TYPESAFE_API_KEY":   {},
	// WebSearch backend credentials (cmd/mecated).
	"WEBSEARCH_API_KEY": {},
	"BRAVE_API_KEY":     {},
	"EXA_API_KEY":       {},
	// Harness / driver auth tokens (cmd/mecated, cmd/mecatui).
	"MECATL_AUTH_TOKEN":        {},
	"MECATL_DRIVER_AUTH_TOKEN": {},
	// Common forge tokens the harness may have been handed (the operator can run
	// the agent in a repo where `gh`/git read these). They are not strictly
	// "harness credentials", but they ARE high-value secrets that an exfiltrating
	// agent should not be able to read out of the environment; the agent's own git
	// operations authenticate via the on-disk git credential helper, not these.
	"GH_TOKEN":     {},
	"GITHUB_TOKEN": {},
}

// NonOverridableExact names credentials read by Mecatl that an inheritance grant cannot restore.
var NonOverridableExact = map[string]struct{}{
	"OPENAI_API_KEY": {}, "OPENROUTER_API_KEY": {}, "ANTHROPIC_API_KEY": {}, "OPENCODE_API_KEY": {}, "TYPESAFE_API_KEY": {},
	"WEBSEARCH_API_KEY": {}, "BRAVE_API_KEY": {}, "EXA_API_KEY": {},
}

// denyPatternSuffixes are case-SENSITIVE name SUFFIXES that mark a variable as
// secret-shaped (defence-in-depth for credentials the harness did not inject —
// e.g. a sibling tool's KEY).
var denyPatternSuffixes = []string{
	"_API_KEY",
	"_TOKEN",
	"_SECRET",
	"_PASSWORD",
	"_PASSWD",
}

// denyPatternPrefixes are case-SENSITIVE name PREFIXES for common cloud
// credential families.
var denyPatternPrefixes = []string{
	"AWS_",
	"AZURE_",
}

// IsSecretName reports whether an environment-variable name should be scrubbed
// from the agent shell: it has the MECATL_ prefix, is in DenyExact, matches a
// secret-shaped suffix or prefix, or is one of the few fixed cloud-credential names that
// fit no pattern.
func IsSecretName(name string) bool {
	if strings.HasPrefix(name, "MECATL_") {
		return true
	}
	if _, ok := DenyExact[name]; ok {
		return true
	}
	for _, suf := range denyPatternSuffixes {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	for _, pre := range denyPatternPrefixes {
		if strings.HasPrefix(name, pre) {
			return true
		}
	}
	// Fixed cloud-credential name that matches no suffix/prefix pattern.
	return name == "GOOGLE_APPLICATION_CREDENTIALS"
}

// ScrubWithInherited applies Scrub but restores only explicitly named secret-shaped
// variables that are not reserved to the harness. It never synthesizes an absent value.
func ScrubWithInherited(base, inherit []string, reserved map[string]struct{}) []string {
	allowed := make(map[string]struct{}, len(inherit))
	for _, name := range inherit {
		if strings.HasPrefix(name, "MECATL_") {
			continue
		}
		if _, blocked := reserved[name]; !blocked {
			allowed[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(base))
	for _, kv := range base {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		name := kv[:i]
		if !IsSecretName(name) {
			out = append(out, kv)
			continue
		}
		if _, ok := allowed[name]; ok {
			out = append(out, kv)
		}
	}
	return out
}

// Scrub returns a process environment derived from base (typically os.Environ())
// with every secret variable (per IsSecretName) DROPPED. Every other inherited
// variable — PATH, HOME, GOPATH, GOCACHE, GOMODCACHE, TMPDIR, LANG, and the rest
// of the toolchain — is kept verbatim, so `go build`/`go test`/`git` still work.
// A malformed entry with no '=' is dropped defensively (exec would reject it).
func Scrub(base []string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue // malformed; drop
		}
		if IsSecretName(kv[:i]) {
			continue // secret; drop
		}
		out = append(out, kv)
	}
	return out
}
