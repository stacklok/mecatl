package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/stacklok/mecatl/internal/app"
)

// trust.go is the mecatui PRE-TUI first-encounter trust prompt (Workspace-Trust
// feature, Phase 2c — the FINAL phase, and the only PRODUCTION caller of
// app.RememberTrust → workspacetrust.Remember).
//
// # Why here (the render-layer rule)
//
// The prompt is COMPOSITION-side: it runs in the cmd/mecatui main, in the pre-TUI
// window (before the Bubble Tea alt screen starts, where the other startup stderr
// notices already live). It is NOT in the ui/ render package — ui/theme/client
// render purely from proto Events and import no engine/... or internal/... package. The prompt
// imports internal/app (the trust fold + the Remember write seam), which is allowed
// HERE: cmd/mecatui's main is a composition root, exactly like cmd/mecated. No proto
// event is added (the in-TUI trust modal is the cut Phase 3).
//
// # The flow
//
// resolveTrustForRun folds the SAME app.ResolveTrust decision Build will compute,
// then decides:
//
//   - already trusted (flag / declared / remembered+anchor-match) ⇒ NO prompt,
//     proceed trusted (Build re-resolves to the same answer).
//   - NOT trusted but the workspace has NO project authority set ⇒ NO prompt,
//     proceed untrusted (nothing a trust grant would admit — don't nag).
//   - NOT trusted AND authority present (TrustNone+authority), OR Drifted (a
//     remembered entry whose identity anchor changed) ⇒ PROMPT. Drift is a
//     RE-PROMPT ("this repo changed since you trusted it").
//
// The prompt answers map to: trust (persist via app.RememberTrust + trust this
// run), trust-once (trust this run, persist NOTHING), no/default/empty (untrusted).
// When the answer trusts the run, main sets cfg.trustProject=true so Build's own
// fold short-circuits to TrustFlag — the prompt OUTCOME wins; Build does NOT
// re-resolve to a different answer or re-prompt (no double resolution).
//
// # Fail-direction (MUST-FIX 5.4) + security (MUST-FIX 5.3)
//
//   - NON-TTY (piped / headless / no interactive operator): we CANNOT prompt, so we
//     NEVER block waiting on input and NEVER auto-trust — we default to UNTRUSTED
//     for this run. Detected by main via term.IsTerminal on stdin's fd.
//   - The workspace path is SANITIZED (terminal escapes stripped) before it is
//     echoed into the prompt, so a repo dir named with ANSI/OSC escapes cannot
//     corrupt or spoof the prompt (CWE-150). sanitizeTrustEcho is the composition-
//     side twin of ui.sanitizeTerminal (the ui copy is unexported in a sibling
//     package; the prompt is not in ui/, so it carries its own small sanitizer, as
//     the design specs).

// trustSeam is the injectable set of trust operations the prompt needs, so the
// whole flow is unit-testable offline without a real terminal, a real
// ~/.config, or a real workspace. The production binding (prodTrustSeam) wraps
// internal/app; tests inject fakes (in-memory authority/decision + a Remember spy).
type trustSeam struct {
	// resolve folds the trust decision (app.ResolveTrust) — used both to decide
	// whether to prompt and to recognise an already-trusted / drifted workspace.
	resolve func() app.TrustDecision
	// hasAuthority reports whether the workspace carries a project authority set
	// worth gating (app.HasProjectAuthority).
	hasAuthority func() bool
	// remember persists a "trust" grant with the live anchor at the given time
	// (app.RememberTrust). Only called on the "trust" (persist) answer.
	remember func(at time.Time) error
	// now is the clock seam for the persisted trustedAt timestamp (time.Now in
	// production), injectable so tests are deterministic.
	now func() time.Time
}

// prodTrustSeam binds the prompt to internal/app for cfg.Workspace.
func prodTrustSeam(cfg app.Config) trustSeam {
	return trustSeam{
		resolve:      func() app.TrustDecision { return app.ResolveTrust(cfg) },
		hasAuthority: func() bool { return app.HasProjectAuthority(cfg) },
		remember:     func(at time.Time) error { return app.RememberTrust(cfg, at) },
		now:          time.Now,
	}
}

// trustOutcome is the prompt's answer to "should this run be trusted?". trusted is
// the effective per-run bool main feeds into cfg.trustProject (only when true —
// monotonic-positive). prompted records whether the operator was actually asked
// (for the slog/test narration); persisted records whether a registry entry was
// written (only on the "trust" answer).
type trustOutcome struct {
	trusted   bool
	prompted  bool
	persisted bool
}

const trustDisclosure = "Trust enables project instructions and project ALLOW grants. Project DENY and ASK rules always apply."
const untrustedDisclosure = "Project instructions and project ALLOW grants stay disabled. Project DENY and ASK rules still apply."

// resolveTrustForRun runs the pre-TUI trust gate and returns the per-run trust
// outcome. It NEVER errors out of band and NEVER blocks: a Remember write failure
// is logged to errw and the run still proceeds trusted (fail-soft); a non-TTY
// skips the prompt and proceeds untrusted (fail-safe).
//
// stdin/errw/isTTY are injected (os.Stdin / os.Stderr / term.IsTerminal in
// production) so every branch is unit-testable without a terminal.
func resolveTrustForRun(seam trustSeam, workspace string, stdin io.Reader, errw io.Writer, isTTY bool) trustOutcome {
	d := seam.resolve()

	// Already trusted (flag / declared / remembered+anchor-match): nothing to ask.
	if d.Trusted {
		return trustOutcome{trusted: true}
	}

	// Untrusted. Prompt only when there is something a trust grant would admit:
	// either a drifted remembered entry (re-prompt) or a fresh workspace that
	// carries a project authority set. A bare untrusted repo with no authority is
	// left untrusted silently — no nag.
	if !d.Drifted && !seam.hasAuthority() {
		return trustOutcome{trusted: false}
	}

	// We would prompt — but only if there is an interactive operator on a TTY.
	// Otherwise (piped / headless) we CANNOT ask: fail-safe to UNTRUSTED for this
	// run. Never block on input; never auto-trust.
	if !isTTY {
		_, _ = fmt.Fprintf(errw, "mecatui: workspace %s is not trusted and stdin is not a terminal; continuing without trusting it. %s Run interactively, pass --trust-project, or add it to trustedWorkspaces to trust it.\n", sanitizeTrustEcho(workspace), untrustedDisclosure)
		return trustOutcome{trusted: false}
	}

	return askTrust(seam, workspace, d.Drifted, stdin, errw)
}

// askTrust prints the prompt to errw, reads one line from stdin, and applies the
// answer. It is reached only when isTTY is true and a prompt is warranted.
func askTrust(seam trustSeam, workspace string, drifted bool, stdin io.Reader, errw io.Writer) trustOutcome {
	safe := sanitizeTrustEcho(workspace)
	pf := func(format string, a ...any) { _, _ = fmt.Fprintf(errw, format, a...) }

	pf("\n")
	if drifted {
		pf("mecatui: this workspace CHANGED since you trusted it:\n  %s\n", safe)
		pf("Its project instructions, agents, commands, or skills changed. %s\nTrust them again?\n", trustDisclosure)
	} else {
		pf("mecatui: do you trust the project files in this workspace?\n  %s\n", safe)
		pf("%s\n", trustDisclosure)
	}
	pf("[t]rust and remember / [o]nce (this run only) / [n]o (default): ")

	answer := readLine(stdin)
	switch answer {
	case "t", "trust", "y", "yes":
		out := trustOutcome{trusted: true, prompted: true}
		if err := seam.remember(seam.now()); err != nil {
			// Fail-soft: a failed persist never aborts startup; the run still
			// proceeds trusted, the operator is just not remembered next time.
			pf("mecatui: WARNING: could not persist trust for this workspace (%v); trusting this run only.\n", err)
			return out
		}
		out.persisted = true
		pf("mecatui: workspace trusted and remembered.\n")
		return out
	case "o", "once":
		pf("mecatui: workspace trusted for this run only (not remembered).\n")
		return trustOutcome{trusted: true, prompted: true}
	default:
		// "n", "no", empty (just Enter), or anything unrecognised ⇒ the safe
		// default: untrusted this run, nothing persisted.
		pf("mecatui: workspace not trusted. %s The agent can still use your configuration and built-in tools.\n", untrustedDisclosure)
		return trustOutcome{trusted: false, prompted: true}
	}
}

// readLine reads a single line from r, trims surrounding whitespace, and
// lowercases it. EOF / read error yields "" (the safe default → untrusted).
func readLine(r io.Reader) string {
	br := bufio.NewReader(r)
	line, _ := br.ReadString('\n')
	return strings.ToLower(strings.TrimSpace(line))
}

// sanitizeTrustEcho strips spoof-capable runes from a string before it is echoed
// into the pre-TUI prompt (CWE-150 terminal-escape injection). The workspace
// path/name is attacker-influencable (a repo dir name can embed escapes or
// invisible/reordering Unicode to redraw, hide, or visually reorder the path), so it
// MUST be neutralised before it reaches the terminal. This is the composition-side
// twin of ui.sanitizeTerminal, hardened for the path-echo surface; it removes:
//
//   - all C0 control bytes (0x00–0x1F), DEL (0x7F), and C1 controls (0x80–0x9F) —
//     ESC and friends. A path has no legitimate control byte, so (unlike the ui
//     sanitizer) it does not even preserve \n/\t (a newline in a path is itself a
//     spoof vector here);
//   - zero-width / invisible runes (U+200B–U+200D ZWSP/ZWNJ/ZWJ, U+2060 word joiner,
//     U+FEFF BOM/ZWNBSP) that could HIDE part of a path; and
//   - bidirectional-override / isolate controls (U+200E/U+200F LRM/RLM,
//     U+202A–U+202E embeddings+RLO, U+2066–U+2069 isolates) that could visually
//     REORDER a path so a trusted-looking prefix masks a malicious one (the
//     "Trojan Source" class).
func sanitizeTrustEcho(s string) string {
	if !strings.ContainsFunc(s, isTrustControl) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isTrustControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isTrustControl reports whether r is a rune to strip from the prompt echo: any C0
// control byte, DEL, a C1 control, or a zero-width / bidi-control rune. A filesystem
// path has no legitimate use for any of these.
func isTrustControl(r rune) bool {
	switch {
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true // C0 controls, DEL, C1 controls
	case r == 0x200b || r == 0x200c || r == 0x200d || r == 0x2060 || r == 0xfeff:
		return true // zero-width / invisible (ZWSP, ZWNJ, ZWJ, word joiner, BOM)
	case r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
		return true // bidi marks / embeddings / overrides / isolates (Trojan Source)
	default:
		return false
	}
}
