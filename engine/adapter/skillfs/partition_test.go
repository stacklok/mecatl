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

func TestAtomicCatalogPartitionsDoNotEvictOrExposeEachOther(t *testing.T) {
	alice := learning.SkillPartition{Principal: "alice"}
	bob := learning.SkillPartition{Principal: "bob"}
	project := learning.SkillPartition{Principal: "alice", Project: "/project"}
	version := func(part learning.SkillPartition, name string) learning.SkillVersion {
		return learning.SkillVersion{Partition: part, State: learning.SkillActive, Bundle: learning.SkillBundle{Name: name, Description: name, Body: name + " body"}}
	}
	catalog := skillfs.NewAtomicCatalog([]tool.SkillMeta{{Name: "external", Description: "deployment"}}, atomicSource{bodies: map[string]string{"external": "external body"}}, nil)
	var wg sync.WaitGroup
	for _, item := range []struct {
		part learning.SkillPartition
		name string
	}{{alice, "alice"}, {bob, "bob"}, {project, "project"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			catalog.RefreshPartitions([]learning.SkillPartition{item.part}, []learning.SkillVersion{version(item.part, item.name)})
		}()
	}
	wg.Wait()
	for _, tc := range []struct {
		parts           []learning.SkillPartition
		present, absent string
	}{
		{[]learning.SkillPartition{alice, project}, "project", "bob"},
		{[]learning.SkillPartition{bob}, "bob", "alice"},
	} {
		live := skillfs.NewLiveToolForPartitions(catalog, tc.parts...)
		if !strings.Contains(live.Spec().Description, tc.present) || strings.Contains(live.Spec().Description, tc.absent) || !strings.Contains(live.Spec().Description, "external") {
			t.Fatalf("partitioned spec = %q", live.Spec().Description)
		}
		result, _ := live.Execute(context.Background(), session.NewToolCall("x", "Skill", []byte(`{"name":"`+tc.absent+`"}`)), tool.Environment{})
		if !result.IsError {
			t.Fatalf("foreign skill executed: %#v", result)
		}
	}
	catalog.ClearPartitions(alice)
	if strings.Contains(skillfs.NewLiveToolForPartitions(catalog, alice).Spec().Description, "alice") || !strings.Contains(skillfs.NewLiveToolForPartitions(catalog, bob).Spec().Description, "bob") {
		t.Fatal("failed refresh quarantine crossed partitions")
	}
}
