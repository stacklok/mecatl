package skillfs_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	onBody func()
}

func (atomicSource) ListSkills(context.Context) ([]tool.SkillMeta, error) { return nil, nil }
func (s atomicSource) SkillBody(_ context.Context, name string) (string, error) {
	body, ok := s.bodies[name]
	if !ok {
		return "", tool.ErrSkillNotFound
	}
	if s.onBody != nil {
		s.onBody()
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
	sum := sha256.Sum256([]byte("ownerless"))
	return learning.SkillVersion{State: learning.SkillActive, Partition: learning.SkillPartition{Principal: hex.EncodeToString(sum[:])}, Bundle: learning.SkillBundle{Name: name, Description: name + " description", Body: body}}
}

func TestADR_0295_CatalogGenerationRejectsStaleUpdates(t *testing.T) {
	partition := activeVersion("new", "").Partition
	catalog := skillfs.NewAtomicCatalog(nil, nil, nil)
	newer := activeVersion("new", "new body")
	older := activeVersion("old", "old body")

	if !catalog.RefreshPartitionsAtGeneration(map[learning.SkillPartition]learning.SkillGeneration{partition: 2}, []learning.SkillVersion{newer}) {
		t.Fatal("new authoritative generation was not published")
	}
	if catalog.RefreshPartitionsAtGeneration(map[learning.SkillPartition]learning.SkillGeneration{partition: 1}, []learning.SkillVersion{older}) {
		t.Fatal("stale publication was accepted")
	}
	if catalog.ClearPartitionsAtGeneration(map[learning.SkillPartition]learning.SkillGeneration{partition: 1}) {
		t.Fatal("stale invalidation was accepted")
	}
	view := catalog.View(partition)
	if view.Generation != 2 || len(view.Metas) != 1 || view.Metas[0].Name != "new" {
		t.Fatalf("stale operation replaced newer snapshot: generation=%d metas=%+v", view.Generation, view.Metas)
	}
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
	live := skillfs.NewLiveToolForPartitions(catalog, activeVersion("same", "").Partition)
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

func TestLiveToolExecutionBindsOneCatalogGeneration(t *testing.T) {
	old := atomicSource{
		bodies: map[string]string{"swap": "old body"},
		assets: map[string][]tool.SkillAsset{"swap": {{Name: "old.txt", Size: 3}}},
		data:   map[string][]byte{"swap\x00old.txt": []byte("old")},
	}
	newSource := atomicSource{
		bodies: map[string]string{"swap": "new body"},
		assets: map[string][]tool.SkillAsset{"swap": {{Name: "new.txt", Size: 3}}},
		data:   map[string][]byte{"swap\x00new.txt": []byte("new")},
	}
	oldMeta := []tool.SkillMeta{{Name: "swap", Description: "old", HasAssets: true}}
	newMeta := []tool.SkillMeta{{Name: "swap", Description: "new", HasAssets: true}}
	catalog := skillfs.NewAtomicCatalog(oldMeta, old, nil)
	old.onBody = func() { catalog.Refresh(newMeta, newSource, nil) }
	catalog.Refresh(oldMeta, old, nil)

	result, _ := skillfs.NewLiveTool(catalog).Execute(context.Background(), session.NewToolCall("c", "Skill", []byte(`{"name":"swap"}`)), tool.Environment{})
	if !strings.Contains(result.Content, "old body") || !strings.Contains(result.Content, "old.txt") || strings.Contains(result.Content, "new.txt") {
		t.Fatalf("execution mixed generations: %s", result.Content)
	}
}

func TestLiveToolLearnedSkillIsCallerPartitioned(t *testing.T) {
	alice := &session.Principal{Issuer: "issuer", Subject: "alice"}
	partitionValue := alice.Issuer + "\x00" + alice.Subject
	sum := sha256.Sum256([]byte(partitionValue))
	version := activeVersion("private", "alice body")
	version.Partition.Principal = hex.EncodeToString(sum[:])
	catalog := skillfs.NewAtomicCatalog(nil, nil, []learning.SkillVersion{version})
	live := skillfs.NewLiveToolForPartitions(catalog, version.Partition)
	call := session.NewToolCall("c", "Skill", []byte(`{"name":"private"}`))

	allowed, _ := live.Execute(session.WithPrincipal(context.Background(), alice), call, tool.Environment{})
	if allowed.IsError || !strings.Contains(allowed.Content, "alice body") {
		t.Fatalf("owner could not execute learned skill: %#v", allowed)
	}
	bob := &session.Principal{Issuer: "issuer", Subject: "bob"}
	foreign := skillfs.NewLiveTool(catalog)
	denied, _ := foreign.Execute(session.WithPrincipal(context.Background(), bob), call, tool.Environment{})
	if !denied.IsError || strings.Contains(denied.Content, "alice body") {
		t.Fatalf("foreign caller executed learned skill: %#v", denied)
	}
	ownerless, _ := foreign.Execute(context.Background(), call, tool.Environment{})
	if !ownerless.IsError {
		t.Fatalf("ownerless caller executed learned skill: %#v", ownerless)
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
			metas := catalog.View(activeVersion("old-a", "").Partition).Metas
			got := metas[0].Name + "," + metas[1].Name
			if got != "old-a,old-b" && got != "new-a,new-b" {
				t.Errorf("mixed generation: %s", got)
				return
			}
		}
	}()
	wg.Wait()
}
