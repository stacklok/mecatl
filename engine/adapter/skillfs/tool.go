package skillfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// ToolName is the catalog name of the single skills tool.
const ToolName = "Skill"

const (
	maxBundledAssetInventoryBytes = 8_000
	maxSkillAssetBytes            = MaxOutputBytes
	maxSourceErrorBytes           = 1_000
)

const descriptionPreamble = `Activate a skill or read one of its bundled textual assets through its logical name.

Skills are curated, reusable playbooks. Only each skill's name and one-line description are shown below. Call with {name} to load its instructions and logical asset inventory. If those instructions need an asset, call again with {name, asset}. Assets are disclosed one at a time; this tool is not a general file browser.

Arguments:
- name (required): the exact name of one of the skills listed below.
- asset (optional): one logical asset name from that skill's activation result.

Available skills:`

// Tool is the single model-facing skills tool. It consumes the logical,
// path-free SkillSource port directly and remains read-only.
type Tool struct {
	byName      map[string]tool.SkillMeta
	src         tool.SkillSource
	description string
}

var _ tool.Tool = Tool{}

// NewTool builds the Skill tool over metadata and its logical source.
func NewTool(metas []tool.SkillMeta, source tool.SkillSource) Tool {
	byName := make(map[string]tool.SkillMeta, len(metas))
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
	return Tool{byName: byName, src: source, description: b.String()}
}

type skillArgs struct {
	Name  string `json:"name"`
	Asset string `json:"asset,omitempty"`
}

// Spec returns the model-facing Skill specification and progressive-disclosure inventory.
func (t Tool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name:        ToolName,
		Description: t.description,
		Schema: Schema(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "Exact name of the skill to activate, as listed in this tool's description."},
    "asset": {"type": "string", "description": "Optional logical asset name exactly as listed by a prior activation."}
  },
  "required": ["name"]
}`),
	}
}

// ReadOnly reports that Skill reads logical source data without mutation.
func (Tool) ReadOnly() bool { return true }

// Execute activates a skill or returns one validated, bounded textual asset.
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
		return session.NewToolError(in.ID, fmt.Sprintf("unknown skill %q; %s", name, t.availableHint())), nil
	}
	if t.src == nil {
		return session.NewToolError(in.ID, fmt.Sprintf("skill %q is unavailable (no source wired); %s", name, t.availableHint())), nil
	}
	if args.Asset != "" {
		return t.readAsset(ctx, in.ID, sk.Name, args.Asset), nil
	}
	return t.activate(ctx, in.ID, sk), nil
}

func (t Tool) activate(ctx context.Context, callID session.ToolCallID, sk tool.SkillMeta) session.ToolResult {
	body, err := t.src.SkillBody(ctx, sk.Name)
	if err != nil {
		return session.NewToolError(callID, sourceError(fmt.Sprintf("activating skill %q failed", sk.Name), err))
	}
	assets, err := t.src.ListSkillAssets(ctx, sk.Name)
	if err != nil && !errors.Is(err, tool.ErrSkillNotFound) {
		return session.NewToolError(callID, sourceError(fmt.Sprintf("listing assets of skill %q failed", sk.Name), err))
	}
	if err := validateAssetInventory(assets); err != nil {
		return session.NewToolError(callID, sourceError(fmt.Sprintf("listing assets of skill %q failed", sk.Name), err))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Skill: %s\n", sk.Name)
	if sk.Compatibility != "" {
		fmt.Fprintf(&b, "Compatibility: %s\n", sk.Compatibility)
	}
	if len(sk.AllowedTools) > 0 {
		fmt.Fprintf(&b, "This skill declares allowed-tools: %s. These are the tools the skill expects to use; each call still follows normal permission rules.\n", strings.Join(sk.AllowedTools, ", "))
	}
	b.WriteString(renderBundledAssetInventory(assets))
	b.WriteString("\n")
	b.WriteString(body)
	// Truncate's carried contract appends TruncationMarker beyond its byte
	// argument. Reserve that suffix here so the complete activation result,
	// including the marker, stays inside the model-facing output envelope.
	return session.NewToolResult(callID, Truncate(b.String(), MaxOutputBytes-len(TruncationMarker)))
}

func (t Tool) readAsset(ctx context.Context, callID session.ToolCallID, skill, asset string) session.ToolResult {
	if !tool.ValidSkillAssetName(asset) {
		return session.NewToolError(callID, fmt.Sprintf("invalid logical asset name %q", asset))
	}
	assets, err := t.src.ListSkillAssets(ctx, skill)
	if err != nil {
		return session.NewToolError(callID, sourceError(fmt.Sprintf("listing assets of skill %q failed", skill), err))
	}
	var listed *tool.SkillAsset
	for i := range assets {
		if assets[i].Name == asset {
			listed = &assets[i]
			break
		}
	}
	if listed == nil {
		return session.NewToolError(callID, fmt.Sprintf("unknown asset %q for skill %q; activate the skill to see its logical asset names", asset, skill))
	}
	if listed.Size < 0 {
		return session.NewToolError(callID, fmt.Sprintf("asset %q for skill %q has an invalid advertised size", asset, skill))
	}
	header := fmt.Sprintf("Skill asset: %s / %s\n\n", skill, asset)
	maxPayloadBytes := MaxOutputBytes - len(header)
	if maxPayloadBytes < 0 {
		return session.NewToolError(callID, fmt.Sprintf("asset %q for skill %q cannot fit in the tool output limit", asset, skill))
	}
	if listed.Size > int64(maxPayloadBytes) {
		return session.NewToolError(callID, fmt.Sprintf("asset %q for skill %q is too large (%d bytes; payload limit %d bytes after the %d-byte result header)", asset, skill, listed.Size, maxPayloadBytes, len(header)))
	}
	data, err := t.src.ReadSkillAsset(ctx, skill, asset)
	if err != nil {
		return session.NewToolError(callID, sourceError(fmt.Sprintf("reading asset %q for skill %q failed", asset, skill), err))
	}
	if len(data) > maxPayloadBytes {
		return session.NewToolError(callID, fmt.Sprintf("asset %q for skill %q is too large (payload limit %d bytes after the %d-byte result header); no content returned", asset, skill, maxPayloadBytes, len(header)))
	}
	if !utf8.Valid(data) {
		return session.NewToolError(callID, fmt.Sprintf("asset %q for skill %q is not valid UTF-8", asset, skill))
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return session.NewToolError(callID, fmt.Sprintf("asset %q for skill %q contains NUL bytes and is not textual", asset, skill))
	}
	return session.NewToolResult(callID, header+string(data))
}

func validateAssetInventory(assets []tool.SkillAsset) error {
	for _, asset := range assets {
		if !tool.ValidSkillAssetName(asset.Name) {
			return fmt.Errorf("source returned invalid logical asset name %q", asset.Name)
		}
	}
	return nil
}

func sourceError(prefix string, err error) string {
	return Truncate(prefix+": "+err.Error(), maxSourceErrorBytes)
}

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

// RegisterSource discovers a filesystem source and registers Skill when non-empty.
func RegisterSource(ctx context.Context, cat *tool.Catalog, src Source) ([]Skill, []SkipError, error) {
	fsSrc, skips, err := NewFSSource(ctx, src)
	if err != nil {
		return nil, skips, err
	}
	discovered := fsSrc.Discovered()
	if len(discovered) == 0 {
		return nil, skips, nil
	}
	metas, _ := fsSrc.ListSkills(ctx)
	if err := cat.Register(NewTool(metas, fsSrc)); err != nil {
		return discovered, skips, err
	}
	return discovered, skips, nil
}

// Register discovers and registers skills under dir.
func Register(cat *tool.Catalog, dir string) ([]Skill, []SkipError, error) {
	return RegisterSource(context.Background(), cat, DirSource{Dir: dir})
}

func renderBundledAssetInventory(assets []tool.SkillAsset) string {
	if len(assets) == 0 {
		return ""
	}
	var inventory strings.Builder
	inventory.WriteString("Bundled assets (logical names; request one with this Skill tool's asset argument):\n")
	for i, a := range assets {
		line := fmt.Sprintf("  - %s (%d bytes)\n", a.Name, a.Size)
		omitted := fmt.Sprintf("  ... %d bundled assets omitted.\n", len(assets)-i)
		if inventory.Len()+len(line)+len(omitted) > maxBundledAssetInventoryBytes {
			inventory.WriteString(omitted)
			break
		}
		inventory.WriteString(line)
	}
	return inventory.String()
}
