package governance

import "strings"

// SplitCommands splits a compound Shell command line into its individual simple
// commands, breaking on the shell operators "&&", "||", ";", "|", a single
// unquoted "&" (background), and newlines, while respecting single and double
// quotes (operators inside quotes are literal).
//
// This is the compound-command awareness the permission layer relies on: a line
// such as `git status && rm -rf /` must be evaluated as TWO commands, so that a
// deny on `rm` blocks the whole compound even though `git status` would be
// allowed. The returned slice contains the trimmed segments with empty segments
// dropped; an empty or whitespace-only input yields nil.
//
// Newlines and a single "&" are separators too: `ls\nrm -rf build` and
// `ls & rm -rf build` smuggle a second command past a gate that only knows the
// classic operators. Command/process substitution and subshell grouping
// (`$(...)`, backticks, `<(...)`, and `(`/`{` grouping) are NOT decomposed here;
// callers must treat any segment for which HasSubstitutionOrGrouping reports
// true as fail-safe (not read-only; escalate to at least Ask), since the inner
// command cannot be soundly extracted without a full shell parser.
func SplitCommands(cmd string) []string {
	var (
		out      []string
		buf      strings.Builder
		inSingle bool
		inDouble bool
	)
	runes := []rune(cmd)
	flush := func() {
		seg := strings.TrimSpace(buf.String())
		if seg != "" {
			out = append(out, seg)
		}
		buf.Reset()
	}
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
			buf.WriteRune(c)
			continue
		case inDouble:
			if c == '"' {
				inDouble = false
			}
			buf.WriteRune(c)
			continue
		case c == '\'':
			inSingle = true
			buf.WriteRune(c)
			continue
		case c == '"':
			inDouble = true
			buf.WriteRune(c)
			continue
		}

		// Unquoted: look for the two-rune operators "&&" and "||" first, then
		// the single-rune separators ";", "|", "&" and newlines. A lone "&" is a
		// background separator and must split too, so a denied inner command
		// cannot ride on `ls & rm -rf build`.
		if (c == '&' || c == '|') && i+1 < len(runes) && runes[i+1] == c {
			flush()
			i++ // consume the second operator rune
			continue
		}
		if c == ';' || c == '|' || c == '&' || c == '\n' || c == '\r' {
			flush()
			continue
		}
		buf.WriteRune(c)
	}
	flush()
	return out
}

// HasSubstitutionOrGrouping reports whether a Shell segment contains command or
// process substitution, or subshell/group-command grouping, in unquoted text:
// `$(...)`, a backtick, `<(...)`/`>(...)`, an opening "(" or "{" used as
// grouping. These constructs can hide an arbitrary inner command that the
// operator-based splitter does not decompose; the permission layer treats any
// such segment as fail-safe (not read-only, and escalated to at least Ask) so a
// denied/destructive inner command cannot be silently matched as one literal
// allowed token.
//
// The scan is quote-aware: a "(" inside single or double quotes is literal data
// and does not count. A "$" immediately before "(" or "{" (command substitution
// or "${...}" expansion) is flagged conservatively.
func HasSubstitutionOrGrouping(seg string) bool {
	runes := []rune(seg)
	var inSingle, inDouble bool
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case inDouble:
			if c == '"' {
				inDouble = false
				continue
			}
			// Command substitution and backticks stay active inside double quotes.
			if hasSubstitutionTrigger(c, next) {
				return true
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case hasSubstitutionTrigger(c, next) || c == '(' || c == '{':
			// Unquoted "(" / "{" is subshell or group-command grouping. "{" is
			// only a group command as a standalone token, but treating any
			// unquoted occurrence as grouping is the conservative, fail-safe
			// choice.
			return true
		}
	}
	return false
}

// hasSubstitutionTrigger reports whether c (with following rune next) opens a
// command or process substitution: a backtick, "$(", "${", "<(" or ">(".
func hasSubstitutionTrigger(c, next rune) bool {
	switch c {
	case '`':
		return true
	case '$':
		return next == '(' || next == '{'
	case '<', '>':
		return next == '('
	}
	return false
}

// substitutionPlaceholder is the inert token outerWithSubstitutionsBlanked
// substitutes for each extracted substitution/grouping span. It is a bare path-shaped
// argument (no leading verb, no redirection char, no shell operator), so the blanked
// outer command reads to simpleReadOnly exactly as if the substitution had produced a
// single literal filename — never a verb, never a write indicator. It deliberately
// contains no characters HasSubstitutionOrGrouping would re-flag.
const substitutionPlaceholder = "MECATL_SUBST"

// extractSubstitutions returns the inner command text of EVERY command/process
// substitution or subshell/group-command grouping in a single Shell segment, scanned
// quote-aware and RECURSIVELY (a nested `$( … $(…) … )` contributes both the inner
// and the outer inner-text). It is the read-only-aware companion to
// HasSubstitutionOrGrouping: where that only reports presence, this extracts the
// inner programs so SubstitutionReadOnly / IsolationApprovable can classify them.
//
// ok=false (fail-safe, empty inner) on ANY ambiguity: an unbalanced opener, an
// unterminated backtick, a "${…}" parameter expansion (NOT a command — but it can
// embed `${x:-$(cmd)}`, which we cannot soundly decompose), or input the scanner
// cannot resolve. A caller MUST treat ok=false as "not classifiable" and fail safe
// (escalate to Ask / refuse auto-approval). A segment with NO substitution at all
// returns (nil, true): there is nothing to extract and nothing ambiguous.
//
// The recognised openers mirror HasSubstitutionOrGrouping: "$(" and backticks
// (command substitution), "<(" / ">(" (process substitution), and a bare unquoted
// "(" (subshell) — each closed by its matching ")" (or the matching backtick). A
// bare unquoted "{" (group command) makes extraction ambiguous (its body is the rest
// of the segment up to a "}" that may not exist and is whitespace-significant), so it
// returns ok=false — the conservative choice, exactly as HasSubstitutionOrGrouping
// treats "{" as fail-safe grouping.
func extractSubstitutions(seg string) (inner []string, ok bool) {
	runes := []rune(seg)
	var out []string
	var ok2 bool
	out, ok2 = extractFrom(runes, 0, len(runes), 0)
	return out, ok2
}

// maxSubstitutionDepth bounds the recursion in extractFrom so a pathological deeply
// nested input cannot exhaust the stack. Beyond it, extraction fails safe (ok=false).
const maxSubstitutionDepth = 32

// span describes one substitution/grouping opener found by walkSubstitutions: the inner
// body bounds [bodyStart, bodyEnd) and the index of the matching close rune.
type span struct {
	bodyStart, bodyEnd int
	closeIdx           int
}

// nextSpanOpener reports whether runes[i] (with following rune next) opens a
// substitution/grouping span OUTSIDE quotes, given the current double-quote state, and
// returns the index just past the opener (the body start). It mirrors
// hasSubstitutionTrigger plus the bare "(" subshell. A bare "{" is signalled via
// isBrace so the caller fails safe. The "${" form is signalled via isParamExpansion.
func nextSpanOpener(c, next rune, inDouble bool) (bodyStart int, kind byte, ok bool) {
	switch {
	case c == '$' && next == '{':
		return 0, 'P', true // ${…} parameter expansion: ambiguous, fail safe.
	case c == '$' && next == '(':
		return 2, '(', true
	case (c == '<' || c == '>') && next == '(' && !inDouble:
		return 2, '(', true
	case c == '`':
		return 1, '`', true
	case c == '(' && !inDouble:
		return 1, '(', true
	case c == '{' && !inDouble:
		return 0, 'B', true // bare group command: ambiguous, fail safe.
	}
	return 0, 0, false
}

// walkSubstitutions scans runes[start:end] quote-aware, invoking onSpan for each
// top-level substitution/grouping span (with the index just past it so the caller can
// advance) and onRune for each literal rune outside a span. It returns ok=false on any
// ambiguity (unterminated quote, "${" expansion, bare "{" group, or an unclosed
// opener). It is the single quote/opener scanner shared by extractFrom and blankFrom, so
// their classification of what is a span cannot drift. The callbacks may return false to
// abort (propagated as ok=false).
func walkSubstitutions(runes []rune, start, end int, onSpan func(s span) bool, onRune func(c rune)) bool {
	var inSingle, inDouble bool
	for i := start; i < end; i++ {
		c := runes[i]
		next := rune(0)
		if i+1 < end {
			next = runes[i+1]
		}
		switch {
		case inSingle:
			onRune(c)
			if c == '\'' {
				inSingle = false
			}
			continue
		case c == '\'' && !inDouble:
			inSingle = true
			onRune(c)
			continue
		case c == '"':
			inDouble = !inDouble
			onRune(c)
			continue
		}
		bodyOff, kind, isOpener := nextSpanOpener(c, next, inDouble)
		if !isOpener {
			onRune(c)
			continue
		}
		if kind == 'P' || kind == 'B' {
			return false // ${…} expansion or bare "{" group: cannot soundly decompose.
		}
		var s span
		var okSpan bool
		if kind == '`' {
			var b [2]int
			b, s.closeIdx, okSpan = spanBacktick(runes, i+bodyOff, end)
			s.bodyStart, s.bodyEnd = b[0], b[1]
		} else {
			var b [2]int
			b, s.closeIdx, okSpan = spanParen(runes, i+bodyOff, end)
			s.bodyStart, s.bodyEnd = b[0], b[1]
		}
		if !okSpan || !onSpan(s) {
			return false
		}
		i = s.closeIdx
	}
	return !inSingle && !inDouble
}

// extractFrom scans runes[start:end] for substitution/grouping spans, appending each
// span's inner text and recursing into it. It returns the accumulated inner commands
// and ok=false on any ambiguity (unbalanced opener/backtick, a "{" group, "${"
// parameter expansion, or excessive depth).
func extractFrom(runes []rune, start, end, depth int) ([]string, bool) {
	if depth > maxSubstitutionDepth {
		return nil, false
	}
	var out []string
	ok := walkSubstitutions(runes, start, end,
		func(s span) bool {
			rec, okRec := extractFrom(runes, s.bodyStart, s.bodyEnd, depth+1)
			if !okRec {
				return false
			}
			out = append(out, string(runes[s.bodyStart:s.bodyEnd]))
			out = append(out, rec...)
			return true
		},
		func(rune) {}, // literal runes do not contribute inner commands.
	)
	if !ok {
		return nil, false
	}
	return out, true
}

// spanParen finds the matching ")" for an opener whose body starts at index `from`,
// honouring nested parens and quotes. It returns the inner [start,end) bounds (the
// text between the opener and its matching close), the index of the close paren, and
// ok=false if the paren is never closed. Nested "(" inside increases the depth so a
// `$( ( ) )` closes correctly.
func spanParen(runes []rune, from, end int) (bounds [2]int, closeIdx int, ok bool) {
	depth := 1
	var inSingle, inDouble bool
	for i := from; i < end; i++ {
		c := runes[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case inDouble:
			if c == '"' {
				inDouble = false
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return [2]int{from, i}, i, true
			}
		}
	}
	return [2]int{}, 0, false
}

// spanBacktick finds the matching closing backtick for an opener whose body starts at
// index `from`. Backticks do not nest, so the first unescaped backtick closes it. It
// returns the inner bounds, the index of the close backtick, and ok=false if it is
// never closed.
func spanBacktick(runes []rune, from, end int) (bounds [2]int, closeIdx int, ok bool) {
	for i := from; i < end; i++ {
		if runes[i] == '\\' { // skip an escaped char inside the backtick body
			i++
			continue
		}
		if runes[i] == '`' {
			return [2]int{from, i}, i, true
		}
	}
	return [2]int{}, 0, false
}

// outerWithSubstitutionsBlanked replaces every substitution/grouping span in seg with
// the inert substitutionPlaceholder token, so simpleReadOnly can classify the OUTER
// command's verb/redirection without HasSubstitutionOrGrouping short-circuiting it to
// false. It returns ok=false on the same ambiguity extractSubstitutions rejects (so a
// caller never classifies an outer it could not soundly blank). The placeholder is a
// path-shaped argument, so e.g. `cat $(ls)` blanks to `cat MECATL_SUBST` (read-only
// verb + one arg) while `$(rm x) foo` blanks to `MECATL_SUBST foo` (placeholder as the
// verb → unknown → not read-only, the fail-safe outcome for a substitution-as-verb).
func outerWithSubstitutionsBlanked(seg string) (string, bool) {
	runes := []rune(seg)
	var b strings.Builder
	ok := walkSubstitutions(runes, 0, len(runes),
		func(span) bool {
			// Replace the whole span (NOT recursing into its body) with the inert
			// placeholder, so simpleReadOnly classifies the outer command.
			b.WriteString(substitutionPlaceholder)
			return true
		},
		func(c rune) { b.WriteRune(c) },
	)
	if !ok {
		return "", false
	}
	return b.String(), true
}

// shellControlKeywords are the Shell compound-command / control-flow keywords that
// PREFIX a real command inside a loop/conditional segment (after SplitCommands breaks
// on `;`/newlines). They are not programs; stripping them exposes the actual command
// to classify. `in` and the loop variable in `for VAR in LIST` are handled by
// stripShellKeywords specially (the LIST is the substitution we already classified).
var shellControlKeywords = map[string]bool{
	"do": true, "then": true, "else": true, "elif": true,
	"while": true, "until": true, "if": true,
}

// stripShellKeywords removes leading shell control-flow scaffolding from a blanked
// segment so the residual real command (if any) can be classified. It handles:
//   - a leading `do`/`then`/`else`/`elif`/`while`/`until`/`if` keyword (and repeats);
//   - a `for VAR in LIST` / `select VAR in LIST` header, whose LIST is the iteration
//     source (already validated as a read-only inner when it was a substitution) — the
//     header itself runs no command, so it strips to empty;
//   - trailing structural tokens `done`/`fi`/`;;`.
//
// It returns the residual command string ("" when the segment is pure scaffolding,
// which the caller treats as read-only — it runs no program). It is purely
// lexical/conservative: an unrecognised head is returned unchanged.
func stripShellKeywords(seg string) string {
	fields := strings.Fields(seg)
	for len(fields) > 0 {
		head := fields[0]
		switch {
		case head == "done" || head == "fi" || head == "esac" || head == ";;":
			fields = fields[1:]
		case head == "for" || head == "select":
			// `for VAR in LIST` (LIST already validated). Everything up to and including
			// the closing structural token is scaffolding; the body is a SEPARATE segment
			// (SplitCommands broke on the `;` before `do`). So the whole `for … in …`
			// header runs no command → strip to empty.
			return ""
		case shellControlKeywords[head]:
			fields = fields[1:]
		default:
			return strings.Join(fields, " ")
		}
	}
	return ""
}

// isPureSubshell reports whether a segment is exactly a bare `(...)` subshell wrapping
// a body (optionally with surrounding whitespace), e.g. `(git status)`. A bare subshell
// just GROUPS and runs its body directly, so its blanked-to-a-lone-placeholder outer is
// safe (the body was validated as the inner). This is DISTINCT from command
// substitution `$(...)`/backticks in command position (e.g. `$(echo ls)`), which
// executes the substitution's OUTPUT as a command — a code-execution vector that must
// NEVER be treated as pure grouping.
func isPureSubshell(seg string) bool {
	t := strings.TrimSpace(seg)
	runes := []rune(t)
	if len(runes) < 2 || runes[0] != '(' {
		return false
	}
	_, closeIdx, ok := spanParen(runes, 1, len(runes))
	if !ok {
		return false
	}
	// The matching ")" must be the LAST non-space rune (nothing trails the subshell).
	return closeIdx == len(runes)-1
}

// outerReadOnlyAfterBlanking reports whether the blanked OUTER of a substitution
// segment is read-only. The seg argument is the ORIGINAL (un-blanked) segment, needed
// to tell a safe bare subshell from an unsafe command-substitution-as-verb. A blanked
// outer that is exactly the placeholder is pure grouping ONLY when the original was a
// bare `(...)` subshell — the inner was already validated, so the outer is vacuously
// read-only. A lone-placeholder outer from `$(...)`/backticks (command substitution in
// command position) is the fail-safe case (false): its OUTPUT would be executed.
// Otherwise control-flow scaffolding is stripped and the residual (if any) is classified
// by simpleReadOnly.
func outerReadOnlyAfterBlanking(seg, blanked string) bool {
	trimmed := strings.TrimSpace(blanked)
	if trimmed == substitutionPlaceholder {
		return isPureSubshell(seg) // safe only for a bare subshell, not command substitution.
	}
	residual := stripShellKeywords(blanked)
	if strings.TrimSpace(residual) == "" {
		return true // pure scaffolding (e.g. a `for … in …` header) runs no command.
	}
	if strings.TrimSpace(residual) == substitutionPlaceholder {
		// A residual that is a lone placeholder after stripping scaffolding (e.g. a
		// `for x in MECATL_SUBST` list source) is the iteration source, not a command in
		// command position — read-only.
		return true
	}
	return simpleReadOnly(residual)
}

// SubstitutionReadOnly reports whether a single Shell SEGMENT that contains command/
// process substitution or subshell grouping is nonetheless safe to treat as read-only:
// every extracted inner command is ReadOnlyShell-true AND the outer command (with each
// substitution blanked to an inert placeholder) is simpleReadOnly-true. It is the
// SEPARATE read-only-aware classifier (A1) the evaluator consults to AVOID flooring a
// fully-read-only substitution (e.g. `cat $(ls)`, `echo $(git rev-parse HEAD)`) at Ask.
//
// It fails safe (false) on: a segment with NO substitution (use simpleReadOnly
// directly — there is nothing for this classifier to do), any extraction ambiguity
// (extractSubstitutions ok=false / the blank ok=false), any inner command that is not
// read-only, or an outer that is not read-only once blanked. It NEVER widens
// ReadOnlyShell/simpleReadOnly/plan-mode — those stay byte-for-byte unchanged; this is
// an ADDITIONAL allow path, consulted only where the substitution floor would otherwise
// apply.
func SubstitutionReadOnly(seg string) bool {
	if !HasSubstitutionOrGrouping(seg) {
		// No substitution: this classifier does not apply. The caller's ordinary
		// simpleReadOnly path already handles a plain segment; returning false here keeps
		// the responsibilities crisp (and a non-substituted segment is never floored).
		return false
	}
	inner, ok := extractSubstitutions(seg)
	if !ok {
		return false
	}
	for _, in := range inner {
		// An inner may itself be a substitution-bearing read-only command (a nested
		// `cat $(ls)`), so accept either plain read-only OR a read-only substitution.
		if !ReadOnlyShell(in) && !SubstitutionReadOnly(in) {
			return false
		}
	}
	blanked, ok := outerWithSubstitutionsBlanked(seg)
	if !ok {
		return false
	}
	return outerReadOnlyAfterBlanking(seg, blanked)
}

// worktreeEscapeVerbs are git invocations a SANDBOXED subagent must NEVER auto-run even
// in an isolated worktree, because they reach OUTSIDE the throwaway checkout: `git push`
// publishes to a remote, `git config` writes shared `.git/config` (a code-execution
// vector — alias/pager/sshCommand), `git remote` rewrites remote config, `git worktree`/
// `git submodule` add or rewrite checkouts/submodules outside the throwaway tree. They
// are hard-rejected by IsolationApprovable regardless of read-only-ness. The keys are the
// resolved git SUBCOMMAND (after gitSubcommand skips any leading global flags).
var worktreeEscapeGitSubcommands = map[string]bool{
	"push":      true,
	"config":    true,
	"remote":    true,
	"fetch":     true,
	"pull":      true,
	"clone":     true,
	"worktree":  true,
	"submodule": true,
}

// gitGlobalValueFlags are git GLOBAL flags (before the subcommand) that consume a
// SEPARATE following value (the space form, e.g. `git -C <path>` / `git -c <kv>`). The
// `=` form (`--git-dir=<path>`) carries its own value in the same token. gitSubcommand
// skips these to find the real subcommand, so `git -C /x push` does not slip past the
// escape-verb check by shifting `push` out of fields[1].
var gitGlobalValueFlags = map[string]bool{
	"-C": true, "-c": true,
	"--git-dir": true, "--work-tree": true, "--namespace": true,
	"--exec-path": true, "--config-env": true,
}

// gitPathBearingGlobalFlags are git GLOBAL flags that POINT git at a different repo,
// working tree, or directory — i.e. OUTSIDE the throwaway worktree. Their presence makes
// a command NOT isolation-approvable regardless of the subcommand: `git -C /outside log`,
// `git --git-dir=/x status` and `git --work-tree=/y status` all operate outside the
// sandbox. Matched on the bare flag name (the `=` form is split off before lookup).
var gitPathBearingGlobalFlags = map[string]bool{
	"-C": true, "--git-dir": true, "--work-tree": true,
}

// gitSubcommand resolves the real git SUBCOMMAND from a `git …` field slice (fields[0]
// == "git"), skipping any leading GLOBAL flags. It returns the subcommand, whether a
// PATH-bearing global flag (`-C`/`--git-dir`/`--work-tree`) was seen (which escapes the
// worktree → caller must reject), and ok=false when no subcommand could be resolved
// (a bare `git` or a dangling value flag — fail safe). Both the `--flag=value` and the
// `--flag value` forms are handled.
func gitSubcommand(fields []string) (sub string, escapesPath bool, ok bool) {
	i := 1 // fields[0] == "git"
	for i < len(fields) {
		f := fields[i]
		if !strings.HasPrefix(f, "-") {
			return f, escapesPath, true // first non-flag token is the subcommand
		}
		name := f
		if eq := strings.IndexByte(f, '='); eq >= 0 {
			name = f[:eq] // `--git-dir=/x` → `--git-dir`
		}
		if gitPathBearingGlobalFlags[name] {
			escapesPath = true
		}
		// A space-form value flag (`-C <path>`, `-c <kv>`) consumes the next token too,
		// UNLESS it already carried an `=` value in this token.
		i++
		if gitGlobalValueFlags[name] && !strings.Contains(f, "=") {
			i++
		}
	}
	return "", escapesPath, false // bare `git` or dangling value flag: fail safe.
}

// goWorktreeEscapeFlags are `go` build/test flags that escape the worktree sandbox —
// either by running an ARBITRARY EXTERNAL program rather than the repo's own (trusted)
// code, or by WRITING to an arbitrary (possibly absolute / `../`-escaping) path:
//   - `-exec` (run the test/built binary via a named program),
//   - `-toolexec` (run a program for every tool invocation),
//   - `-overlay` (substitute arbitrary files into the build via a JSON map),
//   - `-o` (write the compiled binary to a named path — e.g. `go build -o /etc/cron.d/x`,
//     which drops an executable OUTSIDE the throwaway worktree → persistence/code-exec).
//
// The worktree isolates the FILESYSTEM checkout, NOT the process and NOT an `-o`
// destination — so these escape isolation and must NOT auto-approve (they SURFACE to the
// human instead). Matched on the bare flag name (the `=` form is split off before lookup),
// so both `-exec /x`, `-exec=/x`, `--exec /x`, `--exec=/x`, `-o /x` and `-o=/x` are caught.
var goWorktreeEscapeFlags = map[string]bool{
	"-exec": true, "--exec": true,
	"-toolexec": true, "--toolexec": true,
	"-overlay": true, "--overlay": true,
	"-o": true, "--o": true,
}

// goArgsEscapeWorktree reports whether any arg in a `go {test,build,…}` invocation is one
// of goWorktreeEscapeFlags — meaning the command would run an arbitrary external program
// or write to an arbitrary path, neither of which the worktree's filesystem isolation
// contains. It normalises a flag's `=value` form to the bare flag name before lookup.
func goArgsEscapeWorktree(args []string) bool {
	for _, a := range args {
		name := a
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			name = a[:eq]
		}
		if goWorktreeEscapeFlags[name] {
			return true
		}
	}
	return false
}

// worktreeSafeGoSubcommands is the MINIMAL audited set of `go` subcommands an isolated
// subagent may auto-run beyond the read-only set: build/test/vet/list inspect or
// compile within the worktree and do not escape it. `go run` (executes arbitrary code),
// `go get`/`go mod`/`go install` (mutate modules / the module cache / GOBIN) are
// deliberately EXCLUDED — anything not here SURFACES to the human rather than
// auto-approving, so the set stays conservative.
var worktreeSafeGoSubcommands = map[string]bool{
	"test":  true,
	"build": true,
	"vet":   true,
	"list":  true,
}

// segmentIsolationApprovable classifies a SINGLE simple command (no shell operators,
// no substitution — the caller decomposes those) for isolated-subagent auto-approval:
// it is read-only (simpleReadOnly) OR a worktree-safe `go {test,build,vet,list}`, AND
// it is NOT a worktree-escape verb. It mirrors simpleReadOnly's wrapper/redirection
// discipline for the go-verb case so `go test > out` is rejected (the redirection
// makes it a write).
// segmentIsolationApprovable classifies a single command for isolated-subagent
// auto-approval. classifyText is the text to classify (the segment itself, or its
// substitution-blanked outer); orig is the ORIGINAL segment, used only to tell a safe
// bare `(...)` subshell from an unsafe command-substitution-as-verb when classifyText
// blanks to a lone placeholder.
func segmentIsolationApprovable(orig, classifyText string) bool {
	// Strip leading control-flow scaffolding (`do`, `for … in …`, etc.) so the real
	// command is classified. Pure scaffolding (empty residual) runs no program and is
	// trivially approvable.
	residual := stripShellKeywords(classifyText)
	if strings.TrimSpace(residual) == "" {
		return true
	}
	if strings.TrimSpace(residual) == substitutionPlaceholder {
		// A lone placeholder is pure grouping ONLY for a bare subshell; a command
		// substitution in command position executes its output → fail safe.
		return isPureSubshell(orig)
	}
	canon := Canonicalize(residual)
	fields := strings.Fields(canon)
	if len(fields) == 0 {
		return false
	}
	// The SHARED worktree-escape rejection path (escapeRejectionsFree, also behind
	// flooredAllowSafe): git's REAL subcommand is resolved past any leading global
	// flags (`git -C <path>`, `git --git-dir=<x>`, `git -c <kv>` shift the
	// subcommand right), then a worktree-escape subcommand OR any path-bearing
	// global flag that points git OUTSIDE the throwaway worktree rejects; and a
	// `go` invocation carrying an escape flag (-exec/-toolexec/-overlay run an
	// external program; -o writes the binary to an arbitrary path — the worktree
	// isolates the FILESYSTEM checkout, not the PROCESS and not an -o destination)
	// rejects. This runs BEFORE the simpleReadOnly shortcut below, because
	// simpleReadOnly reads fields[1] as the subcommand and would otherwise
	// green-light `git -C /outside log` as a read-only `git -C`. (The go-flag
	// rejection firing before the allowlist branch is outcome-identical: no `go`
	// command is ever simpleReadOnly, so the only acceptance path for go is the
	// allowlist branch below, which required the same flag check.)
	if !escapeRejectionsFree(residual) {
		return false
	}
	// A read-only residual is always isolation-approvable (read-only git subcommands,
	// ls/cat/grep/…). git with global flags has already passed the escape check above.
	if simpleReadOnly(residual) {
		return true
	}
	// The MINIMAL worktree-safe extension: `go {test,build,vet,list}` with no output
	// redirection (a redirection would write outside the verb's normal scope). The
	// escape flags were already rejected by the shared path above.
	if strings.ContainsAny(canon, ">") {
		return false
	}
	if fields[0] == "go" && len(fields) >= 2 && worktreeSafeGoSubcommands[fields[1]] {
		return true
	}
	return false
}

// flooredAllowSafe reports whether a substitution-floored Shell SEGMENT that a
// CONFIGURED Allow covers may resolve WITHOUT surfacing — the bound behind
// PermissionDecision.FlooredConfiguredAllow (issue #32). The configured Allow
// vouches ONLY for the OUTER command (the literal the operator wrote a rule
// for); it can never vouch for what a substitution HIDES. So the bound is:
//
//   - every recursively-extracted INNER must independently classify POSITIVELY
//     read-only — the SAME inner contract SubstitutionReadOnly (A1) applies:
//     plain ReadOnlyShell, or itself a read-only substitution. An unknown or
//     mutating inner (`$(zap)`, `$(touch x)`) fails — the substitution floor's
//     charter ("an allow rule for the outer literal can never silently approve
//     a hidden command") holds;
//   - the blanked OUTER must pass the worktree-escape REJECTIONS
//     (escapeRejectionsFree — the ONE rejection path shared with
//     segmentIsolationApprovable) as defense-in-depth, including the
//     pure-subshell discipline for a lone-placeholder residual (a `$(...)` in
//     command position EXECUTES its output — never cleared);
//   - it deliberately does NOT require the blanked outer to be read-only or on
//     the go allowlist — that is exactly what the configured Allow vouches for.
//     `go test $(git rev-parse HEAD)` clears; `go test $(zap)` surfaces.
//
// Fail-safe false on any extraction/blanking ambiguity. It is a sibling
// classifier: ReadOnlyShell/SubstitutionReadOnly/IsolationApprovable/plan-mode
// stay byte-for-byte unchanged. Positive soundness is fuzzed by
// FuzzFlooredConfiguredAllow.
func flooredAllowSafe(seg string) bool {
	if !HasSubstitutionOrGrouping(seg) {
		// FAIL-SAFE: this classifier ONLY decides whether the SUBSTITUTION floor
		// may be relaxed, so it is only ever consulted under a substitution. A
		// non-substitution segment has no floor to relax — return false rather
		// than the permissive escape-free answer, so a future caller that drops
		// the substitution guard can never get a true for an arbitrary (e.g.
		// destructive) plain command. The evaluator's floor branch always passes
		// a substitution-bearing segment, so this is unreachable on the live path
		// (pinned by TestFlooredAllowSafeNoSubstitutionFailsSafe).
		return false
	}
	inner, ok := extractSubstitutions(seg)
	if !ok {
		return false // ambiguity: fail safe.
	}
	for _, in := range inner {
		// The A1 inner contract: plain read-only, or itself a read-only
		// substitution (nested inners are separate entries in the recursive
		// extraction, so each level is independently checked).
		if !ReadOnlyShell(in) && !SubstitutionReadOnly(in) {
			return false
		}
	}
	blanked, okB := outerWithSubstitutionsBlanked(seg)
	if !okB {
		return false
	}
	return outerEscapeFree(seg, blanked)
}

// outerEscapeFree applies the worktree-escape rejection discipline to one
// (possibly blanked) outer command text: strip control-flow scaffolding, accept
// an empty residual (runs no program), require the pure-subshell shape for a
// lone-placeholder residual (a command-position substitution executes its
// OUTPUT — never escape-free), then apply the shared escape rejections. orig is
// the ORIGINAL (un-blanked) segment, needed for the pure-subshell check.
func outerEscapeFree(orig, text string) bool {
	residual := stripShellKeywords(text)
	trimmed := strings.TrimSpace(residual)
	if trimmed == "" {
		return true // pure scaffolding runs no command.
	}
	if trimmed == substitutionPlaceholder {
		return isPureSubshell(orig)
	}
	return escapeRejectionsFree(trimmed)
}

// escapeRejectionsFree is the ONE worktree-escape rejection path (issue #32),
// shared by segmentIsolationApprovable (A2) and flooredAllowSafe/outerEscapeFree
// so the two classifiers cannot drift: the resolved git SUBCOMMAND (past leading
// global flags) must not be a worktree-escape verb (worktreeEscapeGitSubcommands)
// nor shifted by a PATH-bearing global flag (`-C`/`--git-dir`/`--work-tree` —
// git pointed OUTSIDE the sandbox; an unresolvable subcommand fails safe), and a
// `go` invocation must not carry an escape flag (-exec/-toolexec/-overlay/-o).
// text is a keyword-stripped residual; callers handle the empty/placeholder
// shapes first.
func escapeRejectionsFree(text string) bool {
	canon := Canonicalize(strings.TrimSpace(text))
	fields := strings.Fields(canon)
	if len(fields) == 0 {
		return true
	}
	if fields[0] == "git" {
		sub, escapesPath, ok := gitSubcommand(fields)
		if !ok || escapesPath || worktreeEscapeGitSubcommands[sub] {
			return false
		}
	}
	if fields[0] == "go" && goArgsEscapeWorktree(fields[1:]) {
		return false
	}
	return true
}

// IsolationApprovable reports whether a (possibly compound) Shell command line is safe
// to AUTO-APPROVE for an ISOLATED (forked-worktree / force-copy) subagent that would
// otherwise hit the permission Ask floor (A2). It is a strict superset of
// SubstitutionReadOnly: every command — each SplitCommands segment AND every
// recursively-extracted substitution inner — must be read-only OR a worktree-safe
// `go {test,build,vet,list}`, none may be a worktree-escape verb (git push/config/
// remote/fetch/pull/clone), and substitution extraction must succeed. It fails safe
// (false) on any extraction ambiguity, any unrecognised/destructive command, or any
// escape verb — so a borderline command SURFACES to the human rather than auto-running
// in the sandbox. It NEVER mutates the read-only/plan-mode classifiers.
func IsolationApprovable(cmd string) bool {
	segs := SplitCommands(cmd)
	if len(segs) == 0 {
		return false
	}
	for _, seg := range segs {
		if HasSubstitutionOrGrouping(seg) {
			// Every inner command must itself be isolation-approvable, AND the blanked
			// outer must be too. Recurse via the inner extraction.
			inner, ok := extractSubstitutions(seg)
			if !ok {
				return false
			}
			for _, in := range inner {
				if !IsolationApprovable(in) {
					return false
				}
			}
			blanked, ok := outerWithSubstitutionsBlanked(seg)
			if !ok {
				return false
			}
			if !segmentIsolationApprovable(seg, blanked) {
				return false
			}
			continue
		}
		if !segmentIsolationApprovable(seg, seg) {
			return false
		}
	}
	return true
}

// wrapperFlagsTakeValue records, per stripped wrapper, the long/short flags that
// consume a following argument (so we skip the value too, not just the flag).
// Conservative: unknown flags that look like options are skipped as valueless.
var wrapperFlagsTakeValue = map[string]map[string]bool{
	"timeout": {"-s": true, "--signal": true, "-k": true, "--kill-after": true},
	"nice":    {"-n": true, "--adjustment": true},
	"env":     {"-u": true, "--unset": true, "-C": true, "--chdir": true},
	"stdbuf":  {"-i": true, "--input": true, "-o": true, "--output": true, "-e": true, "--error": true},
	"ionice":  {"-c": true, "--class": true, "-n": true, "--classdata": true, "-p": true, "--pid": true},
	"time":    {},
}

// wrapperLeadingPositionals records how many mandatory non-flag positional
// arguments a wrapper consumes before the wrapped command begins. `timeout`
// takes a required DURATION (`timeout 5 rm x`); the rest take none.
var wrapperLeadingPositionals = map[string]int{
	"timeout": 1,
}

// strippableWrappers is the CLOSED, audited set of process-wrapper commands that
// Canonicalize peels off before permission matching. It deliberately contains
// only transparent wrappers that do not change which program ultimately runs.
//
// It MUST NOT grow to include re-entrant launchers such as `docker exec`, `npx`,
// `devbox run` or `sudo`: those select or virtualize a different execution
// environment and stripping them would silently open a permission backdoor
// (doc 08 §10).
var strippableWrappers = map[string]bool{
	"timeout": true,
	"time":    true,
	"nice":    true,
	"env":     true,
	"stdbuf":  true,
	"ionice":  true,
}

// Canonicalize strips leading transparent process wrappers from a single Shell
// command so that permission matching sees the real program being run. Only the
// closed strippableWrappers set is removed (`timeout`, `time`, `nice`, `env`,
// `stdbuf`, `ionice`), together with their leading option flags and any values
// those flags consume.
//
// Re-entrant launchers are intentionally left intact: `docker exec foo rm x`,
// `npx ...`, `devbox run ...` and `sudo rm x` are returned unchanged, because
// stripping them would defeat the permission boundary.
//
// Canonicalize operates on a SINGLE simple command; callers that may receive a
// compound line should SplitCommands first.
func Canonicalize(cmd string) string {
	trimmed := strings.TrimSpace(cmd)
	for {
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			return trimmed
		}
		head := fields[0]
		if !strippableWrappers[head] {
			return trimmed
		}
		// Walk past the wrapper's own flags (and any values they consume), then
		// past its mandatory leading positionals (e.g. timeout's DURATION), to
		// find where the wrapped command begins.
		valueFlags := wrapperFlagsTakeValue[head]
		positionals := wrapperLeadingPositionals[head]
		i := 1
		for i < len(fields) {
			f := fields[i]
			if !strings.HasPrefix(f, "-") {
				if positionals > 0 {
					positionals--
					i++
					continue
				}
				break // first non-flag token after positionals is the program
			}
			// `--key=value` carries its own value; consume just this token.
			if strings.Contains(f, "=") {
				i++
				continue
			}
			i++
			if valueFlags[f] {
				i++ // also consume the separate value argument
			}
		}
		if i >= len(fields) {
			// Nothing left after the wrapper (e.g. bare `env`): leave as-is.
			return trimmed
		}
		trimmed = strings.Join(fields[i:], " ")
		// Loop to peel further nested wrappers (e.g. `timeout 5 nice rm x`).
	}
}

// readOnlyVerbs are first-token commands whose canonical use does not mutate the
// workspace. `git` is handled specially (only read-only subcommands qualify).
//
// Deliberately EXCLUDED are general-purpose interpreters whose program argument
// can execute arbitrary commands or write arbitrary files with no shell-level
// redirection (which the top-level `>`/writeIndicators checks would catch):
//   - awk: `awk 'BEGIN{system("…")}'`, `print | "cmd"`.
//   - sed: `sed -i` (in-place edit), the `w`/`W` script commands.
//
// These cannot be soundly classified read-only without parsing their language,
// so they are not listed and resolve to Ask (and are hard-denied under plan
// mode). Verbs that are read-only in normal use but mutate via a finite,
// well-defined set of flags (find, sort) ARE listed, and are guarded by
// verbArgsMutate below.
var readOnlyVerbs = map[string]bool{
	"ls": true, "cat": true, "grep": true, "egrep": true, "fgrep": true,
	"find": true, "rg": true, "head": true, "tail": true, "pwd": true,
	"echo": true, "wc": true, "stat": true, "file": true, "which": true,
	"whoami": true, "date": true, "tree": true, "diff": true, "sort": true,
	"uniq": true, "cut": true, "true": true,
}

// readOnlyGitSubcommands are the `git` subcommands that only inspect state.
//
// `config` is deliberately EXCLUDED: `git config` writes .git/config and can
// persist values that execute shell on later (allow-listed) git operations —
// e.g. an alias whose value starts with `!`, or core.pager/core.sshCommand/
// core.fsmonitor. Its only read-only forms (--get/--list) are not worth that
// bypass surface, so all `git config` resolves to Ask.
var readOnlyGitSubcommands = map[string]bool{
	"status": true, "log": true, "diff": true, "show": true, "branch": true,
	"remote": true, "ls-files": true, "rev-parse": true, "blame": true,
	"describe": true, "tag": true, "shortlog": true,
}

// verbArgsMutate reports whether a read-only verb's own arguments request a file
// write or command execution that the top-level redirection/writeIndicators
// checks miss. It guards verbs (find, sort) that are read-only in normal use but
// can mutate through a finite set of flags/primaries with no shell redirection.
func verbArgsMutate(verb string, args []string) bool {
	switch verb {
	case "find":
		// Primaries that delete, execute, or write files.
		for _, a := range args {
			switch a {
			case "-delete", "-exec", "-execdir", "-ok", "-okdir",
				"-fprintf", "-fprint", "-fprint0", "-fls":
				return true
			}
		}
	case "sort":
		// -o/--output writes results to a named file (no shell redirection).
		for _, a := range args {
			if a == "-o" || a == "--output" ||
				strings.HasPrefix(a, "-o") || strings.HasPrefix(a, "--output=") {
				return true
			}
		}
	}
	return false
}

// writeIndicators are tokens that, anywhere in a command, mark it as mutating
// regardless of the leading verb (output redirection and destructive verbs).
var writeIndicators = map[string]bool{
	"rm": true, "mv": true, "cp": true, "mkdir": true, "rmdir": true,
	"touch": true, "tee": true, "dd": true, "chmod": true, "chown": true,
	"ln": true, "truncate": true, "install": true,
}

// ReadOnlyShell reports whether a Shell command line is read-only, i.e. safe to
// run under plan mode. It is a conservative heuristic: it returns true only when
// EVERY simple command in a (possibly compound) line is recognised read-only,
// and false the moment it sees output redirection, a destructive verb, or an
// unrecognised command. Wrappers are canonicalized away before classification.
//
// Examples that are read-only: `ls`, `cat f`, `grep x f`, `git status`,
// `git log`, `git diff`. Examples that are NOT: anything containing `>`, `>>`,
// `rm`, `mv`, `mkdir`, `git commit`, or an unknown command.
func ReadOnlyShell(cmd string) bool {
	parts := SplitCommands(cmd)
	if len(parts) == 0 {
		return false
	}
	for _, p := range parts {
		if !simpleReadOnly(p) {
			return false
		}
	}
	return true
}

// simpleReadOnly classifies a single simple command (no shell operators).
func simpleReadOnly(cmd string) bool {
	// Command/process substitution or subshell grouping can hide an arbitrary
	// (possibly destructive) inner command the splitter does not decompose; fail
	// safe and treat the whole segment as not read-only.
	if HasSubstitutionOrGrouping(cmd) {
		return false
	}
	canon := Canonicalize(cmd)
	// Any output redirection makes it a write.
	if strings.ContainsAny(canon, ">") {
		return false
	}
	fields := strings.Fields(canon)
	if len(fields) == 0 {
		return false
	}
	// A destructive verb anywhere in the line disqualifies it.
	for _, f := range fields {
		if writeIndicators[f] {
			return false
		}
	}
	verb := fields[0]
	if verb == "git" {
		if len(fields) < 2 {
			return false
		}
		return readOnlyGitSubcommands[fields[1]]
	}
	if !readOnlyVerbs[verb] {
		return false
	}
	// A read-only verb can still mutate through its own arguments (e.g.
	// `find -delete`, `sort -o`); reject those.
	return !verbArgsMutate(verb, fields[1:])
}
