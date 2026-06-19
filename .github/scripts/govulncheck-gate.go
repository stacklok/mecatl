//go:build ignore

// govulncheck-gate filters a govulncheck JSON stream against an accepted-risk
// allowlist and fails (exit non-zero) if any REACHABLE vulnerability is found
// whose OSV id is NOT allowlisted. It is the supply-chain CI gate for mecatl
// (issue #118).
//
// Why a wrapper: govulncheck has no native ignore/allowlist mechanism, and in
// its default text mode it exits non-zero on ANY finding — there is no way to
// accept a specific unfixable-upstream CVE without also masking new ones. So we
// run `govulncheck -format json` (which exits 0 even WITH findings — we parse
// the stream, we do NOT rely on its exit code) and apply the allowlist here.
//
// Reachability: a govulncheck `finding` message is REACHABLE when its call
// trace has a leaf function frame — i.e. trace[0].function != null. Findings
// without a function frame are module-level "you require a vulnerable module
// but no vulnerable symbol is called" notes; they are NOT a gate failure here
// (the same distinction govulncheck's "=== Symbol Results ===" draws).
//
// Fail-CLOSED: this program is a FILTER, not the runner. The caller pipes
// `govulncheck -format json ... | go run govulncheck-gate.go <allowlist...>`
// under `set -o pipefail`, so if govulncheck itself errors (build/network) the
// pipeline fails before this gate can pass vacuously. This program additionally
// fails on a malformed/empty JSON stream (no `config` preamble observed), so a
// truncated capture cannot be mistaken for "clean".
//
// Usage:
//
//	govulncheck -format json ./... | go run govulncheck-gate.go [OSV-ID...]
//
// Args are the allowlist of accepted-risk OSV ids. Pass NONE for a STRICT gate
// (the engine module: any reachable vuln reds the build). The mecatl ROOT app
// allowlists exactly two unfixable-upstream docker CVEs — see the CI job and
// docs/usage.md (supply-chain) for the documented rationale.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

// message is the subset of a govulncheck JSON stream object we care about. The
// stream is a sequence of single-key objects: {"config":…} {"progress":…}
// {"osv":…} {"finding":…} …. We decode each and inspect only `config` (preamble
// presence ⇒ a well-formed stream) and `finding`.
type message struct {
	Config  *json.RawMessage `json:"config"`
	Finding *finding         `json:"finding"`
}

type finding struct {
	OSV   string  `json:"osv"`
	Trace []frame `json:"trace"`
}

type frame struct {
	Function string `json:"function"`
}

func main() {
	allow := map[string]bool{}
	for _, id := range os.Args[1:] {
		allow[id] = true
	}

	reachable, sawConfig, err := scan(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "govulncheck-gate: cannot parse govulncheck JSON: %v\n", err)
		os.Exit(2)
	}
	// Fail-closed on a stream with no config preamble: a healthy govulncheck
	// run always emits a `config` message first, so its absence means we got a
	// truncated/empty/error stream and must NOT report "clean".
	if !sawConfig {
		fmt.Fprintln(os.Stderr, "govulncheck-gate: no govulncheck `config` message in input — refusing to pass on a malformed/empty stream (fail-closed)")
		os.Exit(2)
	}

	os.Exit(report(reachable, allow))
}

// scan reads the govulncheck JSON stream and returns the set of REACHABLE OSV
// ids (those with at least one finding carrying a function-level trace frame),
// whether a config preamble was seen, and a decode error if the stream is
// malformed.
func scan(r io.Reader) (reachable map[string]bool, sawConfig bool, err error) {
	reachable = map[string]bool{}
	dec := json.NewDecoder(r)
	for {
		var m message
		if derr := dec.Decode(&m); derr != nil {
			if derr == io.EOF {
				break
			}
			return nil, sawConfig, derr
		}
		if m.Config != nil {
			sawConfig = true
		}
		if m.Finding != nil && isReachable(m.Finding) {
			reachable[m.Finding.OSV] = true
		}
	}
	return reachable, sawConfig, nil
}

// isReachable reports whether a finding has a leaf function frame
// (trace[0].function != null) — govulncheck's own reachability signal.
func isReachable(f *finding) bool {
	return len(f.Trace) > 0 && f.Trace[0].Function != ""
}

// report prints the human-readable gate result and returns the process exit
// code: 0 when every reachable id is allowlisted, 1 when any NEW reachable id
// remains. A STALE allowlist entry (passed but not observed reachable in this
// scan — e.g. a CVE that got fixed upstream and dropped out) is reported as a
// non-fatal WARNING so the dead entry can be pruned; it never changes the exit
// code.
func report(reachable, allow map[string]bool) int {
	var observedAllowed, newIDs, staleAllowed []string
	for id := range reachable {
		if allow[id] {
			observedAllowed = append(observedAllowed, id)
		} else {
			newIDs = append(newIDs, id)
		}
	}
	for id := range allow {
		if !reachable[id] {
			staleAllowed = append(staleAllowed, id)
		}
	}
	sort.Strings(observedAllowed)
	sort.Strings(newIDs)
	sort.Strings(staleAllowed)

	if len(observedAllowed) > 0 {
		fmt.Println("govulncheck-gate: accepted-risk allowlisted vulnerabilities observed (reachable, but explicitly accepted — review periodically):")
		for _, id := range observedAllowed {
			fmt.Printf("  - %s (allowlisted)\n", id)
		}
	}

	if len(staleAllowed) > 0 {
		fmt.Println("govulncheck-gate: WARNING — allowlisted ids NOT reachable in this scan (stale? consider removing from the allowlist):")
		for _, id := range staleAllowed {
			fmt.Printf("  - %s (allowlisted but not reachable)\n", id)
		}
	}

	if len(newIDs) > 0 {
		fmt.Fprintln(os.Stderr, "govulncheck-gate: FAIL — NEW reachable vulnerabilities not on the allowlist:")
		for _, id := range newIDs {
			fmt.Fprintf(os.Stderr, "  - %s  (https://pkg.go.dev/vuln/%s)\n", id, id)
		}
		fmt.Fprintln(os.Stderr, "Fix the vulnerability (bump the dependency), or — only if there is no upstream fix and the risk is accepted — add the id to the allowlist with a dated rationale in .github/workflows/ci.yml + docs/usage.md.")
		return 1
	}

	if len(observedAllowed) == 0 {
		fmt.Println("govulncheck-gate: PASS — no reachable vulnerabilities.")
	} else {
		fmt.Println("govulncheck-gate: PASS — only accepted-risk allowlisted vulnerabilities are reachable.")
	}
	return 0
}
