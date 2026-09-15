package app

import (
	"testing"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/modelhook"
)

func TestADR_0342_ContextualGuardrails_Scenario1_ExactRepeatGrant(t *testing.T) {
	grants := modelhook.NewWaiverHolder()
	r := &guardrailActionReviewer{grants: grants}
	request := agent.ToolReviewRequest{
		Event:            governance.HookEvent{SessionID: "session-a"},
		EffectiveCall:    session.NewToolCall("call-a", writeToolName, []byte(`{"path":"a","content":"x y"}`)),
		Caller:           agent.ReviewCaller{Role: "main", Capabilities: []string{"Write", "Read"}},
		Environment:      session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "placement", Revision: "revision-1"},
		Target:           agent.ReviewTarget{Kind: "workspace", DestinationID: "a"},
		PrincipalFacts:   []agent.ReviewPrincipalFact{{Kind: "target_dependency_version", Ref: "dependency-0", Statement: "a\x00version-1"}},
		EvidenceComplete: true, TrajectoryComplete: true,
	}
	digest, available := r.GrantDigest(request)
	if !available || digest == "" {
		t.Fatalf("digest=%q available=%v", digest, available)
	}
	r.ArmGrant(digest, request.Event.SessionID)
	if !r.AllowsGrant(digest) {
		t.Fatal("armed exact grant missed")
	}
	grants.ClearSession(request.Event.SessionID)
	if r.AllowsGrant(digest) {
		t.Fatal("grant survived session clear")
	}
	r.ArmGrant(digest, request.Event.SessionID)
	assertMiss := func(name string, mutate func(*agent.ToolReviewRequest)) {
		t.Helper()
		changed := request
		changed.Caller.Capabilities = append([]string(nil), request.Caller.Capabilities...)
		changed.PrincipalFacts = append([]agent.ReviewPrincipalFact(nil), request.PrincipalFacts...)
		mutate(&changed)
		got, ok := r.GrantDigest(changed)
		if ok && r.AllowsGrant(got) {
			t.Fatalf("%s unexpectedly reused exact grant", name)
		}
	}
	assertMiss("argument whitespace", func(req *agent.ToolReviewRequest) { req.EffectiveCall.Args = []byte(`{"path":"a","content":"x  y"}`) })
	assertMiss("target version", func(req *agent.ToolReviewRequest) { req.PrincipalFacts[0].Statement = "a\x00version-2" })
	assertMiss("environment revision", func(req *agent.ToolReviewRequest) { req.Environment.Revision = "revision-2" })
	assertMiss("destination", func(req *agent.ToolReviewRequest) { req.Target.DestinationID = "b" })
	assertMiss("session", func(req *agent.ToolReviewRequest) { req.Event.SessionID = "session-b" })
	assertMiss("incomplete dependency set", func(req *agent.ToolReviewRequest) {
		req.PrincipalFacts = append(req.PrincipalFacts, agent.ReviewPrincipalFact{Kind: "repeat_dependency_incomplete"})
	})

	otherProcess := &guardrailActionReviewer{grants: modelhook.NewWaiverHolder()}
	otherDigest, _ := otherProcess.GrantDigest(request)
	if otherDigest == digest || otherProcess.AllowsGrant(digest) {
		t.Fatal("grant crossed Build/process-local keyed owner")
	}
}
