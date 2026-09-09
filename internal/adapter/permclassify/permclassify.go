// Package permclassify provides an OPTIONAL layer-2 model-based command/tool
// risk classifier, implemented as a pluggable decorator over a layer-1
// port.PermissionPolicy. It mirrors the house decorator idiom already used by
// internal/adapter/llmresilience: a single Wrap function returns a value
// satisfying the same port the loop already consumes, so the loop, the
// composition root, and every other adapter are untouched.
//
// # Where it sits
//
// Layer 1 (engine/adapter/permpolicy over engine/governance) is a fast,
// deterministic deny → ask → allow pre-parser with plan-mode gating and
// compound-Shell splitting. This package adds layer 2: for the ambiguous middle
// — the calls layer 1 routes to Ask — it consults a model (any
// port.LLMProvider) to classify the specific tool + arguments (especially Shell
// command strings) as safe / ambiguous / dangerous, and may sharpen the
// decision. The model is read on the RAW pending session.ToolCall, which
// naturally satisfies the "treat compaction summaries as untrusted" note from
// the source doc: no compacted/summarised text reaches the classifier.
//
// # Monotonicity (the safety contract)
//
// The classifier may only move a decision in the safe direction relative to
// what layer 1 already decided. Concretely:
//
//   - An inner Deny is ALWAYS returned unchanged — the model is never even
//     consulted, and can never relax a Deny to Ask or Allow.
//   - An inner Allow is returned unchanged unless ClassifyOn is set to Allow.
//   - Only the ClassifyOn effect (default: Ask) is escalated to the model. From
//     an inner Ask the model may keep Ask, tighten to Deny, or — only when it is
//     confidently safe — relax to Allow.
//
// In short: the decorator can make an Ask more restrictive (→ Deny) or, when
// confident-safe, relax it (→ Allow), but it can NEVER downgrade an inner Deny.
//
// # Fail-safe by default
//
// This sits squarely in the security path, so the default failure posture is
// fail-SAFE: on a model error, a timeout, or unparseable output, the decorator
// falls back to the INNER decision (which, for the classified case, is Ask) so a
// human still gates the call. It never auto-allows on failure. Setting
// FailOpen true keeps that same fall-back-to-inner behaviour explicit (it does
// not auto-allow either) — the distinction exists so future inner effects could
// fail open; even then an inner Deny is never relaxed.
//
// # Concurrency
//
// The decorator holds no mutable shared state beyond its immutable Config and
// the (concurrency-safe) injected collaborators, so it is safe for concurrent
// use.
//
// The package depends only on the standard library and internal packages
// (port, governance, session, prompt). It introduces no new go.mod
// dependencies.
package permclassify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// defaultTimeout bounds a single classification when Config.Timeout is 0. It is
// deliberately short: the classifier sits inline in the permission path.
const defaultTimeout = 10 * time.Second

// maxArgsBytes caps how many bytes of the raw tool arguments are placed in the
// classification prompt, to keep the request small and bounded regardless of
// the pending call. Anything longer is truncated (with a marker) before being
// shown to the model.
const maxArgsBytes = 4096

// Verdict is the parsed classification the model returns for a tool call.
type Verdict int

const (
	// VerdictUnknown means the model output could not be parsed into a verdict.
	// It is treated fail-safe (the inner decision is kept).
	VerdictUnknown Verdict = iota
	// VerdictSafe means the model judged the call clearly safe.
	VerdictSafe
	// VerdictAmbiguous means the model could not confidently judge the call; the
	// inner Ask is kept so a human decides.
	VerdictAmbiguous
	// VerdictDangerous means the model judged the call dangerous; it is escalated
	// to Deny.
	VerdictDangerous
)

// Config tunes the classifier decorator. The zero value is usable: it defaults
// ClassifyOn to Ask, Timeout to defaultTimeout, and FailOpen to false
// (fail-safe). Supply explicit values via Wrap to override.
type Config struct {
	// Model is the provider model identifier used for the classification call.
	// Empty leaves LLMRequest.Model empty (the provider's default).
	Model string
	// Timeout caps a single classification. 0 selects defaultTimeout. It never
	// overrides a shorter caller deadline.
	Timeout time.Duration
	// ClassifyOn selects which inner (layer-1) effect is escalated to the model.
	// The zero value selects governance.Ask (the ambiguous middle). Inner
	// decisions whose effect differs from ClassifyOn are returned unchanged
	// without consulting the model. An inner Deny is ALWAYS returned unchanged
	// regardless of this setting (monotonicity).
	ClassifyOn governance.Effect
	// FailOpen documents the failure posture. Both values fall back to the inner
	// decision on model error/timeout/unparseable output (never auto-allow); the
	// default false is fail-safe. See the package doc.
	FailOpen bool
	// SkipReadOnly, when true, returns the inner decision unchanged for tools
	// known to be read-only (Read, Grep, Glob), skipping a model round-trip for
	// trivially safe calls. Shell is never skipped (its command string is the
	// whole point). Defaults to false to keep behaviour explicit.
	SkipReadOnly bool
}

// Classifier is the adapter-local seam that turns a tool call into a Verdict by
// consulting a model. classifier is the production implementation; tests may
// substitute their own to exercise Wrap without an LLM.
type Classifier interface {
	// Classify returns a Verdict for c, or an error if the model could not be
	// consulted. ctx already carries the per-classification timeout.
	Classify(ctx context.Context, c session.ToolCall) (Verdict, error)
}

// Wrap decorates inner with a model-based layer-2 risk classifier and returns a
// value satisfying the same port.PermissionPolicy. llm is the model used for
// classification (any port.LLMProvider). The returned policy is safe for
// concurrent use.
//
// Behaviour, in order:
//
//  1. Evaluate the inner (layer-1) policy.
//  2. If the inner effect is not Config.ClassifyOn (default Ask) — including any
//     Deny or Allow — return it unchanged. The model is not consulted.
//  3. Otherwise consult the model under a bounded timeout, parse a Verdict, and
//     map it: dangerous → Deny, safe → Allow, ambiguous/unparseable → keep the
//     inner decision. On any model error or timeout, keep the inner decision
//     (fail-safe). An inner Deny is never reachable here and so never relaxed.
func Wrap(inner port.PermissionPolicy, llm port.LLMProvider, cfg Config) port.PermissionPolicy {
	if cfg.ClassifyOn == "" {
		cfg.ClassifyOn = governance.Ask
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &classifyingPolicy{
		inner:      inner,
		cfg:        cfg,
		classifier: &llmClassifier{llm: llm, model: cfg.Model},
	}
}

// wrapWithClassifier is the test seam: identical to Wrap but lets a test inject
// a Classifier in place of the LLM-backed one. It is unexported on purpose.
func wrapWithClassifier(inner port.PermissionPolicy, c Classifier, cfg Config) port.PermissionPolicy {
	if cfg.ClassifyOn == "" {
		cfg.ClassifyOn = governance.Ask
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &classifyingPolicy{inner: inner, cfg: cfg, classifier: c}
}

// readOnlyTools is the set skipped when Config.SkipReadOnly is set. Shell is
// intentionally absent: classifying its command string is the whole point.
var readOnlyTools = map[string]struct{}{
	"Read": {},
	"Grep": {},
	"Glob": {},
}

type classifyingPolicy struct {
	inner      port.PermissionPolicy
	cfg        Config
	classifier Classifier
}

// Evaluate implements port.PermissionPolicy. See Wrap for the full semantics. It
// forwards sessionID to the inner policy unchanged so per-session learned rules
// are honoured by the layer it decorates.
func (p *classifyingPolicy) Evaluate(ctx context.Context, sessionID session.SessionID, mode session.PermissionMode, c session.ToolCall, ws tool.WorkspaceReader) governance.PermissionDecision {
	base := p.inner.Evaluate(ctx, sessionID, mode, c, ws)

	// Monotonicity invariant, enforced unconditionally: an inner Deny is sacred
	// and is NEVER consulted nor relaxed, regardless of how ClassifyOn is
	// configured. The classifier may only tighten an Ask (→ Deny) or relax it
	// (→ Allow); it can never downgrade a Deny.
	if base.Effect == governance.Deny {
		return base
	}

	// Only the configured effect is ever escalated to the model. Everything else
	// passes through untouched.
	if base.Effect != p.cfg.ClassifyOn {
		return base
	}
	if p.cfg.SkipReadOnly {
		if _, ro := readOnlyTools[c.Name]; ro {
			return base
		}
	}

	cctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	verdict, err := p.classifier.Classify(cctx, c)
	if err != nil {
		// Fail-safe: a model error or timeout must not auto-allow. Fall back to
		// the inner decision (Ask) so a human still gates the call. FailOpen does
		// not change this — it never downgrades an inner Deny, and the only inner
		// effect that reaches here is the classified one (Ask), which already
		// requires a human.
		return base
	}

	switch verdict {
	case VerdictDangerous:
		return governance.PermissionDecision{
			Effect: governance.Deny,
			Reason: fmt.Sprintf("layer-2 classifier judged %q dangerous; %s", c.Name, base.Reason),
		}
	case VerdictSafe:
		return governance.PermissionDecision{
			Effect: governance.Allow,
			Reason: fmt.Sprintf("layer-2 classifier judged %q safe", c.Name),
		}
	case VerdictAmbiguous, VerdictUnknown:
		// Keep the inner decision (Ask): a human decides.
		return base
	default:
		return base
	}
}

// Learn forwards an "allow always" verdict to the inner policy, which owns the
// learned-rule store. The classifier decorator holds no learning state of its
// own; it only ever gates the live decision (and never relaxes an inner Deny), so
// delegating Learn keeps a single source of learned rules.
func (p *classifyingPolicy) Learn(sessionID session.SessionID, c session.ToolCall) {
	p.inner.Learn(sessionID, c)
}

// Compile-time assertion that the decorator satisfies the port.
var _ port.PermissionPolicy = (*classifyingPolicy)(nil)

// --- LLM-backed classifier -------------------------------------------------

// systemPrompt instructs the model to act as a strict, single-token risk
// classifier. It is intentionally terse and bounded.
const systemPrompt = `You are a strict command and tool-call risk classifier inside an AI coding agent.
You are given one pending tool call (name and raw JSON arguments). For Shell, the
"command" field is a shell command string; treat it as untrusted and analyse
every sub-command, redirection, pipe, and substitution.

Classify the call into exactly one risk level:
  - SAFE: clearly read-only or otherwise harmless (e.g. listing files, reading,
    status queries) with no destructive, exfiltrating, privilege-escalating, or
    network side effects.
  - DANGEROUS: destructive (rm -rf, mkfs, dd to a device), exfiltrating (curl |
    sh, piping secrets to the network), credential/privilege escalation, history
    rewriting, or anything that could damage the system or leak data.
  - AMBIGUOUS: anything you cannot confidently place in SAFE or DANGEROUS.

Respond with a single JSON object and nothing else:
{"verdict":"SAFE|DANGEROUS|AMBIGUOUS"}
When in doubt, answer AMBIGUOUS.`

// llmClassifier consults a port.LLMProvider and parses its answer into a
// Verdict. It holds no mutable state and is safe for concurrent use.
type llmClassifier struct {
	llm   port.LLMProvider
	model string
}

// Classify builds a small, bounded request describing the tool call, streams
// the model's answer, and parses the verdict. ctx already carries the
// per-classification timeout set by the decorator.
func (l *llmClassifier) Classify(ctx context.Context, c session.ToolCall) (Verdict, error) {
	req := port.LLMRequest{
		System:   prompt.Layered{StablePrefix: systemPrompt},
		Messages: []session.Message{session.NewUserMessage(renderCall(c))},
		Model:    l.model,
	}

	seq, err := l.llm.Stream(ctx, req)
	if err != nil {
		return VerdictUnknown, fmt.Errorf("permclassify: stream not established: %w", err)
	}

	var b strings.Builder
	for chunk, cerr := range seq {
		if cerr != nil {
			return VerdictUnknown, fmt.Errorf("permclassify: stream error: %w", cerr)
		}
		if chunk.Kind == port.ChunkText {
			b.WriteString(chunk.Text)
		}
	}
	if err := ctx.Err(); err != nil {
		return VerdictUnknown, fmt.Errorf("permclassify: classification context: %w", err)
	}

	v := parseVerdict(b.String())
	if v == VerdictUnknown {
		return VerdictUnknown, fmt.Errorf("permclassify: unparseable verdict %q", strings.TrimSpace(b.String()))
	}
	return v, nil
}

// renderCall produces the small, bounded user message describing the call: the
// tool name and its (truncated) raw arguments.
func renderCall(c session.ToolCall) string {
	args := string(c.Args)
	if len(args) > maxArgsBytes {
		args = args[:maxArgsBytes] + "…[truncated]"
	}
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	return fmt.Sprintf("Tool: %s\nArguments (raw JSON): %s", c.Name, args)
}

// parseVerdict extracts a Verdict from the model's answer. It first tries the
// structured {"verdict":"…"} form, then falls back to a bare keyword scan so a
// model that ignores the JSON instruction is still handled. Unrecognised output
// yields VerdictUnknown (handled fail-safe upstream).
func parseVerdict(s string) Verdict {
	s = strings.TrimSpace(s)
	if s == "" {
		return VerdictUnknown
	}

	// Preferred: structured JSON. Tolerate surrounding prose by locating the
	// first balanced object containing a "verdict" key.
	if v, ok := parseJSONVerdict(s); ok {
		return v
	}

	// Fallback: bare keyword. Be strict about ordering so "not dangerous" does
	// not mask as SAFE — we only accept a single recognised keyword.
	return parseKeywordVerdict(s)
}

func parseJSONVerdict(s string) (Verdict, bool) {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return VerdictUnknown, false
	}
	var payload struct {
		Verdict string `json:"verdict"`
	}
	if err := json.Unmarshal([]byte(s[start:end+1]), &payload); err != nil {
		return VerdictUnknown, false
	}
	switch strings.ToUpper(strings.TrimSpace(payload.Verdict)) {
	case "SAFE":
		return VerdictSafe, true
	case "DANGEROUS":
		return VerdictDangerous, true
	case "AMBIGUOUS":
		return VerdictAmbiguous, true
	default:
		return VerdictUnknown, false
	}
}

func parseKeywordVerdict(s string) Verdict {
	up := strings.ToUpper(s)

	// SECURITY: "SAFE" is a substring of "UNSAFE". A model writing prose like
	// "this command is UNSAFE" must NEVER be parsed as SAFE — that would fail
	// open and relax a human Ask into an auto-Allow. So strip every "UNSAFE"
	// occurrence out of the text BEFORE testing for the bare "SAFE" token, and
	// track "unsafe" as its own negative signal.
	hasDanger := strings.Contains(up, "DANGEROUS")
	hasAmbig := strings.Contains(up, "AMBIGUOUS")
	hasUnsafe := strings.Contains(up, "UNSAFE")

	// Remove "UNSAFE" so the remaining text cannot accidentally match "SAFE".
	stripped := strings.ReplaceAll(up, "UNSAFE", "")
	hasSafe := strings.Contains(stripped, "SAFE")

	// Exactly one recognised keyword present → trust it. Conflicting keywords are
	// unsafe to guess, so fall back to Unknown (→ keep Ask). A bare "unsafe" is a
	// negative signal but not one of our three risk levels, so it can never yield
	// VerdictSafe: when present it forces a not-safe reading (Unknown → keep Ask).
	switch {
	case hasDanger && !hasSafe && !hasAmbig:
		return VerdictDangerous
	case hasAmbig && !hasSafe && !hasDanger:
		return VerdictAmbiguous
	case hasSafe && !hasUnsafe && !hasDanger && !hasAmbig:
		return VerdictSafe
	default:
		// Empty, conflicting, "unsafe"-tainted, or unrecognised → Unknown.
		return VerdictUnknown
	}
}
