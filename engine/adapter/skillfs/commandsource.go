package skillfs

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/tool"
)

// SkillCommandSource adapts the resolved skill seam (the always-in-context
// SkillMeta inventory + the Activator that loads a body on activation) to the
// prompt.CommandSource port, so each discovered skill is invocable as
// `/<skill-name>` — a Claude-Code-style skill-as-slash-command. A skill body IS
// the command template: the model receives the skill's instructions directly in
// context (no tool dispatch, no new concept — this reuses the existing
// CommandSource/SourceExpander seam the loop already consumes).
//
// The Activator is the SAME seam the Skill tool loads through, so the driver
// path's caching/materialization is SHARED — a `/skill` expansion and a Skill
// tool activation read through one activator. The skill body returned by
// Activate is ALREADY frontmatter-stripped (ParseSkill trims it), so
// SourceExpander's stripFrontmatter is a no-op on it — safe.
//
// TRUST GATE: this source is constructed over the resolved seam, which an
// untrusted workspace's project tier never enters (ResolveSources drops
// project-tier skills before this source is built). So a SkillCommandSource
// never leaks untrusted project skills — the gate is INHERITED by
// construction, the same way the Skill tool's inventory is.
type SkillCommandSource struct {
	metas []tool.SkillMeta
	act   Activator
}

// NewSkillCommandSource builds a prompt.CommandSource over the skill seam.
// metas is the always-in-context skill inventory (name + description); act
// loads a named skill's body on CommandBody. A nil/empty metas yields a source
// that lists and expands nothing (the no-skills path stays byte-identical).
func NewSkillCommandSource(metas []tool.SkillMeta, act Activator) *SkillCommandSource {
	return &SkillCommandSource{metas: metas, act: act}
}

// ListCommands returns one prompt.Command per skill, defensively filtered by
// prompt.ValidCommandName (a skill name that cannot be invoked as `/<name>` is
// dropped — belt-and-braces; the skill-name grammar is a subset of the command
// grammar, so this is a no-op on well-formed skills). De-duplicated by name and
// name-sorted. A non-nil error is reserved for a genuine backend fault; an
// empty inventory is normal (returns an empty slice, never nil-error).
func (s *SkillCommandSource) ListCommands(_ context.Context) ([]prompt.Command, error) {
	if len(s.metas) == 0 {
		return nil, nil
	}
	out := make([]prompt.Command, 0, len(s.metas))
	seen := make(map[string]struct{}, len(s.metas))
	for _, m := range s.metas {
		if !prompt.ValidCommandName(m.Name) {
			continue
		}
		if _, dup := seen[m.Name]; dup {
			continue
		}
		seen[m.Name] = struct{}{}
		out = append(out, prompt.Command{Name: m.Name, Description: m.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CommandBody returns the named skill's body. An unknown name (or a name not
// invocable as a command) is the NORMAL found=false outcome (the input passes
// through the expander unchanged), never an error. An activation fault (e.g. a
// driver materialization failure) is returned as an error, mirroring the Skill
// tool's addressable activation-error posture.
func (s *SkillCommandSource) CommandBody(ctx context.Context, name string) (string, bool, error) {
	if name == "" || s.act == nil {
		return "", false, nil
	}
	// Defensively skip a name the command grammar could not have parsed, so an
	// unknown non-command name short-circuits to found=false without an activator
	// round-trip (a not-found Activate is the normal outcome anyway, but this
	// keeps the contract honest for a caller that hand-builds a name).
	if !prompt.ValidCommandName(name) {
		return "", false, nil
	}
	act, err := s.act.Activate(ctx, name)
	if err != nil {
		if errors.Is(err, tool.ErrSkillNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	body := strings.TrimSpace(act.Body)
	if body == "" {
		// An empty body is not an expansion: leave the input unchanged so the
		// model sees its raw `/name` rather than a blank substitution.
		return "", false, nil
	}
	return body, true, nil
}

// Compile-time assertion that *SkillCommandSource satisfies prompt.CommandSource.
var _ prompt.CommandSource = (*SkillCommandSource)(nil)
