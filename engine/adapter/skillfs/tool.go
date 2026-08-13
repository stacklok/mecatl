package skillfs

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// ToolName is the catalog name of the single skills tool.
const ToolName = "Skill"

// maxBundledAssetInventoryBytes bounds the activation result space devoted to
// enumerating bundled asset names. The remainder remains available for the
// skill's instructions, which are the activation's primary payload.
const maxBundledAssetInventoryBytes = 8_000

// descriptionPreamble is the static head of the Skill tool's description. The
// per-skill metadata (name + one-line description) is appended to it at
// construction time, so the always-in-context inventory of skills lives in the
// ToolSpec.Description — cheap and cache-stable across turns.
const descriptionPreamble = `Activate a skill: load a named, progressive-disclosure instruction set into the conversation.

Skills are curated, reusable playbooks (workflows, conventions, domain procedures) authored as files. Only each skill's NAME and one-line description are shown below; the FULL instructions load only when you activate a skill here. This keeps your context small until a skill is actually needed.

When to use:
- When the task matches one of the skills listed below. Read the one-line
  descriptions, pick the best match, then activate it by name to get its full
  instructions, and follow them.
- You may activate more than one skill across a task, one call at a time.

When NOT to use:
- For reading project files (use Read) or searching code (use Grep/Glob). A skill
  is curated guidance, not a file browser.
- When no skill below fits the task — just proceed without one.

Activation output starts with the skill's base directory; any bundled files the
skill references (references/, scripts/, assets/) live under that directory.

Arguments:
- name (required): the exact name of one of the skills listed below.

Available skills:`

// Tool is the single model-facing skills tool. Its Spec().Description enumerates
// every discovered skill's metadata (the always-in-context layer); Execute
// loads a single skill's full body through the Activator seam (the
// load-on-activation layer). It is read-only — it returns instructions and
// mutates nothing — so the dispatcher may run it in parallel with other reads
// and it remains available in plan mode.
type Tool struct {
	// byName indexes skill metadata by activation name for O(1) Execute lookup.
	byName map[string]tool.SkillMeta
	// act loads a skill's body + base directory on activation. The metadata
	// layer above is path-free; WHERE the body/payloads come from is entirely
	// the activator's business (FS snapshot in place, or driver materialization).
	act Activator
	// description is the precomputed, cache-stable tool description: the static
	// preamble plus the enumerated skill metadata.
	description string
}

// Compile-time assertion that Tool implements tool.Tool.
var _ tool.Tool = Tool{}

// NewTool builds the Skill tool over the given skill metadata and activator.
// The metadata is indexed by name and rendered into the tool description once,
// at construction, so Spec() is allocation-free per call — byte-identical to
// the pre-seam rendering (preamble + "\n- <name>: <description>" per skill,
// name-sorted). Callers should not pass an empty slice — the composition root
// omits the tool entirely when no skills are discovered (see RegisterSource);
// NewTool with no skills yields a tool whose Execute always reports "no skills
// available".
func NewTool(metas []tool.SkillMeta, act Activator) Tool {
	byName := make(map[string]tool.SkillMeta, len(metas))
	// Copy + sort by name so the description ordering is deterministic regardless
	// of the input slice's order.
	sorted := append([]tool.SkillMeta(nil), metas...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var b strings.Builder
	b.WriteString(descriptionPreamble)
	if len(sorted) == 0 {
		b.WriteString("\n  (none configured)")
	}
	for _, s := range sorted {
		byName[s.Name] = s
		fmt.Fprintf(&b, "\n- %s: %s", s.Name, s.Description)
	}
	return Tool{byName: byName, act: act, description: b.String()}
}

// skillArgs is the JSON argument shape for the Skill tool.
type skillArgs struct {
	Name string `json:"name"`
}

// Spec returns the model-facing specification. The Description carries the
// always-in-context skill inventory (name + one-line description per skill); the
// schema is a single required "name" string.
func (t Tool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name:        ToolName,
		Description: t.description,
		Schema: Schema(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "Exact name of the skill to activate, as listed in this tool's description."}
  },
  "required": ["name"]
}`),
	}
}

// ReadOnly reports that activating a skill only returns instructions and mutates
// no state. This keeps the tool dispatchable in parallel with other reads and
// available in plan mode (read-only tools survive the plan-mode catalog filter).
func (Tool) ReadOnly() bool { return true }

// Execute looks up the named skill and returns its full body as the tool result,
// prefixed by a small header naming the skill and its BASE DIRECTORY — the
// runtime-discoverability axis: a skill's bundled files (references/, scripts/,
// assets/) live under that directory, which may be OUTSIDE the workspace
// (~/.claude/skills/…), and without the path in the result the model can only
// guess (and the workspace then refuses the guess). The composition root opens
// every production Workspace with exactly the activator-derived directories as
// read-only allowed roots (osfs.WithReadRoots) — the FS source's per-skill dirs
// or the driver asset cache — so the absolute path the header advertises is
// readable by construction. The body + base directory load through the
// Activator seam; the rendering is byte-identical to the pre-seam form for FS
// skills, and a BaseDir of "" (no payloads) omits the Base-directory block. An
// unknown (or empty) name is a model-addressable error result that lists the
// available skill names so the model can recover, NOT a harness-level error —
// and so is a failed activation (e.g. a driver bundle over the cap), naming
// the skill and the available alternatives.
func (t Tool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	var args skillArgs
	if msg, ok := ParseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return session.NewToolError(in.ID, "the \"name\" argument is required; "+t.availableHint()), nil
	}
	sk, ok := t.byName[name]
	if !ok {
		return session.NewToolError(in.ID,
			fmt.Sprintf("unknown skill %q; %s", name, t.availableHint())), nil
	}
	if t.act == nil {
		return session.NewToolError(in.ID,
			fmt.Sprintf("skill %q cannot be activated (no activator wired); %s", name, t.availableHint())), nil
	}
	act, err := t.act.Activate(ctx, name)
	if err != nil {
		return session.NewToolError(in.ID,
			fmt.Sprintf("activating skill %q failed: %v; %s", name, err, t.availableHint())), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Skill: %s\n", sk.Name)
	b.WriteString(renderBaseDirectory(act, true))
	// Compatibility is the optional ADVISORY `compatibility` frontmatter field
	// (issue #419). Surface it as an advisory note on activation so the model
	// learns the author's stated compatibility (e.g. "mecatl >= 0.1"); it is
	// never enforced as a gate — only shown. Omitted (empty) → no note, so the
	// pre-seam byte-identical rendering for skills without the field is
	// preserved.
	if sk.Compatibility != "" {
		fmt.Fprintf(&b, "Compatibility: %s\n", sk.Compatibility)
	}
	// AllowedTools is the optional ADVISORY `allowed-tools` frontmatter field
	// (agentskills.io, Experimental; issue #419): a list of tool names the skill
	// EXPECTS to use. Surface it as an advisory note on activation so the model
	// learns the author's intent — and EXPLICITLY state that calls still follow
	// normal permission rules, so the model does NOT infer pre-approval. It is
	// NEVER a permission grant: the permission evaluator never reads it, and a
	// call still resolves through the normal deny-dominant policy at every
	// posture (including yolo). Omitted (empty) → no note, so a skill without the
	// field renders byte-identically to before (preserving the activation
	// golden).
	if len(sk.AllowedTools) > 0 {
		fmt.Fprintf(&b, "This skill declares allowed-tools: %s. These are the tools the skill expects to use; each call still follows normal permission rules.\n",
			strings.Join(sk.AllowedTools, ", "))
	}
	b.WriteString(renderBundledAssetInventory(act.Assets))
	b.WriteString("\n")
	b.WriteString(act.Body)
	return session.NewToolResult(in.ID, Truncate(b.String(), MaxOutputBytes)), nil
}

// availableHint returns a short "available skills are: ..." sentence (or a clear
// "no skills" message) for error results, so the model always learns the valid
// names from a failed call.
func (t Tool) availableHint() string {
	if len(t.byName) == 0 {
		return "no skills are available"
	}
	names := make([]string, 0, len(t.byName))
	for n := range t.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return "available skills are: " + strings.Join(names, ", ")
}

// --- Registration helpers -------------------------------------------------

// RegisterSource discovers skills from src and, when at least one valid skill is
// found, registers a single Skill tool into cat. It returns the discovered
// skills, the aggregated skip diagnostics (malformed/duplicate/shadowed entries),
// and the first registration error (e.g. a name collision) or a discovery fault.
//
// src is the EXTENSIBILITY POINT: pass a single DirSource for one directory, or a
// MultiSource (built via ResolveSources + NewMultiSource) to aggregate the
// conventional locations and explicit paths with precedence. The consumer here is
// agnostic to where skills come from. It is re-expressed over the seam pieces
// (NewFSSource snapshot + NewSnapshotActivator), so a directly-registered tool
// renders byte-identically to the composition-assembled one.
//
// Skills are OPT-IN and the tool is registered ONLY when there is something to
// expose: if src yields zero valid skills, RegisterSource registers NOTHING and
// returns (nil, skips, nil) — there is no value in advertising a Skill tool with
// an empty inventory. The composition root logs the skip diagnostics and the
// enabled/disabled state.
func RegisterSource(ctx context.Context, cat *tool.Catalog, src Source) ([]Skill, []SkipError, error) {
	fsSrc, skips, err := NewFSSource(ctx, src)
	if err != nil {
		return nil, skips, err
	}
	discovered := fsSrc.Discovered()
	if len(discovered) == 0 {
		return nil, skips, nil
	}
	metas, _ := fsSrc.ListSkills(ctx) // snapshot read; never errors
	if err := cat.Register(NewTool(metas, NewSnapshotActivator(fsSrc))); err != nil {
		return discovered, skips, err
	}
	return discovered, skips, nil
}

// Register discovers skills under a single directory and registers the Skill
// tool, exactly like RegisterSource with a DirSource{Dir: dir}. It is preserved
// for backward compatibility; new wiring should use RegisterSource with a
// composed Source.
func Register(cat *tool.Catalog, dir string) ([]Skill, []SkipError, error) {
	return RegisterSource(context.Background(), cat, DirSource{Dir: dir})
}

// renderActivationAssets renders the activation-derived header shared by Skill
// slash commands. Command sources cannot know which tools their consuming
// catalog exposes, so they omit the tool-specific guidance.
func renderActivationAssets(act Activation, includeToolGuidance bool) string {
	return renderBaseDirectory(act, includeToolGuidance) + renderBundledAssetInventory(act.Assets)
}

func renderBaseDirectory(act Activation, includeToolGuidance bool) string {
	if act.BaseDir == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Base directory: %s\n", act.BaseDir)
	if includeToolGuidance {
		b.WriteString("Bundled files (references/, scripts/, assets/) live under the base directory; read them with the Read tool by absolute path, and run bundled scripts via Bash with their absolute path.\n")
	}
	return b.String()
}

// renderBundledAssetInventory caps the prompt-visible inventory while retaining
// the number of omitted assets. Both Skill activation and slash commands use it.
func renderBundledAssetInventory(assets []tool.SkillAsset) string {
	if len(assets) == 0 {
		return ""
	}
	var inventory strings.Builder
	inventory.WriteString("Bundled files:\n")
	for i, a := range assets {
		line := fmt.Sprintf("  - %s\n", a.Name)
		omitted := fmt.Sprintf("  ... %d bundled files omitted.\n", len(assets)-i)
		if inventory.Len()+len(line)+len(omitted) > maxBundledAssetInventoryBytes {
			inventory.WriteString(omitted)
			break
		}
		inventory.WriteString(line)
	}
	return inventory.String()
}
