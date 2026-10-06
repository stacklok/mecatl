package grpcdriver

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/grpc"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
)

// hostileAgentServer is a RAW driverv1 server (NOT the harness wrapper) so the
// client's defensive normalization can be fed wire data a well-behaved Go
// source could never produce: blank names, duplicates, unsorted order,
// oversized descriptions/bodies, trusted-tier origin claims, dirty
// hooks/headers.
type hostileAgentServer struct {
	driverv1.UnimplementedAgentSourceServiceServer
	defs []*driverv1.AgentDef
}

func (s *hostileAgentServer) ListAgentDefs(context.Context, *driverv1.ListAgentDefsRequest) (*driverv1.ListAgentDefsResponse, error) {
	return &driverv1.ListAgentDefsResponse{AgentDefs: s.defs}, nil
}

func newHostileAgentClient(t *testing.T, defs []*driverv1.AgentDef) *AgentSource {
	t.Helper()
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterAgentSourceServiceServer(gs, &hostileAgentServer{defs: defs})
	})
	return NewAgentSource(conn, AgentOptions{})
}

// TestAgentSourceListDefensiveDiscipline pins the §1.C client discipline: drop
// blank names, de-dup first-wins, sort by name, force descriptions
// single-line then truncate to the always-in-context cap, re-cap bodies, and
// stamp Origin AgentOriginDriver UNCONDITIONALLY (a driver must not claim
// project/user tier labels).
func TestAgentSourceListDefensiveDiscipline(t *testing.T) {
	longDesc := strings.Repeat("d", tool.MaxAgentDescriptionBytes*2)
	longBody := strings.Repeat("b", tool.MaxAgentBodyBytes*2)
	src := newHostileAgentClient(t, []*driverv1.AgentDef{
		{Name: "zeta", Description: "claims a trusted tier", Origin: "project"},
		{Name: "", Description: "blank name must be dropped"},
		{Name: "   ", Description: "whitespace name must be dropped"},
		{Name: "dup", Description: "first wins", Origin: "user"},
		{Name: "dup", Description: "second loses"},
		{Name: "alpha", Description: longDesc, Body: longBody},
		{Name: "multiline", Description: "line one\nline two\r\n\tline three\x7f!"},
	})
	got, err := src.ListAgentDefs(context.Background())
	if err != nil {
		t.Fatalf("ListAgentDefs: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("ListAgentDefs kept %d defs, want 4 (blank dropped, dup de-duped): %+v", len(got), got)
	}
	if got[0].Name != "alpha" || got[1].Name != "dup" || got[2].Name != "multiline" || got[3].Name != "zeta" {
		t.Errorf("not name-sorted: %q, %q, %q, %q", got[0].Name, got[1].Name, got[2].Name, got[3].Name)
	}
	if len(got[0].Description) > tool.MaxAgentDescriptionBytes {
		t.Errorf("description not truncated to the cap: %d > %d", len(got[0].Description), tool.MaxAgentDescriptionBytes)
	}
	if len(got[0].Body) > tool.MaxAgentBodyBytes {
		t.Errorf("body not truncated to the cap: %d > %d", len(got[0].Body), tool.MaxAgentBodyBytes)
	}
	if got[1].Description != "first wins" {
		t.Errorf("de-dup kept %q, want the FIRST wire entry", got[1].Description)
	}
	if strings.ContainsAny(got[2].Description, "\n\r\t\x7f") {
		t.Errorf("multi-line description leaked control characters: %q", got[2].Description)
	}
	if want := "line one line two   line three !"; got[2].Description != want {
		t.Errorf("single-line normalization = %q, want %q", got[2].Description, want)
	}
	// Origin is stamped driver UNCONDITIONALLY — even "project"/"user" claims.
	for _, d := range got {
		if d.Origin != tool.AgentOriginDriver {
			t.Errorf("def %q Origin = %q, want driver (a driver-listed def is ALWAYS driver tier)", d.Name, d.Origin)
		}
	}
}

// TestAgentSourceHooksHeadersNormalized pins the hooks/headers re-normalization
// through the SAME exported helpers the frontmatter parser uses: trimmed
// keys/values, empties dropped, nil for an empty map.
func TestAgentSourceHooksHeadersNormalized(t *testing.T) {
	src := newHostileAgentClient(t, []*driverv1.AgentDef{
		{
			Name:        "spec",
			Description: "specialist",
			Hooks: map[string]string{
				" PreToolUse ": " echo pre ",
				"":             "echo orphan",
				"Stop":         "   ",
			},
			McpServers: []*driverv1.AgentMCPServer{
				{
					Name: "inline",
					Url:  "https://example.test/mcp",
					Headers: map[string]string{
						" Authorization ": " Bearer tok ",
						"Empty":           "  ",
					},
				},
				{Name: "ref"},
			},
		},
		{Name: "bare", Description: "no hooks, no servers"},
	})
	got, err := src.ListAgentDefs(context.Background())
	if err != nil {
		t.Fatalf("ListAgentDefs: %v", err)
	}
	spec := got[1] // name-sorted: bare, spec
	if spec.Name != "spec" {
		t.Fatalf("unexpected order: %+v", got)
	}
	if want := map[string]string{"PreToolUse": "echo pre"}; !reflect.DeepEqual(spec.Hooks, want) {
		t.Errorf("Hooks = %+v, want %+v (trimmed, empties dropped)", spec.Hooks, want)
	}
	if len(spec.MCPServers) != 2 {
		t.Fatalf("MCPServers = %+v, want 2 entries", spec.MCPServers)
	}
	inline := spec.MCPServers[0]
	if want := map[string]string{"Authorization": "Bearer tok"}; !reflect.DeepEqual(inline.Headers, want) {
		t.Errorf("Headers = %+v, want %+v (trimmed, empties dropped)", inline.Headers, want)
	}
	if ref := spec.MCPServers[1]; !ref.IsReference() || ref.Headers != nil {
		t.Errorf("reference entry = %+v, want url-less with nil headers", ref)
	}
	bare := got[0]
	if bare.Hooks != nil || bare.MCPServers != nil {
		t.Errorf("absent hooks/servers must stay nil, got hooks=%v servers=%v", bare.Hooks, bare.MCPServers)
	}
}

// TestAgentSourceCtxRewrap pins the §H context row: a caller-cancelled ctx
// surfaces so errors.Is(err, context.Canceled) holds harness-side.
func TestAgentSourceCtxRewrap(t *testing.T) {
	src := newHostileAgentClient(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := src.ListAgentDefs(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("ListAgentDefs(cancelled ctx) = %v, want errors.Is(_, context.Canceled)", err)
	}
}

// TestAgentSourceNameTrimmedForDedupAndStorage pins the trim discipline: the
// FS parser trims frontmatter names, so the wire client must not be weaker —
// " x" and "x" are the SAME def (first wins) and the kept name is trimmed.
func TestAgentSourceNameTrimmedForDedupAndStorage(t *testing.T) {
	src := newHostileAgentClient(t, []*driverv1.AgentDef{
		{Name: " x", Description: "padded first"},
		{Name: "x", Description: "bare second"},
		{Name: "\ty \n", Description: "tabby"},
	})
	got, err := src.ListAgentDefs(context.Background())
	if err != nil {
		t.Fatalf("ListAgentDefs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListAgentDefs kept %d defs, want 2 (\" x\" and \"x\" de-dup to one): %+v", len(got), got)
	}
	if got[0].Name != "x" || got[1].Name != "y" {
		t.Errorf("stored names must be trimmed: %q, %q", got[0].Name, got[1].Name)
	}
	if got[0].Description != "padded first" {
		t.Errorf("de-dup kept %q, want the FIRST wire entry (trimmed key)", got[0].Description)
	}
}

// TestAgentSourceCountCapDropsDefWithWarn pins one representative count cap
// (hooks > maxAgentHookEntries): the over-cap def is DROPPED with a WARN
// naming it — fail-soft per def, never a fatal snapshot error — and its
// well-formed siblings survive.
func TestAgentSourceCountCapDropsDefWithWarn(t *testing.T) {
	bigHooks := make(map[string]string, maxAgentHookEntries+1)
	for i := 0; i <= maxAgentHookEntries; i++ {
		bigHooks["Phase"+strconv.Itoa(i)] = "echo " + strconv.Itoa(i)
	}
	diag := &recordingDiag{}
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterAgentSourceServiceServer(gs, &hostileAgentServer{defs: []*driverv1.AgentDef{
			{Name: "bloated", Description: "too many hooks", Hooks: bigHooks},
			{Name: "sane", Description: "fine"},
		}})
	})
	src := NewAgentSource(conn, AgentOptions{Diagnostics: diag})
	got, err := src.ListAgentDefs(context.Background())
	if err != nil {
		t.Fatalf("ListAgentDefs: %v (over-cap must be fail-soft per def, not an error)", err)
	}
	if len(got) != 1 || got[0].Name != "sane" {
		t.Fatalf("ListAgentDefs = %+v, want only the sane def (bloated dropped)", got)
	}
	if !diag.find(port.LevelWarn, "exceeds a count cap") {
		t.Errorf("dropping an over-cap def must WARN through Diagnostics; got %+v", diag.entries)
	}
}
