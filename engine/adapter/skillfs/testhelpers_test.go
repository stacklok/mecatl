package skillfs

import (
	"context"
	"fmt"

	"github.com/stacklok/mecatl/engine/tool"
)

type stubSkillSource struct {
	bodies    map[string]string
	assets    map[string][]tool.SkillAsset
	data      map[string][]byte
	bodyErr   error
	assetsErr error
	readErr   error
	reads     int
}

func (*stubSkillSource) ListSkills(context.Context) ([]tool.SkillMeta, error) { return nil, nil }

func (s *stubSkillSource) SkillBody(_ context.Context, name string) (string, error) {
	if s.bodyErr != nil {
		return "", s.bodyErr
	}
	body, ok := s.bodies[name]
	if !ok {
		return "", fmt.Errorf("%w: %q", tool.ErrSkillNotFound, name)
	}
	return body, nil
}

func (s *stubSkillSource) ListSkillAssets(_ context.Context, name string) ([]tool.SkillAsset, error) {
	if s.assetsErr != nil {
		return nil, s.assetsErr
	}
	return append([]tool.SkillAsset(nil), s.assets[name]...), nil
}

func (s *stubSkillSource) ReadSkillAsset(_ context.Context, skill, asset string) ([]byte, error) {
	s.reads++
	if s.readErr != nil {
		return nil, s.readErr
	}
	data, ok := s.data[skill+"/"+asset]
	if !ok {
		return nil, fmt.Errorf("%w: %q/%q", tool.ErrSkillAssetNotFound, skill, asset)
	}
	return append([]byte(nil), data...), nil
}
