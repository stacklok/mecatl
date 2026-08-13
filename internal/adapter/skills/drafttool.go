package skills

import (
	"context"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/toolkit"
)

// DraftToolName is the catalog name of the writable skill-authoring tool.
const DraftToolName = "SkillDraft"

// draftDescription is the model-facing documentation for the SkillDraft tool. It
// carries the trigger criteria, the over-eager anti-pattern (mirroring
// memory.rememberDescription's "When NOT to use"), and the load-bearing fact that
// a drafted skill is NOT active this session — it enters a review queue the
// operator must approve.
const draftDescription = `Draft a candidate skill (a reusable, progressive-disclosure "how-to" playbook) from a procedure you just performed, so a FUTURE session can reuse it after an operator approves it.

When to use (be conservative):
- ONLY after you have just completed a multi-step, NON-OBVIOUS procedure that a
  future session would plausibly repeat: 5+ tool calls, or a workflow the user
  explicitly corrected you into. Capture the procedure as numbered steps.

When NOT to use (the over-eager anti-pattern — these are REJECTED):
- Do NOT draft generic language/framework knowledge you already know.
- Do NOT draft one-off steps, or anything the filesystem already encodes (a
  command in the Makefile, a documented config). Let the code be the memory of
  the code.
- Procedural "how-to" ONLY. For a durable FACT or preference, use Remember instead.

IMPORTANT — a drafted skill is NOT active this session:
- It is written to a QUARANTINE review queue, never to the live skill catalog.
- An operator must review and promote it before any session can activate it.
- So drafting is cheap to get wrong (it only costs quarantine disk), but do not
  spam it — an over-eager draft is rejected at review.

How to write the SKILL.md fields:
- description: ONE line — what it does and WHEN to use it (the applicability
  condition). Keep it short; it becomes always-in-context metadata if promoted.
- body: numbered procedure steps, then an explicit "Done when:" line. For large
  reference material, do NOT inline it — add a line like
  "For detail, Read: <skill-dir>/REFERENCE.md". Use {braces} for variable
  placeholders. No secrets, no absolute machine paths, no instructions that try
  to override the agent.

Arguments:
- name        (required): short lowercase activation name, e.g. "deploy-to-staging".
- description (required): the one-line applicability summary.
- body        (required): the markdown procedure.`

// DraftTool is the writable skill-authoring tool. It is MUTATING
// (ReadOnly() == false): it writes a candidate SKILL.md to the quarantine via the
// injected Drafter. Because it is mutating, the dispatcher runs it alone/serially
// and the catalog filters it out of plan mode. The Drafter is constructor-
// injected, the write-side mirror of how the read-only Skill tool takes a Source.
type DraftTool struct {
	drafter Drafter
}

// Compile-time assertion that DraftTool implements tool.Tool.
var _ tool.Tool = DraftTool{}

// NewDraftTool constructs the SkillDraft tool bound to d. d must be non-nil; the
// composition root registers this tool only when a quarantine dir is configured.
func NewDraftTool(d Drafter) tool.Tool {
	if d == nil {
		panic("skills: NewDraftTool requires a non-nil Drafter")
	}
	return DraftTool{drafter: d}
}

// draftArgs is the JSON argument shape for the SkillDraft tool.
type draftArgs struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

// Spec returns the model-facing specification.
func (DraftTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name:        DraftToolName,
		Description: draftDescription,
		Schema: toolkit.Schema(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "Short lowercase activation name (letters, digits, '-', '_'), e.g. \"deploy-to-staging\"."},
    "description": {"type": "string", "description": "One-line summary of what the skill does and WHEN to use it."},
    "body": {"type": "string", "description": "Markdown: numbered procedure steps plus an explicit \"Done when:\" line."}
  },
  "required": ["name", "description", "body"]
}`),
	}
}

// ReadOnly reports that SkillDraft mutates persistent state (it writes a file).
func (DraftTool) ReadOnly() bool { return false }

// Execute validates and quarantines the candidate skill. A validation/
// sanitization failure is returned as a model-addressable error result (never a
// harness-level Go error), so the model can revise and retry. On success the
// result names the quarantine path and any near-duplicate warnings.
func (t DraftTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	var args draftArgs
	if msg, ok := toolkit.ParseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}

	// draftArgs (the JSON wire shape) and DraftRequest (the seam input) are kept
	// field-identical so this conversion stays valid; add a field to one and you
	// MUST add it to the other in the same position.
	res, err := t.drafter.Draft(ctx, DraftRequest(args))
	if err != nil {
		// A validation/sanitization/write failure: model-addressable, not a fault.
		return session.NewToolError(in.ID, err.Error()), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Drafted skill %q to the review queue at %s.\n", strings.TrimSpace(args.Name), res.Path)
	b.WriteString("It is NOT active this session — an operator must promote it before any session can activate it.")
	for _, w := range res.Warnings {
		fmt.Fprintf(&b, "\nNote: %s", w)
	}
	return session.NewToolResult(in.ID, b.String()), nil
}
