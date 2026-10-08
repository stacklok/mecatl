package app

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/internal/adapter/skills"
)

// badUTF8 is the orphaned-lead-byte sequence from issue #402: BSD `cat -t` turns
// a valid em dash (E2 80 94) into E2 4D 2D 5E 40 4D 2D 5E 54, keeping the E2 lead
// byte while rendering its continuation bytes as ASCII.
const badUTF8 = "\xe2M-^@M-^T"

// TestSnapshotsSurviveInvalidUTF8 is the issue-#402 backstop for the COMPOSITION
// layer's proto builders — the sibling surface the mapper's backstop cannot reach.
//
// Skills, agent defs and souls are os.ReadFile→string off the workspace with no
// decoder to launder them (unlike MCP and provider text, which arrive JSON-decoded
// and are therefore already coerced). A .claude/skills/foo/SKILL.md or a SOUL.md
// saved in Latin-1 would otherwise fail proto.Marshal and turn ListSkills /
// ListAgents / GetSoul into codes.Internal — the same stream-killing bug class,
// on a different RPC. Under mecatequi the workspace is an attacker-authored PR
// checkout, so these files are genuinely untrusted input.
func TestSnapshotsSurviveInvalidUTF8(t *testing.T) {
	t.Run("skills", func(t *testing.T) {
		snap := skillSnapshot([]skills.Skill{{
			Name: "s" + badUTF8, Description: "d" + badUTF8, Body: "BODY", Path: "/x/SKILL.md",
		}})
		if len(snap) != 1 {
			t.Fatalf("snapshot len = %d, want 1", len(snap))
		}
		mustMarshal(t, snap[0])
		if !strings.ContainsRune(snap[0].GetName(), '�') {
			t.Errorf("name not repaired: %q", snap[0].GetName())
		}
		if !strings.ContainsRune(snap[0].GetDescription(), '�') {
			t.Errorf("description not repaired: %q", snap[0].GetDescription())
		}
	})

	t.Run("agents", func(t *testing.T) {
		// Tools is deliberately NOT seeded: scopedToolNames intersects with the
		// harness catalog, so only harness-authored names can survive into it.
		reg := agents.NewRegistry([]agents.AgentDef{{
			Name:           "a" + badUTF8,
			Description:    "d" + badUTF8,
			Model:          "m" + badUTF8,
			PermissionMode: "p" + badUTF8,
			Color:          "c" + badUTF8,
		}})
		snap := agentSnapshot(Config{}, reg)
		if len(snap) != 1 {
			t.Fatalf("snapshot len = %d, want 1", len(snap))
		}
		mustMarshal(t, snap[0])
		for field, got := range map[string]string{
			"name":            snap[0].GetName(),
			"description":     snap[0].GetDescription(),
			"model":           snap[0].GetModel(),
			"permission_mode": snap[0].GetPermissionMode(),
			"color":           snap[0].GetColor(),
		} {
			if !strings.ContainsRune(got, '�') {
				t.Errorf("%s not repaired: %q", field, got)
			}
		}
	})

	t.Run("soul", func(t *testing.T) {
		xdg := t.TempDir()
		fakeSoulEnv(t, xdg)
		writeUserSoul(t, xdg, "You are terse "+badUTF8+" and direct.")

		got := soulSnapshotWith(Config{}, newFakeIO().io())
		if got == nil {
			t.Fatal("a present user soul must yield a non-nil snapshot")
		}
		mustMarshal(t, got)
		if !strings.ContainsRune(got.GetContent(), '�') {
			t.Errorf("content not repaired: %q", got.GetContent())
		}
		// Size and hash describe the FILE, not this projection, so they are
		// computed over the original bytes and must not shift with the repair.
		if got.GetSizeBytes() == 0 || got.GetSha256() == "" {
			t.Errorf("hash/size not populated: sha=%q size=%d", got.GetSha256(), got.GetSizeBytes())
		}
	})
}

func mustMarshal(t *testing.T, m proto.Message) {
	t.Helper()
	if _, err := proto.Marshal(m); err != nil {
		t.Fatalf("proto.Marshal failed (the issue-#402 stream/RPC kill): %v", err)
	}
}
