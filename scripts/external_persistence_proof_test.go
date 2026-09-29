package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the shell's bootstrap and comparison policy without allowing any Go
// invocation to reach a network. The real candidate/tidy boundary is exercised
// by prove-external-persistence.sh itself.
func TestExternalPersistenceProof(t *testing.T) {
	script, err := os.ReadFile("prove-external-persistence.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, drift, remove, wantFailure string
	}{
		{name: "release_adapter_sums"},
		{name: "support_sum_drift", drift: "internal/adaptersupport", wantFailure: "unexpected-sum-drift"},
		{name: "driver_sum_drift", drift: "contracts/gen/go/mecatl/driver", wantFailure: "unexpected-sum-drift"},
		{name: "raw_adapter_comparison_mutation", remove: "[ \"$mod\" != adapters ] && ", wantFailure: "v0.1.0-dev"},
		{name: "bootstrap_poisoning_mutation", remove: " GOPRIVATE=none GONOPROXY=none GONOSUMDB=none", wantFailure: "unsafe Go bootstrap environment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, err := filepath.Abs("../.scratch")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(base, 0o755); err != nil {
				t.Fatal(err)
			}
			repo, err := os.MkdirTemp(base, "persistence-script-test-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(repo); err != nil {
					t.Error(err)
				}
			})
			write := func(path, content string) {
				t.Helper()
				path = filepath.Join(repo, path)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			text := string(script)
			if tc.remove != "" {
				if !strings.Contains(text, tc.remove) {
					t.Fatal("mutation target missing")
				}
				text = strings.Replace(text, tc.remove, "", 1)
			}
			write("scripts/prove-external-persistence.sh", text)
			for _, mod := range []string{"internal/adaptersupport", "contracts/gen/go/mecatl/driver", "adapters"} {
				write(mod+"/go.mod", "module github.com/stacklok/mecatl/"+mod+"\n")
				write(mod+"/go.sum", "example.org/dependency v1.0.0 h1:fixture\n")
			}
			// These representative release-version entries exist only in the
			// disposable fixture, never as published checksums in the checkout.
			releaseSums := "github.com/stacklok/mecatl/internal/adaptersupport v0.1.0 h1:fixture\ngithub.com/stacklok/mecatl/contracts/gen/go/mecatl/driver v0.1.0 h1:fixture\n"
			write("adapters/go.sum", releaseSums)
			write("candidate.sum", strings.ReplaceAll(releaseSums, "v0.1.0 ", "v0.1.0-dev "))
			write("bin/go", `#!/bin/sh
set -eu
if [ "$GOPRIVATE" != none ] || [ "$GONOPROXY" != none ] || [ "$GONOSUMDB" != none ] || [ "$GOSUMDB" != off ]; then
 echo 'unsafe Go bootstrap environment' >&2; exit 97
fi
case "$1" in
 env)
  [ "$GOPROXY" = off ]
  case "$2" in GOMODCACHE) printf '%s/cache\n' "$FIXTURE_ROOT";; GOROOT) printf '%s\n' "$FIXTURE_ROOT";; *) exit 98;; esac ;;
 run)
  [ "$GOPROXY" = off ]
  out=$4
  for mod in internal/adaptersupport contracts/gen/go/mecatl/driver adapters; do
   mkdir -p "$out/source/$mod"
   cp "$FIXTURE_ROOT/$mod/go.mod" "$FIXTURE_ROOT/$mod/go.sum" "$out/source/$mod/"
  done
  mkdir -p "$out/engine-only" "$out/consumer" ;;
 mod)
  case "$2" in
   tidy)
    case "$PWD" in */source/adapters) cp "$FIXTURE_ROOT/candidate.sum" go.sum;; esac
    if [ -n "$PROOF_DRIFT" ] && [ "$PWD" = "$FIXTURE_ROOT/.scratch/proof/source/$PROOF_DRIFT" ]; then
     printf 'unexpected-sum-drift\n' >> go.sum
    fi ;;
   download) ;;
   *) exit 98 ;;
  esac ;;
 test) ;;
 list) printf 'example.org/consumer\n' ;;
 *) exit 98 ;;
esac
`)
			cmd := exec.Command("sh", "scripts/prove-external-persistence.sh", ".scratch/proof")
			cmd.Dir = repo
			cmd.Env = append(os.Environ(), "FIXTURE_ROOT="+repo, "PROOF_DRIFT="+tc.drift, "PATH="+filepath.Join(repo, "bin")+":"+os.Getenv("PATH"), "GOCACHE=", "GOPRIVATE=*", "GONOPROXY=*", "GONOSUMDB=*")
			output, err := cmd.CombinedOutput()
			if tc.wantFailure == "" {
				if err != nil || !strings.Contains(string(output), "PASS: candidate proxy") {
					t.Fatalf("proof: %v\n%s", err, output)
				}
			} else if err == nil || !strings.Contains(string(output), tc.wantFailure) {
				t.Fatalf("expected failure %q: %v\n%s", tc.wantFailure, err, output)
			}
		})
	}
}
