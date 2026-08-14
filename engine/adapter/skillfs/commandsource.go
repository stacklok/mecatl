package skillfs

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/tool"
)

// SkillCommandSource exposes admitted skills as slash commands through the same
// logical, path-free SkillSource consumed by the Skill tool.
type SkillCommandSource struct {
	metas []tool.SkillMeta
	names map[string]struct{}
	src   tool.SkillSource
}

// NewSkillCommandSource builds a prompt.CommandSource over a SkillSource.
func NewSkillCommandSource(metas []tool.SkillMeta, source tool.SkillSource) *SkillCommandSource {
	names := make(map[string]struct{}, len(metas))
	for _, m := range metas {
		if prompt.ValidCommandName(m.Name) {
			names[m.Name] = struct{}{}
		}
	}
	return &SkillCommandSource{metas: metas, names: names, src: source}
}

// ListCommands returns the admitted skill names as sorted slash commands.
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

// CommandBody returns one admitted skill's instruction body.
func (s *SkillCommandSource) CommandBody(ctx context.Context, name string) (string, bool, error) {
	body, _, found, err := s.load(ctx, name)
	return body, found, err
}

// CommandBodyWithPost keeps the body separate from the logical inventory so
// command argument substitution cannot rewrite asset names.
func (s *SkillCommandSource) CommandBodyWithPost(ctx context.Context, name string) (body, post string, found bool, err error) {
	return s.load(ctx, name)
}

func (s *SkillCommandSource) load(ctx context.Context, name string) (body, post string, found bool, err error) {
	if name == "" || s.src == nil || !prompt.ValidCommandName(name) {
		return "", "", false, nil
	}
	if _, ok := s.names[name]; !ok {
		return "", "", false, nil
	}
	body, err = s.src.SkillBody(ctx, name)
	if err != nil {
		if errors.Is(err, tool.ErrSkillNotFound) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", "", false, nil
	}
	assets, err := s.src.ListSkillAssets(ctx, name)
	if err != nil {
		if errors.Is(err, tool.ErrSkillNotFound) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	if err := validateAssetInventory(assets); err != nil {
		return "", "", false, err
	}
	return body, renderBundledAssetInventory(assets), true, nil
}

var (
	_ prompt.CommandSource              = (*SkillCommandSource)(nil)
	_ prompt.CommandPostExpansionSource = (*SkillCommandSource)(nil)
)
