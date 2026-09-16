package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/app"
)

// fakeSeam builds a trustSeam over in-memory fakes so every prompt branch is
// exercised offline — no real terminal, no real ~/.config, no real workspace. It
// records whether remember was called and at what time.
type fakeSeam struct {
	decision     app.TrustDecision
	hasAuthority bool
	rememberErr  error

	remembered   bool
	rememberedAt time.Time
}

func (f *fakeSeam) seam() trustSeam {
	fixed := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	return trustSeam{
		resolve:      func() app.TrustDecision { return f.decision },
		hasAuthority: func() bool { return f.hasAuthority },
		remember: func(at time.Time) error {
			f.remembered = true
			f.rememberedAt = at
			return f.rememberErr
		},
		now: func() time.Time { return fixed },
	}
}

const ttyOn, ttyOff = true, false

// TestResolveTrustForRunAlreadyTrustedNoPrompt asserts an already-trusted decision
// proceeds trusted WITHOUT prompting, even on a TTY — across ALL three trusted
// sources (flag / declared / remembered+anchor-match), so a future branch that
// prompts on an already-trusted source can't slip through.
func TestResolveTrustForRunAlreadyTrustedNoPrompt(t *testing.T) {
	for _, src := range []app.TrustSource{app.TrustFlag, app.TrustDeclared, app.TrustRemembered} {
		t.Run(src.String(), func(t *testing.T) {
			f := &fakeSeam{decision: app.TrustDecision{Trusted: true, Source: src}, hasAuthority: true}
			var errw bytes.Buffer
			out := resolveTrustForRun(f.seam(), "/ws", strings.NewReader("n\n"), &errw, ttyOn)
			if !out.trusted || out.prompted || out.persisted {
				t.Fatalf("already-trusted (%s): got %+v, want trusted=true prompted=false persisted=false", src, out)
			}
			if f.remembered {
				t.Fatalf("already-trusted (%s) path must not write the registry", src)
			}
			if errw.Len() != 0 {
				t.Fatalf("already-trusted (%s) path printed a prompt: %q", src, errw.String())
			}
		})
	}
}

// TestResolveTrustForRunNoAuthorityNoPrompt asserts a not-trusted workspace with NO
// project authority set proceeds untrusted WITHOUT prompting (nothing to gate).
func TestResolveTrustForRunNoAuthorityNoPrompt(t *testing.T) {
	f := &fakeSeam{decision: app.TrustDecision{Trusted: false, Source: app.TrustNone}, hasAuthority: false}
	var errw bytes.Buffer
	out := resolveTrustForRun(f.seam(), "/ws", strings.NewReader("t\n"), &errw, ttyOn)
	if out.trusted || out.prompted {
		t.Fatalf("no-authority: got %+v, want trusted=false prompted=false", out)
	}
	if errw.Len() != 0 {
		t.Fatalf("no-authority path printed a prompt: %q", errw.String())
	}
}

// TestResolveTrustForRunTrustPersists asserts answering "t" (trust) on a TTY with
// authority present trusts the run AND persists via remember (with the clock time).
func TestResolveTrustForRunTrustPersists(t *testing.T) {
	f := &fakeSeam{decision: app.TrustDecision{Trusted: false, Source: app.TrustNone}, hasAuthority: true}
	var errw bytes.Buffer
	out := resolveTrustForRun(f.seam(), "/ws", strings.NewReader("t\n"), &errw, ttyOn)
	if !out.trusted || !out.prompted || !out.persisted {
		t.Fatalf("trust: got %+v, want trusted=true prompted=true persisted=true", out)
	}
	if !f.remembered {
		t.Fatal("trust answer did not call remember (not persisted)")
	}
	if f.rememberedAt != time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC) {
		t.Fatalf("remember got clock time %v, want the injected fixed time", f.rememberedAt)
	}
}

// TestResolveTrustForRunTrustOnceNotPersisted asserts "o" (once) trusts the run but
// does NOT persist (remember never called).
func TestResolveTrustForRunTrustOnceNotPersisted(t *testing.T) {
	f := &fakeSeam{decision: app.TrustDecision{Trusted: false, Source: app.TrustNone}, hasAuthority: true}
	var errw bytes.Buffer
	out := resolveTrustForRun(f.seam(), "/ws", strings.NewReader("o\n"), &errw, ttyOn)
	if !out.trusted || !out.prompted {
		t.Fatalf("once: got %+v, want trusted=true prompted=true", out)
	}
	if out.persisted || f.remembered {
		t.Fatal("trust-once must NOT persist (remember was called)")
	}
}

// TestResolveTrustForRunAcceptVariants pins the accept-token normalization
// (ToLower + TrimSpace) and the aliases: t/trust/y/yes ⇒ trusted+persisted;
// o/once ⇒ trusted+NOT-persisted. A regression dropping case/space normalization or
// an alias fails here.
func TestResolveTrustForRunAcceptVariants(t *testing.T) {
	persistTokens := []string{" T \n", "YES\n", "  trust  \n", "Y\n", "TRUST\n"}
	for _, tok := range persistTokens {
		f := &fakeSeam{decision: app.TrustDecision{Trusted: false, Source: app.TrustNone}, hasAuthority: true}
		var errw bytes.Buffer
		out := resolveTrustForRun(f.seam(), "/ws", strings.NewReader(tok), &errw, ttyOn)
		if !out.trusted || !out.persisted || !f.remembered {
			t.Fatalf("persist token %q: got %+v remembered=%v, want trusted+persisted", tok, out, f.remembered)
		}
	}

	onceTokens := []string{"O\n", " once \n", "ONCE\n"}
	for _, tok := range onceTokens {
		f := &fakeSeam{decision: app.TrustDecision{Trusted: false, Source: app.TrustNone}, hasAuthority: true}
		var errw bytes.Buffer
		out := resolveTrustForRun(f.seam(), "/ws", strings.NewReader(tok), &errw, ttyOn)
		if !out.trusted || out.persisted || f.remembered {
			t.Fatalf("once token %q: got %+v remembered=%v, want trusted + NOT persisted", tok, out, f.remembered)
		}
	}
}

// TestResolveTrustForRunDeclineUntrusted asserts "n" / default / empty leaves the
// run untrusted and persists nothing.
func TestResolveTrustForRunDeclineUntrusted(t *testing.T) {
	for _, ans := range []string{"n\n", "no\n", "\n", "garbage\n"} {
		f := &fakeSeam{decision: app.TrustDecision{Trusted: false, Source: app.TrustNone}, hasAuthority: true}
		var errw bytes.Buffer
		out := resolveTrustForRun(f.seam(), "/ws", strings.NewReader(ans), &errw, ttyOn)
		if out.trusted {
			t.Fatalf("answer %q: got trusted=true, want untrusted", ans)
		}
		if !out.prompted {
			t.Fatalf("answer %q: expected prompted=true", ans)
		}
		if f.remembered {
			t.Fatalf("answer %q: must not persist", ans)
		}
	}
}

// TestResolveTrustForRunDriftReprompts asserts a DRIFTED remembered entry RE-PROMPTS
// (not silently trusted, not silently dropped) and that answering trust re-persists
// the new anchor. The re-prompt must mention the workspace CHANGED.
func TestResolveTrustForRunDriftReprompts(t *testing.T) {
	// Drifted=true, Trusted=false, and hasAuthority deliberately FALSE to prove the
	// drift path prompts on its own (a drifted repo whose authority was deleted still
	// re-prompts because the remembered grant must be re-confirmed).
	f := &fakeSeam{decision: app.TrustDecision{Trusted: false, Source: app.TrustNone, Drifted: true}, hasAuthority: false}
	var errw bytes.Buffer
	out := resolveTrustForRun(f.seam(), "/ws", strings.NewReader("t\n"), &errw, ttyOn)
	if !out.trusted || !out.prompted || !out.persisted {
		t.Fatalf("drift+trust: got %+v, want trusted=true prompted=true persisted=true", out)
	}
	if !f.remembered {
		t.Fatal("drift re-prompt answered trust did not re-persist the new anchor")
	}
	if !strings.Contains(strings.ToUpper(errw.String()), "CHANGED") {
		t.Fatalf("drift re-prompt did not mention the workspace CHANGED: %q", errw.String())
	}
}

// TestResolveTrustForRunNonTTYUntrusted asserts the fail-direction: a not-trusted
// workspace with authority but NO interactive terminal proceeds UNTRUSTED without
// prompting or blocking, and never persists.
func TestResolveTrustForRunNonTTYUntrusted(t *testing.T) {
	f := &fakeSeam{decision: app.TrustDecision{Trusted: false, Source: app.TrustNone}, hasAuthority: true}
	var errw bytes.Buffer
	// Feed "t\n" to prove it is NOT read (no auto-trust off a pipe).
	out := resolveTrustForRun(f.seam(), "/ws", strings.NewReader("t\n"), &errw, ttyOff)
	if out.trusted || out.prompted {
		t.Fatalf("non-TTY: got %+v, want trusted=false prompted=false", out)
	}
	if f.remembered {
		t.Fatal("non-TTY must never persist")
	}
	if !strings.Contains(errw.String(), "not a terminal") {
		t.Fatalf("non-TTY note not printed: %q", errw.String())
	}
}

// TestResolveTrustForRunNonTTYDriftUntrusted asserts a DRIFTED entry on a non-TTY
// also fails safe to untrusted (never silently re-grants off a pipe).
func TestResolveTrustForRunNonTTYDriftUntrusted(t *testing.T) {
	f := &fakeSeam{decision: app.TrustDecision{Trusted: false, Source: app.TrustNone, Drifted: true}, hasAuthority: true}
	var errw bytes.Buffer
	out := resolveTrustForRun(f.seam(), "/ws", strings.NewReader("t\n"), &errw, ttyOff)
	if out.trusted || f.remembered {
		t.Fatalf("non-TTY drift: got %+v remembered=%v, want untrusted, not persisted", out, f.remembered)
	}
}

// TestTrustPromptCopyCoversEveryOperatorOutcome ensures the same concise trust
// boundary is visible whether the operator accepts, declines, reconsents after
// drift, or cannot be prompted on a non-TTY.
func TestTrustPromptCopyCoversEveryOperatorOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		drifted, tty bool
		want         string
	}{
		{"remember", "t\n", false, ttyOn, "workspace trusted and remembered"},
		{"once", "o\n", false, ttyOn, "this run only (not remembered)"},
		{"decline", "n\n", false, ttyOn, "ALLOW grants stay disabled"},
		{"drift", "n\n", true, ttyOn, "CHANGED since you trusted it"},
		{"non-tty", "t\n", false, ttyOff, "stdin is not a terminal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSeam{decision: app.TrustDecision{Source: app.TrustNone, Drifted: tc.drifted}, hasAuthority: true}
			var errw bytes.Buffer
			resolveTrustForRun(f.seam(), "/ws", strings.NewReader(tc.answer), &errw, tc.tty)
			got := errw.String()
			if !strings.Contains(got, tc.want) {
				t.Fatalf("output missing outcome %q: %q", tc.want, got)
			}
			for _, phrase := range []string{"project instructions", "soul", "ALLOW grants", "Project DENY and ASK rules"} {
				if !strings.Contains(strings.ToLower(got), strings.ToLower(phrase)) {
					t.Fatalf("output missing disclosure %q: %q", phrase, got)
				}
			}
		})
	}
}

// TestResolveTrustForRunRememberFailureFailSoft asserts a persist failure on "trust"
// is fail-soft: the run still proceeds trusted (persisted=false), a warning printed.
func TestResolveTrustForRunRememberFailureFailSoft(t *testing.T) {
	f := &fakeSeam{
		decision:     app.TrustDecision{Trusted: false, Source: app.TrustNone},
		hasAuthority: true,
		rememberErr:  errFakeWrite,
	}
	var errw bytes.Buffer
	out := resolveTrustForRun(f.seam(), "/ws", strings.NewReader("t\n"), &errw, ttyOn)
	if !out.trusted {
		t.Fatalf("remember-failure: run must still be trusted, got %+v", out)
	}
	if out.persisted {
		t.Fatal("remember-failure must report persisted=false")
	}
	if !strings.Contains(errw.String(), "could not persist") {
		t.Fatalf("remember-failure warning not printed: %q", errw.String())
	}
}

// TestPromptSanitizesWorkspacePath asserts a workspace path carrying terminal
// escapes (CWE-150) is neutralised in the prompt echo — the raw escape bytes never
// reach the terminal. Asserts on the EFFECT (escapes stripped), not a literal payload.
func TestPromptSanitizesWorkspacePath(t *testing.T) {
	// ESC + a control byte + a normal segment.
	evil := "/ws/\x1b[2Jboom\x07/repo"
	f := &fakeSeam{decision: app.TrustDecision{Trusted: false, Source: app.TrustNone}, hasAuthority: true}
	var errw bytes.Buffer
	resolveTrustForRun(f.seam(), evil, strings.NewReader("n\n"), &errw, ttyOn)

	out := errw.String()
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Fatalf("prompt echo contained raw terminal escapes: %q", out)
	}
	// The benign characters of the path must survive (sanitization strips control
	// bytes only, not the visible path).
	if !strings.Contains(out, "boom") || !strings.Contains(out, "repo") {
		t.Fatalf("sanitization mangled the visible path: %q", out)
	}
}

// TestSanitizeTrustEchoStripsControls is a focused unit test on the sanitizer. It
// covers C0/DEL control bytes AND the spoof-capable Unicode the hardening adds:
// zero-width (U+200B) and bidi-override (U+202E RLO, U+2066 LRI) runes.
func TestSanitizeTrustEchoStripsControls(t *testing.T) {
	got := sanitizeTrustEcho("/a\x1b[31m/b\nc\t/d\x7f")
	if strings.ContainsAny(got, "\x1b\n\t\x7f") {
		t.Fatalf("sanitizeTrustEcho left control bytes: %q", got)
	}
	if got != "/a[31m/b"+"c"+"/d" {
		t.Fatalf("sanitizeTrustEcho = %q", got)
	}

	// Zero-width + bidi-control runes must be neutralized too (defense-in-depth on
	// the path-echo spoof surface).
	const zwsp, rlo, lri = '​', '‮', '⁦'
	spoof := "/repo" + string(zwsp) + "/evil" + string(rlo) + "x" + string(lri) + "y"
	clean := sanitizeTrustEcho(spoof)
	if strings.ContainsRune(clean, zwsp) || strings.ContainsRune(clean, rlo) || strings.ContainsRune(clean, lri) {
		t.Fatalf("sanitizeTrustEcho left zero-width/bidi runes: %q", clean)
	}
	if clean != "/repo/evilxy" {
		t.Fatalf("sanitizeTrustEcho(spoof) = %q, want /repo/evilxy", clean)
	}
}

// TestProdTrustSeamWiring is a smoke test on the REAL prodTrustSeam field bindings
// (a swapped resolve↔hasAuthority, or a nil clock, currently ships green without
// it). It points the production app.* functions at a temp XDG config dir (via
// XDG_CONFIG_HOME, honoured by xdgconfig.OSEnv) and a temp workspace carrying a
// project soul (authority present, not yet trusted), then asserts each of the four
// seam fields does the right thing and round-trips:
//
//   - resolve().Trusted == false (a fresh untrusted workspace),
//   - hasAuthority() == true (the soul is authority),
//   - remember(t) persists, after which resolve() == TrustRemembered (the clock seam
//     is non-nil and the write+read round-trips through the real registry).
//
// It never touches the developer's real ~/.config (XDG_CONFIG_HOME is a t.TempDir).
// It must NOT run in parallel (t.Setenv).
func TestProdTrustSeamWiring(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// A real workspace with a project soul ⇒ authority present, untrusted.
	wsBase := t.TempDir()
	ws := filepath.Join(wsBase, "repo")
	if err := os.MkdirAll(filepath.Join(ws, ".mecatl"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".mecatl", "soul.md"), []byte("project persona"), 0o644); err != nil {
		t.Fatalf("write soul: %v", err)
	}

	seam := prodTrustSeam(embeddedConfig(config{workspace: ws}, port.NopDiagnostics{}))

	if d := seam.resolve(); d.Trusted || d.Drifted {
		t.Fatalf("prod resolve(): %+v, want untrusted/undrifted", d)
	}
	if !seam.hasAuthority() {
		t.Fatal("prod hasAuthority() = false, want true (soul present) — resolve↔hasAuthority may be swapped")
	}
	if seam.now == nil {
		t.Fatal("prod seam.now is nil (clock seam unwired)")
	}
	if err := seam.remember(seam.now()); err != nil {
		t.Fatalf("prod remember(): %v", err)
	}
	if d := seam.resolve(); !d.Trusted || d.Source != app.TrustRemembered {
		t.Fatalf("prod resolve() after remember(): %+v, want Trusted/remembered (round-trip)", d)
	}
}

// errFakeWrite is a sentinel for the remember-failure test.
var errFakeWrite = fakeErr("disk full")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }
