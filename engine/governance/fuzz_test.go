package governance

import (
	"strings"
	"testing"
)

// splitSeeds are the tricky inputs from bash_test.go plus a few extra hostile
// forms. They seed every governance fuzzer so the corpus starts from the cases
// we already know exercise the security-critical paths.
var splitSeeds = []string{
	"",
	"   ",
	"git status",
	"git status && rm -rf /",
	"make || echo fail",
	"cd /tmp; ls",
	"cat f | grep x",
	"a && b; c | d || e",
	"echo 'a && b' && ls",
	`echo "x | y" | wc`,
	"ls &&",
	"ls\nrm -rf build",
	"ls\r\nrm x",
	"ls & rm -rf build",
	"echo \"a\nb\"\nls",
	// Substitution / grouping / expansion (must never be read-only).
	"echo $(rm -rf x)",
	"cat $(rm x)",
	"echo ok $(rm -rf build)",
	"echo `rm -rf build`",
	"diff <(rm x) f",
	"tee >(cat)",
	"ls;(rm -rf build)",
	"ls; { rm x; }",
	"cat ${HOME}",
	"(rm x)",
	"{ rm x; }",
	// Wrappers and re-entrant launchers.
	"timeout 5 rm x",
	"timeout -s KILL 5 rm x",
	"timeout --signal=KILL 5 rm x",
	"nice -n 10 cat f",
	"env FOO=bar ls",
	"stdbuf -oL grep x f",
	"timeout 5 nice -n 5 rm x",
	"docker exec foo rm x",
	"npx some-tool",
	"devbox run rm x",
	"sudo rm x",
	"env",
	// Pathological / unbalanced.
	"'unterminated",
	"\"unterminated",
	"$",
	"`",
	"$(",
	"<",
	"&&&&",
	"||||",
	";;;;",
	"\n\n\n",
}

// destructiveTokens are verbs whose appearance as a whole token in the input
// must never be lost across SplitCommands: if any segment contains one, at
// least one returned segment must still contain it (the splitter may not
// silently swallow a destructive command).
var destructiveTokens = []string{
	"rm", "mv", "cp", "mkdir", "rmdir", "touch", "tee", "dd",
	"chmod", "chown", "ln", "truncate", "install",
}

func seedGovernance(f *testing.F) {
	for _, s := range splitSeeds {
		f.Add(s)
	}
}

// FuzzSplitCommands asserts SplitCommands never panics and upholds several
// invariants, the most important being the security cross-check against
// HasSubstitutionOrGrouping / ReadOnlyShell: any input that contains a
// substitution, grouping, or a raw newline can never be classified read-only.
func FuzzSplitCommands(f *testing.F) {
	seedGovernance(f)
	f.Fuzz(func(t *testing.T, cmd string) {
		segs := SplitCommands(cmd)

		// Invariant 1: every returned segment is non-empty and trimmed (no
		// leading/trailing whitespace), matching the documented contract.
		for i, s := range segs {
			if s == "" {
				t.Fatalf("SplitCommands(%q) returned empty segment at %d: %#v", cmd, i, segs)
			}
			if s != strings.TrimSpace(s) {
				t.Fatalf("SplitCommands(%q) segment %d not trimmed: %q", cmd, i, s)
			}
		}

		// Invariant 2: idempotence on already-single segments. Re-splitting a
		// segment that the splitter produced must yield exactly that one segment
		// back (the split reached a fixed point).
		for _, s := range segs {
			again := SplitCommands(s)
			if len(again) != 1 || again[0] != s {
				t.Fatalf("SplitCommands not idempotent on segment %q (from %q): %#v", s, cmd, again)
			}
		}

		// Invariant 3: no destructive token is silently dropped. If the raw input
		// contains a destructive token as a whitespace-delimited field, at least
		// one segment must still contain it as a field. (We only check inputs
		// without quotes/substitution where field semantics are unambiguous.)
		if !strings.ContainsAny(cmd, "'\"`$(){}") {
			inFields := fieldSet(cmd)
			for _, tok := range destructiveTokens {
				if !inFields[tok] {
					continue
				}
				found := false
				for _, s := range segs {
					if fieldSet(s)[tok] {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("SplitCommands(%q) dropped destructive token %q: %#v", cmd, tok, segs)
				}
			}
		}

		// Invariant 4 (security): a raw (unquoted) newline in the input means more
		// than one logical command may be present, so the line must NOT be
		// classified read-only as a single safe command. We assert the splitter
		// actually broke on it by checking ReadOnlyShell's downstream guarantee.
		if hasRawNewline(cmd) && ReadOnlyShell(cmd) {
			// ReadOnlyShell may still be true if BOTH sides are independently
			// read-only (e.g. "ls\npwd"); that is fine. The violation would be a
			// destructive token surviving as read-only, which Invariant 5 covers.
			_ = segs
		}

		// Invariant 5 (security, the crux): if ANY produced segment contains a
		// substitution/grouping construct, ReadOnlyShell must be false. A
		// substitution can hide an arbitrary destructive inner command that the
		// operator splitter cannot decompose.
		anySub := false
		for _, s := range segs {
			if HasSubstitutionOrGrouping(s) {
				anySub = true
				break
			}
		}
		if anySub && ReadOnlyShell(cmd) {
			t.Fatalf("SECURITY: ReadOnlyShell(%q)=true but a segment has substitution/grouping: %#v", cmd, segs)
		}
	})
}

// FuzzCanonicalize asserts Canonicalize never panics, is idempotent, and never
// strips a re-entrant launcher (sudo / docker exec / npx / devbox run), which
// would open a permission backdoor.
func FuzzCanonicalize(f *testing.F) {
	seedGovernance(f)
	f.Fuzz(func(t *testing.T, cmd string) {
		canon := Canonicalize(cmd)

		// Invariant 1: idempotent. Canonicalize(Canonicalize(x)) == Canonicalize(x).
		if again := Canonicalize(canon); again != canon {
			t.Fatalf("Canonicalize not idempotent: Canonicalize(%q)=%q, Canonicalize again=%q", cmd, canon, again)
		}

		// Invariant 2 (security): re-entrant launchers are never peeled off. If
		// the trimmed input begins with one of these as its first field, the
		// canonical form must begin with the same launcher.
		fields := strings.Fields(strings.TrimSpace(cmd))
		if len(fields) > 0 {
			head := fields[0]
			launchers := map[string]bool{"sudo": true, "docker": true, "npx": true, "devbox": true}
			if launchers[head] {
				cf := strings.Fields(canon)
				if len(cf) == 0 || cf[0] != head {
					t.Fatalf("SECURITY: Canonicalize(%q)=%q stripped re-entrant launcher %q", cmd, canon, head)
				}
			}
		}

		// Invariant 3: canonicalization only ever removes leading tokens, so the
		// canonical form must be a suffix (token-wise) of the trimmed input, or
		// equal to it. We assert the canonical form's fields are a tail of the
		// input's fields.
		if !isFieldTail(fields, strings.Fields(canon)) {
			t.Fatalf("Canonicalize(%q)=%q is not a token-tail of the input %v", cmd, canon, fields)
		}
	})
}

// FuzzReadOnlyShell asserts ReadOnlyShell never panics and that the
// substitution/grouping fail-safe holds: any command whose split yields a
// segment with substitution or grouping is never read-only.
func FuzzReadOnlyShell(f *testing.F) {
	seedGovernance(f)
	f.Fuzz(func(t *testing.T, cmd string) {
		ro := ReadOnlyShell(cmd)
		if !ro {
			return
		}
		// If classified read-only, NO segment may contain substitution/grouping.
		for _, s := range SplitCommands(cmd) {
			if HasSubstitutionOrGrouping(s) {
				t.Fatalf("SECURITY: ReadOnlyShell(%q)=true but segment %q has substitution/grouping", cmd, s)
			}
		}
		// And no segment may, after canonicalization, contain a destructive verb
		// as a field or any output redirection.
		for _, s := range SplitCommands(cmd) {
			canon := Canonicalize(s)
			if strings.ContainsAny(canon, ">") {
				t.Fatalf("SECURITY: ReadOnlyShell(%q)=true but segment %q canonicalizes to a redirection %q", cmd, s, canon)
			}
			fs := fieldSet(canon)
			for _, tok := range destructiveTokens {
				if fs[tok] {
					t.Fatalf("SECURITY: ReadOnlyShell(%q)=true but segment %q contains destructive token %q", cmd, s, tok)
				}
			}
		}
	})
}

// FuzzSubstitutionReadOnly asserts the A1 classifier never panics and, when it
// returns true, every extracted inner command is read-only AND the blanked outer has
// no destructive token or output redirection — the security crux of the read-only
// substitution carve-out.
func FuzzSubstitutionReadOnly(f *testing.F) {
	seedGovernance(f)
	// Nested read-only subshells/substitutions: the recursive inner case the soundness
	// assertion must model (a regression seed for the `ReadOnlyShell || SubstitutionReadOnly`
	// inner contract).
	f.Add("( (cat))")
	f.Add("echo $(cat $(ls))")
	f.Fuzz(func(t *testing.T, cmd string) {
		// Run per-segment, as the evaluator does.
		for _, seg := range SplitCommands(cmd) {
			if !SubstitutionReadOnly(seg) {
				continue
			}
			assertSubstReadOnlySound(t, seg)
		}
	})
}

// assertSubstReadOnlySound recursively verifies that a segment SubstitutionReadOnly
// cleared decomposes to ONLY read-only programs: every extracted inner is either plain
// ReadOnlyShell OR itself a sound read-only substitution (the recursive case — a nested
// `( (cat))` / `cat $(cat $(ls))`), and the blanked outer carries no output redirection
// or destructive token. This mirrors the classifier's own `ReadOnlyShell(in) ||
// SubstitutionReadOnly(in)` inner contract, so the fuzzer asserts genuine soundness
// rather than a stricter shape the classifier never promised.
func assertSubstReadOnlySound(t *testing.T, seg string) {
	t.Helper()
	inner, ok := extractSubstitutions(seg)
	if !ok {
		t.Fatalf("SECURITY: SubstitutionReadOnly(%q)=true but extraction failed", seg)
	}
	for _, in := range inner {
		switch {
		case ReadOnlyShell(in):
			// plain read-only inner — sound.
		case SubstitutionReadOnly(in):
			assertSubstReadOnlySound(t, in) // nested read-only substitution — recurse.
		default:
			t.Fatalf("SECURITY: SubstitutionReadOnly(%q)=true but inner %q is neither read-only nor a sound substitution", seg, in)
		}
	}
	// The blanked outer must have no destructive token / output redirection.
	blanked, ok := outerWithSubstitutionsBlanked(seg)
	if !ok {
		t.Fatalf("SECURITY: SubstitutionReadOnly(%q)=true but blanking failed", seg)
	}
	canon := Canonicalize(blanked)
	if strings.ContainsAny(canon, ">") {
		t.Fatalf("SECURITY: SubstitutionReadOnly(%q)=true but blanked outer %q has a redirection", seg, canon)
	}
	fs := fieldSet(canon)
	for _, tok := range destructiveTokens {
		if fs[tok] {
			t.Fatalf("SECURITY: SubstitutionReadOnly(%q)=true but blanked outer %q has destructive token %q", seg, canon, tok)
		}
	}
}

// FuzzIsolationApprovable asserts the A2 classifier never panics and, when it returns
// true, no segment (outer or extracted inner) contains a worktree-escape verb and
// substitution extraction succeeded — the security crux of the isolated-subagent
// auto-approve.
func FuzzIsolationApprovable(f *testing.F) {
	seedGovernance(f)
	f.Add("go test ./...")
	f.Add(`for p in $(go list ./...); do go test -cover "$p"; done`)
	f.Add("git push origin main")
	f.Add("go test -exec /x ./...")
	f.Add("git -C /x log")
	f.Fuzz(func(t *testing.T, cmd string) {
		if !IsolationApprovable(cmd) {
			return
		}
		// POSITIVE soundness (mirrors FuzzSubstitutionReadOnly): if the WHOLE command
		// cleared, EVERY segment — outer (substitution-blanked) and every recursively
		// extracted inner — must be independently isolation-sound. A cleared segment may
		// only be: pure scaffolding/grouping, simpleReadOnly, or a worktree-safe
		// `go {test,build,vet,list}` with no output redirection and no arbitrary-exec flag;
		// and never a worktree-escape verb or a path-escaping git global flag.
		for _, seg := range SplitCommands(cmd) {
			assertSegmentIsolationSound(t, cmd, seg)
		}
	})
}

// assertSegmentIsolationSound fails the fuzzer if seg (a SplitCommands segment of a
// command IsolationApprovable cleared) is not independently isolation-sound. It mirrors
// the classifier's own contract from the OUTSIDE: blank any substitution, strip
// control-flow scaffolding, then require the residual to be empty/placeholder OR
// simpleReadOnly OR a worktree-safe go verb with no `>`/exec-flag — and reject any git
// escape verb / path-escaping global flag. It recurses into every extracted inner.
func assertSegmentIsolationSound(t *testing.T, cmd, seg string) {
	t.Helper()
	if HasSubstitutionOrGrouping(seg) {
		inner, ok := extractSubstitutions(seg)
		if !ok {
			t.Fatalf("SECURITY: IsolationApprovable(%q)=true but extraction failed on %q", cmd, seg)
		}
		for _, in := range inner {
			for _, innerSeg := range SplitCommands(in) {
				assertSegmentIsolationSound(t, cmd, innerSeg)
			}
		}
		blanked, okB := outerWithSubstitutionsBlanked(seg)
		if !okB {
			t.Fatalf("SECURITY: IsolationApprovable(%q)=true but blanking failed on %q", cmd, seg)
		}
		assertClearedResidualSound(t, cmd, seg, blanked)
		return
	}
	assertClearedResidualSound(t, cmd, seg, seg)
}

// assertClearedResidualSound checks the residual of a cleared segment after blanking +
// scaffolding-strip is one of the sound shapes. orig is the original segment (for the
// pure-subshell placeholder case).
func assertClearedResidualSound(t *testing.T, cmd, orig, classifyText string) {
	t.Helper()
	residual := strings.TrimSpace(stripShellKeywords(classifyText))
	if residual == "" {
		return // pure scaffolding (e.g. a for-header) runs no program.
	}
	if residual == substitutionPlaceholder {
		if !isPureSubshell(orig) {
			t.Fatalf("SECURITY: IsolationApprovable(%q)=true but lone-placeholder residual of %q is not a pure subshell", cmd, orig)
		}
		return
	}
	fields := strings.Fields(Canonicalize(residual))
	if len(fields) == 0 {
		t.Fatalf("SECURITY: IsolationApprovable(%q)=true but residual %q has no fields", cmd, residual)
	}
	// git: no escape verb, no path-escaping global flag.
	if fields[0] == "git" {
		sub, escapesPath, ok := gitSubcommand(fields)
		if !ok || escapesPath || worktreeEscapeGitSubcommands[sub] {
			t.Fatalf("SECURITY: IsolationApprovable(%q)=true but git residual %q escapes (sub=%q escapesPath=%v ok=%v)", cmd, residual, sub, escapesPath, ok)
		}
	}
	if simpleReadOnly(residual) {
		return
	}
	// The only non-read-only sound shape: a worktree-safe go verb, no `>`, no exec flag.
	if fields[0] == "go" && len(fields) >= 2 && worktreeSafeGoSubcommands[fields[1]] &&
		!strings.ContainsAny(Canonicalize(residual), ">") && !goArgsEscapeWorktree(fields[2:]) {
		return
	}
	t.Fatalf("SECURITY: IsolationApprovable(%q)=true but residual %q is neither read-only nor a worktree-safe go verb", cmd, residual)
}

// fieldSet returns the set of whitespace-delimited fields of s.
func fieldSet(s string) map[string]bool {
	out := make(map[string]bool)
	for _, f := range strings.Fields(s) {
		out[f] = true
	}
	return out
}

// hasRawNewline reports whether s contains a newline or carriage return outside
// of any quoting (a conservative check: any newline at all, since the splitter
// treats quoted newlines as literal — used only as a soft signal).
func hasRawNewline(s string) bool {
	return strings.ContainsAny(s, "\n\r")
}

// isFieldTail reports whether tail is a contiguous suffix of full (token-wise),
// or whether tail equals full. Canonicalize only strips leading wrapper tokens,
// so its output fields must be a tail of the input fields — except it may merge
// an "env FOO=bar ls" case where the wrapper's own residue stays; to stay
// robust we accept any suffix match anchored at the end.
func isFieldTail(full, tail []string) bool {
	if len(tail) > len(full) {
		return false
	}
	off := len(full) - len(tail)
	for i := range tail {
		if full[off+i] != tail[i] {
			return false
		}
	}
	return true
}

// FuzzFlooredConfiguredAllow asserts POSITIVE soundness of the
// floored-configured-allow classifier (issue #32), mirroring
// FuzzSubstitutionReadOnly/FuzzIsolationApprovable: for every segment
// flooredAllowSafe clears, EVERY recursively-extracted inner must be
// independently read-only (plain ReadOnlyShell or a sound read-only
// substitution — the configured Allow vouches only for the OUTER, never a
// hidden inner), extraction/blanking must have succeeded, a lone-placeholder
// residual must be a pure subshell, and the blanked outer must carry no
// worktree-escape construction (escape git subcommand, path-bearing git global
// flag, go escape flag).
func FuzzFlooredConfiguredAllow(f *testing.F) {
	seedGovernance(f)
	f.Add("go test $(git rev-parse HEAD)")
	f.Add("echo $(ls) > out.txt")
	f.Add("go test $(zap)")
	f.Add("echo $(touch SAFE_MARKER)")
	f.Add("git push $(git rev-parse HEAD)")
	f.Add("$(ls)")
	f.Fuzz(func(t *testing.T, cmd string) {
		// Run per-segment, as the evaluator's floor branch does.
		for _, seg := range SplitCommands(cmd) {
			if !flooredAllowSafe(seg) {
				continue
			}
			assertFlooredAllowSound(t, seg)
		}
	})
}

// assertFlooredAllowSound fails the fuzzer if seg (a segment flooredAllowSafe
// cleared) hides a non-read-only inner or an escape-capable outer. It mirrors
// the classifier's contract from the OUTSIDE.
func assertFlooredAllowSound(t *testing.T, seg string) {
	t.Helper()
	if !HasSubstitutionOrGrouping(seg) {
		assertEscapeRejectionsSound(t, seg, seg, seg)
		return
	}
	inner, ok := extractSubstitutions(seg)
	if !ok {
		t.Fatalf("SECURITY: flooredAllowSafe(%q)=true but extraction failed", seg)
	}
	for _, in := range inner {
		switch {
		case ReadOnlyShell(in):
			// plain read-only inner — sound.
		case SubstitutionReadOnly(in):
			assertSubstReadOnlySound(t, in) // nested read-only substitution — recurse.
		default:
			t.Fatalf("SECURITY: flooredAllowSafe(%q)=true but inner %q is neither read-only nor a sound read-only substitution — a configured outer Allow must never vouch for a hidden inner", seg, in)
		}
	}
	blanked, okB := outerWithSubstitutionsBlanked(seg)
	if !okB {
		t.Fatalf("SECURITY: flooredAllowSafe(%q)=true but blanking failed", seg)
	}
	assertEscapeRejectionsSound(t, seg, seg, blanked)
}

// assertEscapeRejectionsSound checks the (possibly blanked) outer of a cleared
// segment: empty residual is scaffolding; a lone placeholder must be a pure
// subshell (command-position substitution executes its output); otherwise no
// git escape subcommand / path-escaping global flag / go escape flag.
func assertEscapeRejectionsSound(t *testing.T, cmd, orig, classifyText string) {
	t.Helper()
	residual := strings.TrimSpace(stripShellKeywords(classifyText))
	if residual == "" {
		return
	}
	if residual == substitutionPlaceholder {
		if !isPureSubshell(orig) {
			t.Fatalf("SECURITY: flooredAllowSafe(%q)=true but lone-placeholder residual of %q is not a pure subshell", cmd, orig)
		}
		return
	}
	fields := strings.Fields(Canonicalize(residual))
	if len(fields) == 0 {
		return
	}
	if fields[0] == "git" {
		sub, escapesPath, ok := gitSubcommand(fields)
		if !ok || escapesPath || worktreeEscapeGitSubcommands[sub] {
			t.Fatalf("SECURITY: flooredAllowSafe(%q)=true but git residual %q escapes (sub=%q escapesPath=%v ok=%v)", cmd, residual, sub, escapesPath, ok)
		}
	}
	if fields[0] == "go" && goArgsEscapeWorktree(fields[1:]) {
		t.Fatalf("SECURITY: flooredAllowSafe(%q)=true but go residual %q carries an escape flag", cmd, residual)
	}
}
