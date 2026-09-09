package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// BranchSummary is the compact, transcript-free view of one candidate branch the
// judge scores. It is exactly the information a human would get from the joined
// summary — Label + Summary (and whether the branch Failed) — never the branch's
// intermediate transcript, so judging preserves Parallel's context-isolation
// guarantee.
type BranchSummary struct {
	// Label is the branch's stable, human-meaningful tag (e.g. "branch-2").
	Label string
	// Summary is the branch's terminal result text.
	Summary string
	// Failed reports whether the branch failed. ParallelTool only ever passes
	// SUCCESSFUL candidates to a judge (you cannot pick a crashed branch), so this
	// is false in the default flow; the field exists so a future caller (e.g. a
	// team tournament) can pass the full set if it wants.
	Failed bool
}

// BranchJudge selects a winning branch from candidate summaries. It is the reusable
// selection seam for the Parallel "judge"/"best" strategy (and, later, a team
// tournament finish — AGENT-TEAMS-SPIKE §8.7). It is an interface so engine/agent
// never imports an adapter (the default impl runs an *Engine injected by the
// composition root) and so a non-LLM scorer can be substituted in tests/future.
type BranchJudge interface {
	// Judge picks the winner among candidates, guided by criteria. It returns the
	// 0-based position WITHIN candidates (not a branch index) and a short rationale.
	// Implementations MUST be non-interactive and bounded. ParallelTool treats a nil
	// error with an out-of-range index — or any error — as "fall back to the first
	// successful branch", so a judge must never be load-bearing for correctness.
	Judge(ctx context.Context, candidates []BranchSummary, criteria string) (winner int, rationale string, err error)
}

// defaultJudgeRubric is the criteria the judge optimises for when the caller
// supplies none.
const defaultJudgeRubric = "pick the most correct, complete, and maintainable result"

// engineJudge is the default BranchJudge: it runs a dedicated, injected child
// *Engine (built in the composition root — tool-less / read-only so a text scorer
// needs no workspace) over a prompt assembled from the candidate summaries, drains
// it with the SAME drainChild used by Subagent/Parallel (so it inherits the non-interactive
// contract for free), and parses a tolerant JSON verdict. It imports only
// session/tool — layer-clean — and is constructed in app where the judge Engine is
// built (mirroring how ParallelTool/SubagentTool are constructed there).
type engineJudge struct {
	engine    *Engine
	limits    session.Limits
	childMode session.PermissionMode
	idPrefix  string
}

// EngineJudgeOption configures an engineJudge.
type EngineJudgeOption func(*engineJudge)

// WithJudgeLimits overrides the judge run's stop conditions (default
// defaultChildLimits).
func WithJudgeLimits(l session.Limits) EngineJudgeOption {
	return func(j *engineJudge) { j.limits = l }
}

// WithJudgeSessionPrefix sets the prefix used to derive the judge session id
// (default "fork-judge").
func WithJudgeSessionPrefix(p string) EngineJudgeOption {
	return func(j *engineJudge) {
		if strings.TrimSpace(p) != "" {
			j.idPrefix = p
		}
	}
}

// NewEngineJudge constructs the default LLM-over-child-Engine BranchJudge. engine
// must be non-nil; it is the dedicated judge Engine the composition root builds
// (its own catalog/provider, distinct from the branch child Engine so their LLM
// calls never interleave). It panics on a nil engine — a judge with no loop to run
// is a composition-root programming error.
func NewEngineJudge(engine *Engine, opts ...EngineJudgeOption) BranchJudge {
	if engine == nil {
		panic("agent: NewEngineJudge requires a non-nil judge Engine")
	}
	j := &engineJudge{
		engine:    engine,
		limits:    defaultChildLimits,
		childMode: session.ModeDefault,
		idPrefix:  "fork-judge",
	}
	for _, o := range opts {
		o(j)
	}
	return j
}

// judgeVerdict is the structured output the judge is asked to emit. Winner is
// 1-based (a branch NUMBER as shown in the prompt), translated to a 0-based
// candidates position by Judge.
type judgeVerdict struct {
	Winner    int    `json:"winner"`
	Rationale string `json:"rationale"`
}

// Judge runs the judge Engine over the candidate summaries and returns the chosen
// 0-based position within candidates. Any parse failure / out-of-range winner /
// run error returns (0, <note>, nil) — ParallelTool then keeps the first successful
// branch — so the judge can never hard-fail a Parallel call. An empty candidate set is
// a programming error (ParallelTool only calls with ≥2) and returns an error.
func (j *engineJudge) Judge(ctx context.Context, candidates []BranchSummary, criteria string) (int, string, error) {
	if len(candidates) == 0 {
		return 0, "", fmt.Errorf("engineJudge: no candidates to judge")
	}

	// The judge needs no real workspace (it runs no tools); a tool-less in-memory
	// root keeps it isolated and deterministic. We use a noopWorkspace so the judge
	// never reads the parent tree.
	sess := session.New(
		session.SessionID(fmt.Sprintf("%s-%d", j.idPrefix, childSerial.Add(1))),
		j.childMode,
		judgeEnvironment.Ref(),
		j.limits,
		j.engine.now(),
	)

	run := j.engine.Run(ctx, sess, judgeEnvironment, RunRequest{Text: buildJudgePrompt(candidates, criteria)})
	// The judge child is tool-less and non-interactive; the zero childPosture (headless
	// auto-deny) is correct — it can never raise a Shell ask.
	final, stop := drainChild(run, childPosture{role: "judge"})
	if stop == session.StopError || stop == session.StopCancelled {
		return 0, "judge run did not complete; selected the first successful branch", nil
	}

	verdict, ok := parseJudgeVerdict(final)
	if !ok {
		return 0, "judge output unparseable; selected the first successful branch", nil
	}
	// Winner is 1-based in the prompt; translate to a 0-based candidates position.
	pos := verdict.Winner - 1
	if pos < 0 || pos >= len(candidates) {
		return 0, "judge picked an out-of-range branch; selected the first successful branch", nil
	}
	rationale := strings.TrimSpace(verdict.Rationale)
	if rationale == "" {
		rationale = "selected by judge"
	}
	return pos, rationale, nil
}

// buildJudgePrompt assembles the judge's prompt from the candidate summaries and
// criteria. Candidates are numbered 1..N (matching the verdict's 1-based winner).
// It sees ONLY the labels + summaries — never any branch transcript.
func buildJudgePrompt(candidates []BranchSummary, criteria string) string {
	rubric := strings.TrimSpace(criteria)
	if rubric == "" {
		rubric = defaultJudgeRubric
	}
	var b strings.Builder
	fmt.Fprintf(&b, "You are selecting the best result among %d candidate branches.\n", len(candidates))
	fmt.Fprintf(&b, "Criteria: %s.\n\n", rubric)
	for i, c := range candidates {
		fmt.Fprintf(&b, "Branch %d (%s): %s\n", i+1, c.Label, c.Summary)
	}
	b.WriteString("\nYou see ONLY each branch's final summary — not its actual changes or transcript — " +
		"so judge on the summaries alone.\n")
	b.WriteString("Evaluate each candidate against the criteria for correctness (is it right?), " +
		"completeness (does it cover the whole task?), and clarity (is the result usable as-is?).\n")
	b.WriteString("Respond with ONLY a single line of JSON and nothing else — no prose, no code fences: ")
	b.WriteString(`{"winner": <1-based branch number>, "rationale": "<one or two sentences>"}.`)
	return b.String()
}

// parseJudgeVerdict tolerantly extracts a {"winner":N,"rationale":"..."} object
// from possibly chatty judge text: it slices the first balanced top-level {...}
// object and unmarshals it. Returns ok=false on no object / bad JSON.
func parseJudgeVerdict(text string) (judgeVerdict, bool) {
	obj, ok := firstJSONObject(text)
	if !ok {
		return judgeVerdict{}, false
	}
	var v judgeVerdict
	if err := json.Unmarshal([]byte(obj), &v); err != nil {
		return judgeVerdict{}, false
	}
	return v, true
}

// firstJSONObject returns the first balanced, top-level {...} substring of s
// (ignoring braces inside strings), defending against a judge that wraps its JSON
// in prose or code fences. Returns ok=false when no balanced object is present.
func firstJSONObject(s string) (string, bool) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", false
	}
	depth := 0
	inStr := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}

// judgeWorkspace is an EMPTY, read-only workspace stub for the judge run. The judge
// scores text and calls NO tools (its Engine is wired with an empty/read-only
// catalog), so the stub exposes no files: every path is reported as not-existing
// (fs.ErrNotExist) rather than erroring, so the engine's instruction discovery
// (AGENTS.md/CLAUDE.md) cleanly finds nothing instead of hard-failing the run, and
// the judge never sees the parent tree. It satisfies Engine.Run's signature only.
type judgeWorkspace struct{}

func (judgeWorkspace) Root() string { return "/" }
func (judgeWorkspace) Read(context.Context, string) ([]byte, error) {
	return nil, fs.ErrNotExist
}
func (judgeWorkspace) ReadVersion(context.Context, string) ([]byte, tool.FileVersion, error) {
	return nil, tool.FileVersion{}, fs.ErrNotExist
}
func (judgeWorkspace) CreateFile(context.Context, string, []byte) (tool.FileVersion, error) {
	return tool.FileVersion{}, fs.ErrPermission
}
func (judgeWorkspace) ReplaceFile(context.Context, string, tool.FileVersion, []byte) (tool.FileVersion, error) {
	return tool.FileVersion{}, fs.ErrPermission
}
func (judgeWorkspace) Stat(context.Context, string) (tool.FileInfo, error) {
	return tool.FileInfo{}, fs.ErrNotExist
}
func (judgeWorkspace) Glob(context.Context, string) ([]string, error) { return nil, nil }
func (judgeWorkspace) Grep(context.Context, string, string) ([]tool.GrepMatch, error) {
	return nil, nil
}
func (judgeWorkspace) RecordRead(string, tool.FileVersion) {}
func (judgeWorkspace) RecordedVersion(string) (tool.FileVersion, bool) {
	return tool.FileVersion{}, false
}

// Compile-time assertions.
var (
	_ BranchJudge    = (*engineJudge)(nil)
	_ tool.Workspace = judgeWorkspace{}
)

// judgeEnvironment is the EMPTY, shell-less Environment the judge / ask-reviewer
// / guardrail-checker / model-router child engines run against (issue #462): it
// wraps judgeWorkspace with no CommandRunner so a tool-less, read-only child
// scores text without touching the parent tree. Built once via MustEnvironment
// (a process-wide var is safe — the Environment is immutable and carries no
// per-run state).
var judgeEnvironment = tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "judge", Revision: "in-tree-v1"}, judgeWorkspace{}, emptyReadLedger{}, nil)

// emptyReadLedger is for shell-less, tool-less helper engines. Their catalog cannot
// create file-read evidence, so retaining it would have no observable effect.
type emptyReadLedger struct{}

func (emptyReadLedger) RecordRead(context.Context, string, tool.FileVersion) error { return nil }

func (emptyReadLedger) RecordedVersion(context.Context, string) (tool.FileVersion, bool, error) {
	return tool.FileVersion{}, false, nil
}
