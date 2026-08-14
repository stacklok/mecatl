package skillfs_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/skillfs"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type atomicActivator map[string]string

func (a atomicActivator) Activate(_ context.Context, name string) (skillfs.Activation, error) {
	return skillfs.Activation{Body: a[name]}, nil
}
func activeVersion(name, body string) learning.SkillVersion {
	return learning.SkillVersion{State: learning.SkillActive, Bundle: learning.SkillBundle{Name: name, Description: name + " description", Body: body}}
}

func TestAtomicCatalogExternalPrecedenceAndRefresh(t *testing.T) {
	external := []tool.SkillMeta{{Name: "same", Description: "operator"}}
	catalog := skillfs.NewAtomicCatalog(external, atomicActivator{"same": "operator body"}, nil)
	if conflicts := catalog.Refresh(external, atomicActivator{"same": "operator body"}, []learning.SkillVersion{activeVersion("same", "agent body")}); len(conflicts) != 1 {
		t.Fatalf("conflicts=%d", len(conflicts))
	}
	live := skillfs.NewLiveTool(catalog)
	result, _ := live.Execute(context.Background(), session.NewToolCall("c", "Skill", []byte(`{"name":"same"}`)), tool.Environment{})
	if !strings.Contains(result.Content, "operator body") || strings.Contains(result.Content, "agent body") {
		t.Fatalf("collision result: %s", result.Content)
	}
	catalog.Refresh(external, atomicActivator{"same": "operator body"}, []learning.SkillVersion{activeVersion("new-skill", "new body")})
	if !strings.Contains(live.Spec().Description, "new-skill") {
		t.Fatal("spec did not refresh")
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
