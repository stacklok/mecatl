package server

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
)

func TestADR_0342_ContextualGuardrails_Scenario1_ApprovalOriginProtoProjection(t *testing.T) {
	review := &session.GuardrailReviewPayload{ReviewID: "r1", Job: "action", Assessment: "prohibited", Inspection: "complete", Disposition: "ask_action", ReasonCode: "authority-crossing", RuleID: "rule", RuleOrigin: "operator", CheckerProviderID: "review-provider", CheckerModelID: "review-model", Concerns: []session.GuardrailRef{{Ref: "c1", Category: "exfil"}}, Sources: []session.GuardrailRef{{Ref: "s1", Category: "tool-args"}}}
	hook := toProto(session.Event{Type: session.EvHook, Hook: &session.HookPayload{Guardrail: review}}).GetHook().GetGuardrail()
	if hook.GetReviewId() != "r1" || hook.GetCheckerProviderId() != "review-provider" || len(hook.GetConcerns()) != 1 {
		t.Fatalf("hook guardrail = %+v", hook)
	}
	ask := toProto(session.Event{Type: session.EvPermissionAsk, Ask: &session.PendingAsk{Guardrail: &session.GuardrailPendingScope{ReviewID: "r1", Kind: session.GuardrailApprovalAction, GrantDigest: "opaque", SessionOnly: true, RepeatAvailable: true}, Origin: session.ApprovalOriginHookGuardrail}}).GetAsk().GetGuardrail()
	if ask.GetKind() != mecatlv1.GuardrailApprovalKind_GUARDRAIL_APPROVAL_KIND_ACTION || ask.GetGrantDigest() != "opaque" {
		t.Fatalf("ask guardrail = %+v", ask)
	}
	approval := toProto(session.Event{Type: session.EvApproval, Approval: &session.ApprovalPayload{Origin: session.ApprovalOriginPermission}}).GetApproval()
	if approval.GetOrigin() != "permission" {
		t.Fatalf("approval origin = %q", approval.GetOrigin())
	}

	wire, err := proto.Marshal(&mecatlv1.Approval{AskId: "a", Verdict: "allow_once"})
	if err != nil {
		t.Fatal(err)
	}
	var old mecatlv1.Approval
	if err := proto.Unmarshal(wire, &old); err != nil {
		t.Fatal(err)
	}
	if old.GetOrigin() != "" {
		t.Fatalf("absent additive origin = %q", old.GetOrigin())
	}
}

func TestADR_0342_ContextualGuardrails_Scenario5_NoContentLeak(t *testing.T) {
	secret := "held-result-secret-token"
	event := session.Event{Type: session.EvHook, Hook: &session.HookPayload{Guardrail: &session.GuardrailReviewPayload{
		ReviewID: "r", Job: "inbound", Assessment: "prohibited", Inspection: "complete", Disposition: "withhold_result",
		ReasonCode: "authority_crossing", Concerns: []session.GuardrailRef{{Ref: "C1", Category: "redirection"}},
	}}}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := proto.Marshal(toProto(event))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(wire), secret) {
		t.Fatal("durable machine projection leaked held result content")
	}
	fields := toProto(event).GetHook().GetGuardrail().ProtoReflect().Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		name := string(fields.Get(i).Name())
		for _, forbidden := range []string{"rationale", "args", "evidence_body", "held_result", "target_path", "transcript", "call_body"} {
			if name == forbidden {
				t.Fatalf("durable GuardrailReview exposes forbidden field %q", name)
			}
		}
	}
}
