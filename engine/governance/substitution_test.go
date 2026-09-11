package governance

import "testing"

// A NOTE ON DESTRUCTIVE STAND-INS (repo memory: no-destructive-strings-in-test-literals):
// these tests never use a real destructive command literal. A "non-read-only inner"
// is modelled with the innocuous, UNRECOGNISED verb `zap` — it is not in readOnlyVerbs,
// so simpleReadOnly/ReadOnlyShell classify it not-read-only exactly as a real mutating
// verb would, but it does nothing if ever run. We assert on the CLASSIFICATION effect,
// not on any destructive literal.

func TestSubstitutionReadOnly(t *testing.T) {
	cases := []struct {
		name string
		seg  string
		want bool
	}{
		// Positive: read-only inner + read-only outer.
		{"cat of ls", "cat $(ls)", true},
		{"echo of git rev-parse", "echo $(git rev-parse HEAD)", true},
		{"backtick read-only", "echo `git rev-parse HEAD`", true},
		{"process substitution read-only", "diff <(cat a) <(cat b)", true},
		{"nested read-only", "echo $(cat $(ls))", true},
		{"subshell read-only", "(git status)", true},
		// Negative: a non-read-only inner (innocuous stand-in `zap`).
		{"non-read-only inner", "cat $(zap)", false},
		{"non-read-only inner nested", "echo $(cat $(zap))", false},
		{"backtick non-read-only", "echo `zap`", false},
		{"process substitution non-read-only", "diff <(zap) f", false},
		// Negative: outer verb is not read-only even though inner is.
		{"non-read-only outer", "zap $(ls)", false},
		// Negative: outer redirection makes it a write.
		{"outer redirect", "cat $(ls) > out", false},
		// Negative: substitution-as-verb (placeholder becomes the verb → unknown).
		{"substitution as verb", "$(echo ls)", false},
		// Negative: ambiguous / unbalanced extraction fails safe.
		{"unbalanced paren", "cat $(ls", false},
		{"unterminated backtick", "echo `ls", false},
		{"parameter expansion", "cat ${HOME}", false},
		{"group command", "{ ls; }", false},
		// Negative: no substitution at all → classifier does not apply.
		{"no substitution", "cat f", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SubstitutionReadOnly(tc.seg); got != tc.want {
				t.Fatalf("SubstitutionReadOnly(%q) = %v; want %v", tc.seg, got, tc.want)
			}
		})
	}
}

func TestExtractSubstitutions(t *testing.T) {
	cases := []struct {
		name      string
		seg       string
		wantOK    bool
		wantInner []string
	}{
		{"none", "cat f", true, nil},
		{"single", "cat $(ls)", true, []string{"ls"}},
		{"two", "echo $(ls) $(pwd)", true, []string{"ls", "pwd"}},
		{"nested", "echo $(cat $(ls))", true, []string{"cat $(ls)", "ls"}},
		{"backtick", "echo `ls`", true, []string{"ls"}},
		{"process sub", "diff <(cat a) f", true, []string{"cat a"}},
		{"subshell", "(git status)", true, []string{"git status"}},
		{"unbalanced", "cat $(ls", false, nil},
		{"unterminated backtick", "echo `ls", false, nil},
		{"param expansion", "cat ${HOME}", false, nil},
		{"group command", "{ ls; }", false, nil},
		{"quoted paren is literal", "echo '$(ls)'", true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner, ok := extractSubstitutions(tc.seg)
			if ok != tc.wantOK {
				t.Fatalf("extractSubstitutions(%q) ok = %v; want %v (inner=%v)", tc.seg, ok, tc.wantOK, inner)
			}
			if !ok {
				return
			}
			if len(inner) != len(tc.wantInner) {
				t.Fatalf("extractSubstitutions(%q) inner = %v; want %v", tc.seg, inner, tc.wantInner)
			}
			for i := range inner {
				if inner[i] != tc.wantInner[i] {
					t.Fatalf("extractSubstitutions(%q) inner[%d] = %q; want %q", tc.seg, i, inner[i], tc.wantInner[i])
				}
			}
		})
	}
}

func TestOuterBlanked(t *testing.T) {
	cases := []struct {
		name   string
		seg    string
		wantOK bool
		want   string
	}{
		{"simple", "cat $(ls)", true, "cat " + substitutionPlaceholder},
		{"two", "echo $(ls) $(pwd)", true, "echo " + substitutionPlaceholder + " " + substitutionPlaceholder},
		{"backtick", "echo `ls`", true, "echo " + substitutionPlaceholder},
		{"verb position", "$(echo ls) foo", true, substitutionPlaceholder + " foo"},
		{"subshell", "(git status)", true, substitutionPlaceholder},
		{"unbalanced", "cat $(ls", false, ""},
		{"param expansion", "cat ${HOME}", false, ""},
		{"no subst", "cat f", true, "cat f"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := outerWithSubstitutionsBlanked(tc.seg)
			if ok != tc.wantOK {
				t.Fatalf("outerWithSubstitutionsBlanked(%q) ok = %v; want %v", tc.seg, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got != tc.want {
				t.Fatalf("outerWithSubstitutionsBlanked(%q) = %q; want %q", tc.seg, got, tc.want)
			}
		})
	}
}

func TestIsolationApprovable(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want bool
	}{
		// Worktree-safe go verbs.
		{"go test", "go test ./...", true},
		{"go build", "go build ./...", true},
		{"go vet", "go vet ./...", true},
		{"go list", "go list ./...", true},
		// Read-only stays approvable.
		{"read-only ls", "ls -la", true},
		{"git status", "git status", true},
		// The motivating per-package coverage loop (substitution + go test).
		{"coverage loop", `for pkg in $(go list ./...); do go test -cover "$pkg"; done`, true},
		{"go test of go list", "go test $(go list ./...)", true},
		// Minimal set: not-listed go verbs surface (false).
		{"go run", "go run main.go", false},
		{"go get", "go get ./...", false},
		{"go mod tidy", "go mod tidy", false},
		{"make", "make build", false},
		// Worktree-escape verbs are rejected even though isolated.
		{"git push", "git push origin main", false},
		{"git config", "git config user.name x", false},
		{"git remote", "git remote add o url", false},
		{"git fetch", "git fetch origin", false},
		// S2: worktree/submodule are escape verbs too (add/rewrite checkouts outside the
		// throwaway tree).
		{"git worktree add", "git worktree add /tmp/x", false},
		{"git submodule update", "git submodule update --init", false},
		// S2: a global flag shifts the subcommand right — the escape check must skip
		// leading global flags before identifying it, so these do NOT slip past.
		{"git -C escape push", "git -C /outside push origin main", false},
		{"git --git-dir escape config", "git --git-dir=/x config user.name y", false},
		// S2: a PATH-bearing global flag points git OUTSIDE the worktree → not approvable
		// even for a read-only subcommand (else `git -C /outside log` reads outside).
		{"git -C path read", "git -C /outside log", false},
		{"git --git-dir read", "git --git-dir=/x status", false},
		{"git --work-tree read", "git --work-tree=/y status", false},
		// A plain git read-only subcommand with no global flag stays approvable.
		{"git log plain", "git log --oneline", true},
		// S1: go test/build flags that run an ARBITRARY EXTERNAL program (not the repo's
		// own code) escape the worktree's filesystem isolation → not approvable. Uses an
		// innocuous stand-in path/program, never a destructive literal.
		{"go test -exec space", "go test -exec /tmp/zap ./...", false},
		{"go test -exec eq", "go test -exec=/tmp/zap ./...", false},
		{"go test -toolexec", "go test -toolexec=/tmp/zap ./...", false},
		{"go build -toolexec", "go build -toolexec /tmp/zap ./...", false},
		{"go test -overlay", "go test -overlay=/tmp/ov.json ./...", false},
		{"go build -overlay", "go build -overlay /tmp/ov.json ./...", false},
		// -o writes the compiled binary to an arbitrary (possibly absolute / ../-escaping)
		// path OUTSIDE the throwaway worktree (e.g. a cron/autostart dir) → escapes
		// isolation, not approvable. Innocuous stand-in paths, never a destructive literal.
		{"go build -o abs", "go build -o /tmp/zapbin ./...", false},
		{"go build -o eq", "go build -o=/tmp/zapbin ./...", false},
		{"go build -o escape", "go build -o ../../zapbin ./...", false},
		{"go test -o", "go test -o /tmp/zapbin ./...", false},
		// Plain go test/build flags that do NOT escape the worktree stay approvable.
		{"go test -cover -run", "go test -cover -run TestX ./...", true},
		// A destructive inner (innocuous stand-in) is rejected even with a go-test outer.
		{"hidden non-ro inner", "go test $(zap)", false},
		{"non-ro segment in compound", "go test ./... && zap", false},
		// Output redirection on a go verb makes it a write.
		{"go test redirect", "go test ./... > out", false},
		// Ambiguous extraction fails safe.
		{"unbalanced", "go test $(go list", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsolationApprovable(tc.cmd); got != tc.want {
				t.Fatalf("IsolationApprovable(%q) = %v; want %v", tc.cmd, got, tc.want)
			}
		})
	}
}
