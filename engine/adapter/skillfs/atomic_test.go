package skillfs_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/skillfs"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type atomicSource struct {
	bodies map[string]string
	assets map[string][]tool.SkillAsset
	data   map[string][]byte
}

func (atomicSource) ListSkills(context.Context) ([]tool.SkillMeta, error) { return nil, nil }
func (s atomicSource) SkillBody(_ context.Context, name string) (string, error) {
	body, ok := s.bodies[name]
	if !ok {
		return "", tool.ErrSkillNotFound
	}
	return body, nil
}
func (s atomicSource) ListSkillAssets(_ context.Context, name string) ([]tool.SkillAsset, error) {
	if _, ok := s.bodies[name]; !ok {
		return nil, tool.ErrSkillNotFound
	}
	return append([]tool.SkillAsset(nil), s.assets[name]...), nil
}
func (s atomicSource) ReadSkillAsset(_ context.Context, skill, asset string) ([]byte, error) {
	data, ok := s.data[skill+"\x00"+asset]
	if !ok {
		return nil, tool.ErrSkillAssetNotFound
	}
	return append([]byte(nil), data...), nil
}

func activeVersion(name, body string) learning.SkillVersion {
	return learning.SkillVersion{State: learning.SkillActive, Bundle: learning.SkillBundle{Name: name, Description: name + " description", Body: body}}
}

func TestAtomicCatalogExternalPrecedenceAssetParityAndRefresh(t *testing.T) {
	external := []tool.SkillMeta{{Name: "same", Description: "operator", HasAssets: true}}
	source := atomicSource{
		bodies: map[string]string{"same": "operator body"},
		assets: map[string][]tool.SkillAsset{"same": {{Name: "references/check.md", Size: 5}}},
		data:   map[string][]byte{"same\x00references/check.md": []byte("check")},
	}
	catalog := skillfs.NewAtomicCatalog(external, source, nil)
	if conflicts := catalog.Refresh(external, source, []learning.SkillVersion{activeVersion("same", "agent body")}); len(conflicts) != 1 {
		t.Fatalf("conflicts=%d", len(conflicts))
	}
	live := skillfs.NewLiveTool(catalog)
	result, _ := live.Execute(context.Background(), session.NewToolCall("c", "Skill", []byte(`{"name":"same"}`)), tool.Environment{})
	if !strings.Contains(result.Content, "operator body") || strings.Contains(result.Content, "agent body") || !strings.Contains(result.Content, "references/check.md") {
		t.Fatalf("collision result: %s", result.Content)
	}
	asset, _ := live.Execute(context.Background(), session.NewToolCall("a", "Skill", []byte(`{"name":"same","asset":"references/check.md"}`)), tool.Environment{})
	if asset.IsError || !strings.Contains(asset.Content, "check") {
		t.Fatalf("asset result: %#v", asset)
	}

	catalog.Refresh(external, source, []learning.SkillVersion{activeVersion("new-skill", "new body")})
	if !strings.Contains(live.Spec().Description, "new-skill") || !strings.Contains(string(live.Spec().Schema), `"asset"`) {
		t.Fatal("live spec did not retain asset schema after refresh")
	}
	asset, _ = live.Execute(context.Background(), session.NewToolCall("l", "Skill", []byte(`{"name":"new-skill","asset":"x.txt"}`)), tool.Environment{})
	if !asset.IsError || !strings.Contains(asset.Content, "body-only") {
		t.Fatalf("learned asset request = %#v", asset)
	}
	if _, err := catalog.ReadSkillAsset(context.Background(), "new-skill", "x.txt"); !errors.Is(err, tool.ErrSkillAssetNotFound) {
		t.Fatalf("ReadSkillAsset error = %v", err)
	}
}

func TestAtomicCatalogConcurrentReadersSeeCompleteGeneration(t *testing.T) {
	catalog := skillfs.NewAtomicCatalog(nil, nil, []learning.SkillVersion{activeVersion("old-a", "a"), activeVersion("old-b", "b")})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			catalog.Refresh(nil, nil, []learning.SkillVersion{activeVersion("new-a", "a"), activeVersion("new-b", "b")})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			metas, _ := catalog.ListSkills(context.Background())
			got := metas[0].Name + "," + metas[1].Name
			if got != "old-a,old-b" && got != "new-a,new-b" {
				t.Errorf("mixed generation: %s", got)
				return
			}
		}
	}()
	wg.Wait()
}
