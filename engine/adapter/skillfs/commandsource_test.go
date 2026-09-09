package skillfs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestSkillCommandSourceListMirrorsSkills(t *testing.T) {
	src := NewSkillCommandSource([]tool.SkillMeta{
		{Name: "zebra", Description: "z skill"},
		{Name: "alpha", Description: "a skill"},
		{Name: "alpha", Description: "duplicate"},
		{Name: "Bad/Name", Description: "invalid"},
	}, &stubSkillSource{})
	cmds, err := src.ListCommands(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 2 || cmds[0].Name != "alpha" || cmds[1].Name != "zebra" {
		t.Fatalf("ListCommands = %+v", cmds)
	}
}

func TestSkillCommandSourceExpansionIsLogicalAndPathFree(t *testing.T) {
	source := &stubSkillSource{
		bodies: map[string]string{"deploy": "Deploy $1 to $ARGUMENTS."},
		assets: map[string][]tool.SkillAsset{"deploy": {
			{Name: "references/api.md", Size: 42},
			{Name: "scripts/run.sh", Size: 9, Executable: true},
		}},
	}
	src := NewSkillCommandSource([]tool.SkillMeta{{Name: "deploy", Description: "deploy"}}, source)
	out, expanded, err := prompt.NewSourceExpander(src).Expand(context.Background(), nil, "/deploy staging production")
	if err != nil || !expanded {
		t.Fatalf("Expand = (%q, %v, %v)", out, expanded, err)
	}
	for _, forbidden := range []string{"Base directory", "/opt/", "Read tool", "Shell"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("expansion contains forbidden %q: %q", forbidden, out)
		}
	}
	if !strings.Contains(out, "references/api.md (42 bytes)") || !strings.Contains(out, "request one with this Skill tool's asset argument") {
		t.Errorf("expansion lacks logical inventory/guidance: %q", out)
	}
	if !strings.HasSuffix(out, "Deploy staging to staging production.") {
		t.Errorf("body substitution failed: %q", out)
	}
	if source.reads != 0 {
		t.Errorf("slash expansion read %d assets, want zero", source.reads)
	}
}

func TestSkillCommandSourceUnknownAndEmpty(t *testing.T) {
	src := NewSkillCommandSource([]tool.SkillMeta{{Name: "deploy"}}, &stubSkillSource{bodies: map[string]string{"deploy": "   "}})
	for _, name := range []string{"unknown", "deploy", "Bad/Name"} {
		body, found, err := src.CommandBody(context.Background(), name)
		if err != nil || found || body != "" {
			t.Errorf("CommandBody(%q) = (%q, %v, %v)", name, body, found, err)
		}
	}
}

func TestSkillCommandSourceSourceFailures(t *testing.T) {
	boom := errors.New("backend unavailable")
	for _, tc := range []struct {
		name string
		src  *stubSkillSource
	}{
		{"body", &stubSkillSource{bodyErr: boom}},
		{"assets", &stubSkillSource{bodies: map[string]string{"deploy": "body"}, assetsErr: boom}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := NewSkillCommandSource([]tool.SkillMeta{{Name: "deploy"}}, tc.src)
			_, _, err := src.CommandBody(context.Background(), "deploy")
			if !errors.Is(err, boom) {
				t.Fatalf("error = %v, want %v", err, boom)
			}
		})
	}
}

func TestSkillCommandSourceRejectsInvalidInventory(t *testing.T) {
	src := NewSkillCommandSource([]tool.SkillMeta{{Name: "deploy"}}, &stubSkillSource{
		bodies: map[string]string{"deploy": "body"},
		assets: map[string][]tool.SkillAsset{"deploy": {{Name: "../secret"}}},
	})
	_, _, err := src.CommandBody(context.Background(), "deploy")
	if err == nil || !strings.Contains(err.Error(), "invalid logical asset") {
		t.Fatalf("error = %v", err)
	}
}

func TestSkillCommandSourceInventoryBounded(t *testing.T) {
	assets := make([]tool.SkillAsset, 1_000)
	for i := range assets {
		assets[i] = tool.SkillAsset{Name: fmt.Sprintf("references/%04d.md", i), Size: 1}
	}
	src := NewSkillCommandSource([]tool.SkillMeta{{Name: "deploy"}}, &stubSkillSource{
		bodies: map[string]string{"deploy": "body"},
		assets: map[string][]tool.SkillAsset{"deploy": assets},
	})
	_, post, found, err := src.CommandBodyWithPost(context.Background(), "deploy")
	if err != nil || !found {
		t.Fatalf("load = (%v, %v)", found, err)
	}
	if len(post) > maxBundledAssetInventoryBytes || !strings.Contains(post, "bundled assets omitted") {
		t.Fatalf("inventory not bounded/disclosed: len=%d", len(post))
	}
}
