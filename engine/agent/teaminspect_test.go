package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestInspectMemberPullsOneMemberOnly asserts requirement E: the InspectMember tool
// reads exactly ONE persisted member's transcript by (team id, member) and renders a
// bounded result, without surfacing any OTHER member's content (no auto-injection) —
// and an unknown id is a model-addressable error, not a harness fault.
func TestInspectMemberPullsOneMemberOnly(t *testing.T) {
	store := memstore.New()
	ctx := context.Background()
	teamID := "p1"

	// Persist two member sessions under the SHARED namespaced id scheme.
	alpha := seedMemberSession(t, agent.MemberSessionID(teamID, "alpha"),
		"investigate alpha", "ALPHA_FINDING the alpha path is fine")
	if err := store.Save(ctx, alpha); err != nil {
		t.Fatalf("save alpha: %v", err)
	}
	beta := seedMemberSession(t, agent.MemberSessionID(teamID, "beta"),
		"investigate beta", "BETA_SECRET the beta path leaks")
	if err := store.Save(ctx, beta); err != nil {
		t.Fatalf("save beta: %v", err)
	}

	inspect := agent.NewInspectMemberTool(store)

	// Pull alpha ONLY.
	res := call(t, inspect, `{"team_id":"p1","member":"alpha"}`)
	if res.IsError {
		t.Fatalf("InspectMember(alpha) errored: %s", res.Content)
	}
	if !strings.Contains(res.Content, "ALPHA_FINDING the alpha path is fine") {
		t.Errorf("inspect result missing alpha's transcript:\n%s", res.Content)
	}
	// CRITICAL: beta's content must NOT leak — the pull is one member only.
	if strings.Contains(res.Content, "BETA_SECRET") {
		t.Fatalf("inspecting alpha leaked beta's content (no isolation):\n%s", res.Content)
	}

	// An unknown member is a model-addressable error result, not a harness error.
	miss := call(t, inspect, `{"team_id":"p1","member":"ghost"}`)
	if !miss.IsError {
		t.Fatal("InspectMember for an unknown member should be an error result")
	}
	if !strings.Contains(miss.Content, "ghost") {
		t.Errorf("unknown-member error should name the member: %q", miss.Content)
	}

	// Missing args → model-addressable error.
	if bad := call(t, inspect, `{"team_id":"p1"}`); !bad.IsError {
		t.Fatal("InspectMember with no member should be an error result")
	}
}

// TestInspectMemberReadOnly pins that the inspect tool is read-parallel-safe.
func TestInspectMemberReadOnly(t *testing.T) {
	inspect := agent.NewInspectMemberTool(memstore.New())
	ro, ok := inspect.(interface{ ReadOnly() bool })
	if !ok || !ro.ReadOnly() {
		t.Fatalf("InspectMember must report ReadOnly()==true")
	}
}

// TestInspectMemberOwnershipPolicy keeps transcript access aligned with the request
// edge: verified callers are isolated by the full (Issuer, Subject) identity pair.
// The no-verifier compatibility path (a genuinely absent principal, e.g. no OIDC
// wired) stays permissive, but a VERIFIED foreign caller is denied even when the
// tool was constructed without ownership enforcement — the legacy constructor must
// not be MORE permissive than the predicate it is backward-compatible with.
func TestInspectMemberOwnershipPolicy(t *testing.T) {
	store := memstore.New()
	member := seedMemberSession(t, agent.MemberSessionID("team-a", "worker"), "task", "OWNER SECRET")
	owner := &session.Principal{Issuer: "https://issuer-a.example", Subject: "same-subject", GrantType: session.GrantTypeUser}
	if err := member.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}
	if err := store.Save(context.Background(), member); err != nil {
		t.Fatalf("Save: %v", err)
	}
	call := session.NewToolCall("inspect", "InspectMember", json.RawMessage(`{"team_id":"team-a","member":"worker"}`))
	ownerCtx := session.WithPrincipal(context.Background(), owner)
	foreignCtx := session.WithPrincipal(context.Background(), &session.Principal{
		Issuer: "https://issuer-b.example", Subject: "same-subject", GrantType: session.GrantTypeUser,
	})

	for _, tc := range []struct {
		name     string
		tool     tool.Tool
		ctx      context.Context
		wantText bool
	}{
		{"enforced owner", agent.NewInspectMemberToolWithOwnership(store, true), ownerCtx, true},
		{"enforced foreign issuer", agent.NewInspectMemberToolWithOwnership(store, true), foreignCtx, false},
		{"legacy compatibility: no verifier configured", agent.NewInspectMemberTool(store), context.Background(), true},
		{"legacy compatibility: verified foreign caller still denied", agent.NewInspectMemberTool(store), foreignCtx, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tc.tool.Execute(tc.ctx, call, agent.MemEnv("/ws"))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if tc.wantText {
				if res.IsError || !strings.Contains(res.Content, "OWNER SECRET") {
					t.Fatalf("result = %+v, want owner transcript", res)
				}
				return
			}
			if !res.IsError || strings.Contains(res.Content, "OWNER SECRET") || !strings.Contains(res.Content, "no transcript") {
				t.Fatalf("result = %+v, want absence-shaped denial", res)
			}
		})
	}
}

// TestInspectMemberBounded asserts a long member transcript is rendered BOUNDED, not
// copied verbatim.
func TestInspectMemberBounded(t *testing.T) {
	store := memstore.New()
	ctx := context.Background()
	teamID := "p1"
	long := strings.Repeat("y", 5000)
	s := seedMemberSession(t, agent.MemberSessionID(teamID, "verbose"), "go", long)
	if err := store.Save(ctx, s); err != nil {
		t.Fatalf("save: %v", err)
	}
	res := call(t, agent.NewInspectMemberTool(store), `{"team_id":"p1","member":"verbose"}`)
	if res.IsError {
		t.Fatalf("inspect errored: %s", res.Content)
	}
	if strings.Count(res.Content, "y") >= len(long) {
		t.Fatalf("transcript was not bounded: contains the full %d-rune body", len(long))
	}
}

// seedMemberSession builds a member session with one user prompt + one assistant
// reply, recorded through the aggregate methods, ready to persist.
func seedMemberSession(t *testing.T, id session.SessionID, userText, assistantText string) *session.Session {
	t.Helper()
	s := session.New(id, session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	if err := s.RecordUserPrompt(userText, nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := s.RecordAssistant(session.NewAssistantMessage(assistantText, "", nil)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	return s
}

// brokenStore is a port.SessionStore whose Load always fails with an infrastructure
// error that is NOT port.ErrSessionNotFound — exercising the InspectMember path that
// must surface a real store failure distinctly from a clean not-found miss.
type brokenStore struct{}

var errBrokenStore = errors.New("disk on fire")

func (brokenStore) Save(context.Context, *session.Session) error { return errBrokenStore }
func (brokenStore) Load(context.Context, session.SessionID) (*session.Session, error) {
	return nil, errBrokenStore
}

// TestInspectMemberDistinguishesLoadFailureFromNotFound asserts the two error paths
// are distinct: a clean not-found (port.ErrSessionNotFound) reads as "no transcript",
// while a genuine infrastructure failure surfaces the underlying error rather than
// being collapsed into "no transcript".
func TestInspectMemberDistinguishesLoadFailureFromNotFound(t *testing.T) {
	var _ port.SessionStore = brokenStore{}

	// Not-found path: empty memstore → port.ErrSessionNotFound → "no transcript".
	notFound := call(t, agent.NewInspectMemberTool(memstore.New()), `{"team_id":"p1","member":"ghost"}`)
	if !notFound.IsError {
		t.Fatal("not-found should be an error result")
	}
	if !strings.Contains(notFound.Content, "no transcript") {
		t.Errorf("not-found result should read as 'no transcript', got %q", notFound.Content)
	}

	// Infra-failure path: a broken store → surfaced distinctly (NOT "no transcript").
	broken := call(t, agent.NewInspectMemberTool(brokenStore{}), `{"team_id":"p1","member":"scout"}`)
	if !broken.IsError {
		t.Fatal("infra failure should be an error result")
	}
	if strings.Contains(broken.Content, "no transcript") {
		t.Errorf("infra failure must NOT be reported as 'no transcript': %q", broken.Content)
	}
	if !strings.Contains(broken.Content, "failed to load") || !strings.Contains(broken.Content, "disk on fire") {
		t.Errorf("infra failure should surface the underlying error, got %q", broken.Content)
	}
}
