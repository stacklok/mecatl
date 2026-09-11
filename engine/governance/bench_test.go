package governance

import "testing"

// Package-level sinks defeat dead-code elimination: the compiler cannot prove
// the benchmarked results are unused, so the work is not optimised away.
var (
	sinkStrings  []string
	sinkBool     bool
	sinkDecision PermissionDecision
)

// BenchmarkSplitCommands measures the substitution/newline-aware command splitter
// — the security-critical first stage of every Shell permission decision. It is hot
// (runs on every Shell call) and its allocation profile drives the gated KPI.
func BenchmarkSplitCommands(b *testing.B) {
	const cmd = `cat a.go && grep -n TODO *.go | sort -u; echo done && git status`
	b.ReportAllocs()
	for b.Loop() {
		sinkStrings = SplitCommands(cmd)
	}
}

// BenchmarkReadOnlyShell measures the read-only classification of a compound command
// — the path the dispatcher uses to decide read-parallel vs mutate-serial for Shell.
func BenchmarkReadOnlyShell(b *testing.B) {
	const cmd = `git status && grep -rn TODO . | head -n 20 && go vet ./...`
	b.ReportAllocs()
	for b.Loop() {
		sinkBool = ReadOnlyShell(cmd)
	}
}

// BenchmarkSubstitutionReadOnly measures the A1 read-only-substitution classifier
// (governance.SubstitutionReadOnly) over a command with a nested $() substitution —
// the per-segment check that resolves a fully-read-only substitution by the ordinary
// fold instead of flooring at Ask.
func BenchmarkSubstitutionReadOnly(b *testing.B) {
	const seg = `go test $(git rev-parse HEAD)`
	b.ReportAllocs()
	for b.Loop() {
		sinkBool = SubstitutionReadOnly(seg)
	}
}

// BenchmarkIsolationApprovable measures the A2 isolation auto-approve classifier,
// the per-call check an isolated child runs over its proposed command.
func BenchmarkIsolationApprovable(b *testing.B) {
	const cmd = `go test ./... && go build ./... && git status`
	b.ReportAllocs()
	for b.Loop() {
		sinkBool = IsolationApprovable(cmd)
	}
}

// BenchmarkEvaluatorEvaluate measures Evaluator.Evaluate over a realistic
// deny+ask+allow rule set across the three input shapes the dispatcher hits: a
// simple read-only tool (no Shell splitting), a plain single Shell command, and a
// compound Shell command (exercises resolveShell's per-segment fold). The rule set
// mirrors a configured policy so the deny-dominant / scope-precedence resolution
// runs for real, not against an empty rule slice.
func BenchmarkEvaluatorEvaluate(b *testing.B) {
	rules := []Rule{
		{Scope: ScopeManaged, Tool: "Shell", Pattern: "rm *", Effect: Deny},
		{Scope: ScopeSharedProject, Tool: "Shell", Pattern: "git push *", Effect: Ask},
		{Scope: ScopeUser, Tool: "Shell", Pattern: "git *", Effect: Allow},
		{Scope: ScopeUser, Tool: "Read", Effect: Allow},
		{Scope: ScopeSharedProject, Tool: "Write", Effect: Ask},
		{Scope: ScopeBuiltinDefault, Tool: "Shell", Effect: Ask},
	}
	e := NewEvaluator(rules)

	b.Run("simple-tool", func(b *testing.B) {
		args := fileArgs("/proj/main.go")
		b.ReportAllocs()
		for b.Loop() {
			sinkDecision = e.Evaluate("Read", args, false)
		}
	})

	b.Run("plain-bash", func(b *testing.B) {
		args := shellArgs("git status")
		b.ReportAllocs()
		for b.Loop() {
			sinkDecision = e.Evaluate("Shell", args, false)
		}
	})

	b.Run("compound-bash", func(b *testing.B) {
		args := shellArgs("git status && git log --oneline | head -n 5")
		b.ReportAllocs()
		for b.Loop() {
			sinkDecision = e.Evaluate("Shell", args, false)
		}
	})
}
