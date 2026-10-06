package grpcdriver

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/tool"
)

// badUTF8 is the orphaned-lead-byte sequence from issue #402: BSD `cat -t` turns
// a valid em dash (E2 80 94) into E2 4D 2D 5E 40 4D 2D 5E 54, keeping the E2 lead
// byte while rendering its continuation bytes as ASCII.
const badUTF8 = "\xe2M-^@M-^T"

// utf8BadSource is a SkillSource/AgentDefSource/CommandSource returning strings
// with invalid UTF-8 — what a real FS source hands back when a SKILL.md or an
// agent def is saved in Latin-1. os.ReadFile→string has no decoder to launder
// it, unlike the JSON hop that protects MCP and provider text.
type utf8BadSource struct{}

func (utf8BadSource) ListSkills(context.Context) ([]tool.SkillMeta, error) {
	return []tool.SkillMeta{{Name: "s" + badUTF8, Description: "d" + badUTF8, Origin: tool.SkillOriginProject}}, nil
}
func (utf8BadSource) SkillBody(context.Context, string) (string, error) {
	return "body " + badUTF8, nil
}
func (utf8BadSource) ListSkillAssets(context.Context, string) ([]tool.SkillAsset, error) {
	return nil, nil
}
func (utf8BadSource) ReadSkillAsset(context.Context, string, string) ([]byte, error) {
	return nil, nil
}

func (utf8BadSource) ListAgentDefs(context.Context) ([]tool.AgentDef, error) {
	return []tool.AgentDef{{
		Name: "a" + badUTF8, Description: "d" + badUTF8,
		Tools: []string{"Read" + badUTF8}, DisallowedTools: []string{"Edit" + badUTF8},
		Model: "m" + badUTF8, Provider: "p" + badUTF8, PermissionMode: "pm" + badUTF8,
		Color: "c" + badUTF8, Skills: []string{"sk" + badUTF8},
		MCPServers: []tool.AgentMCPServer{{Name: "sv" + badUTF8, URL: "https://x/" + badUTF8}},
		Hooks:      map[string]string{"k" + badUTF8: "v" + badUTF8},
		Body:       "BODY " + badUTF8,
	}}, nil
}

func (utf8BadSource) ListCommands(context.Context) ([]prompt.Command, error) {
	return []prompt.Command{{Name: "c" + badUTF8, Description: "d" + badUTF8}}, nil
}
func (utf8BadSource) CommandBody(context.Context, string) (string, bool, error) {
	return "tmpl " + badUTF8, true, nil
}

// TestDriverServersSurviveInvalidUTF8 is the issue-#402 backstop for the DRIVER
// protocol — the second proto projection, which internal/adapter/server's mapper
// backstop cannot reach. Skill/agent/soul/command content is read off the
// workspace as raw bytes, so a Latin-1 file would fail proto.Marshal and turn
// ListSkills / GetSkillBody / ListAgentDefs / LoadSoul into codes.Internal.
func TestDriverServersSurviveInvalidUTF8(t *testing.T) {
	ctx := context.Background()
	src := utf8BadSource{}

	skillSrv := NewSkillSourceServer(src)

	list, err := skillSrv.ListSkills(ctx, &driverv1.ListSkillsRequest{})
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	mustMarshal(t, "ListSkills", list)
	if !strings.ContainsRune(list.GetSkills()[0].GetName(), '�') {
		t.Errorf("skill name not repaired: %q", list.GetSkills()[0].GetName())
	}

	body, err := skillSrv.GetSkillBody(ctx, &driverv1.GetSkillBodyRequest{Name: "s"})
	if err != nil {
		t.Fatalf("GetSkillBody: %v", err)
	}
	mustMarshal(t, "GetSkillBody", body)

	defs, err := NewAgentSourceServer(src).ListAgentDefs(ctx, &driverv1.ListAgentDefsRequest{})
	if err != nil {
		t.Fatalf("ListAgentDefs: %v", err)
	}
	mustMarshal(t, "ListAgentDefs", defs)

	if got := defs.GetAgentDefs()[0].GetName(); !strings.ContainsRune(got, '�') {
		t.Errorf("agent name not repaired: %q", got)
	}

	cmdSrv := NewCommandSourceServer(src)
	cmds, err := cmdSrv.ListCommands(ctx, &driverv1.ListCommandsRequest{})
	if err != nil {
		t.Fatalf("ListCommands: %v", err)
	}
	mustMarshal(t, "ListCommands", cmds)
	cmdBody, err := cmdSrv.GetCommandBody(ctx, &driverv1.GetCommandBodyRequest{Name: "c"})
	if err != nil {
		t.Fatalf("GetCommandBody: %v", err)
	}
	mustMarshal(t, "GetCommandBody", cmdBody)

	soul, err := NewSoulSourceServer(soulBodyFunc("soul "+badUTF8)).LoadSoul(ctx, &driverv1.LoadSoulRequest{})
	if err != nil {
		t.Fatalf("LoadSoul: %v", err)
	}
	mustMarshal(t, "LoadSoul", soul)
	if !strings.ContainsRune(soul.GetBody(), '�') {
		t.Errorf("soul body not repaired: %q", soul.GetBody())
	}
}

// TestAgentMCPHeadersStayByteExact pins the ONE deliberate non-repair: header
// values are secret-shaped (AGENTS.md), and rewriting a credential to make it
// marshal would corrupt the thing it carries. Bytes that are already valid must
// cross untouched.
func TestAgentMCPHeadersStayByteExact(t *testing.T) {
	const secret = "Bearer sk-live-ééé"
	out := toProtoMCPServers([]tool.AgentMCPServer{{
		Name: "sv", URL: "https://x", Headers: map[string]string{"Authorization": secret},
	}})
	if got := out[0].GetHeaders()["Authorization"]; got != secret {
		t.Fatalf("header rewritten: got %q want %q", got, secret)
	}
}

func mustMarshal(t *testing.T, name string, m proto.Message) {
	t.Helper()
	if _, err := proto.Marshal(m); err != nil {
		t.Fatalf("%s: proto.Marshal failed (the issue-#402 RPC kill): %v", name, err)
	}
}
