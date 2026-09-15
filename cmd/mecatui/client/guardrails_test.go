package client

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type fakeGuardrailClient struct {
	mecatlv1.HarnessServiceClient
	coverage *mecatlv1.ListGuardrailCoverageResponse
	detail   *mecatlv1.GetGuardrailReviewDetailResponse
}

func (f *fakeGuardrailClient) ListGuardrailCoverage(context.Context, *mecatlv1.ListGuardrailCoverageRequest, ...grpc.CallOption) (*mecatlv1.ListGuardrailCoverageResponse, error) {
	return f.coverage, nil
}
func (f *fakeGuardrailClient) GetGuardrailReviewDetail(context.Context, *mecatlv1.GetGuardrailReviewDetailRequest, ...grpc.CallOption) (*mecatlv1.GetGuardrailReviewDetailResponse, error) {
	return f.detail, nil
}

func TestADR_0342_ContextualGuardrails_Scenario7_InterfaceProjectionSafety(t *testing.T) {
	event := &mecatlv1.Event{Type: "permission.ask", RunId: "run-1", Ask: &mecatlv1.PermissionAsk{AskId: "a", Tool: "Shell", Guardrail: &mecatlv1.GuardrailApprovalScope{ReviewId: "r", Kind: mecatlv1.GuardrailApprovalKind_GUARDRAIL_APPROVAL_KIND_RESULT_RELEASE}}}
	ask, ok := EventToMsg(event).(PermissionAskMsg)
	if !ok || ask.ExpectedRunID != "run-1" || ask.Guardrail == nil || ask.Guardrail.Kind != "result_release" || ask.Guardrail.RepeatAvailable {
		t.Fatalf("ask = %#v", ask)
	}
	hook, ok := EventToMsg(&mecatlv1.Event{Type: "hook", Hook: &mecatlv1.Hook{Guardrail: &mecatlv1.GuardrailReview{ReviewId: "r", Job: mecatlv1.GuardrailJob_GUARDRAIL_JOB_INBOUND, Assessment: mecatlv1.GuardrailAssessment_GUARDRAIL_ASSESSMENT_UNRESOLVED, Inspection: mecatlv1.GuardrailInspection_GUARDRAIL_INSPECTION_OPERATIONAL_FAILURE}}}).(HookMsg)
	if !ok || hook.Guardrail == nil || hook.Guardrail.Inspection != "operational_failure" || hook.Guardrail.Assessment != "unresolved" {
		t.Fatalf("hook = %#v", hook)
	}

	fake := &fakeGuardrailClient{coverage: &mecatlv1.ListGuardrailCoverageResponse{Enabled: true, CheckerProviderId: "p", CheckerModelId: "m", Entries: []*mecatlv1.GuardrailCoverageEntry{{Tool: "Read", Phase: "post", Job: mecatlv1.GuardrailJob_GUARDRAIL_JOB_INBOUND, Inspection: mecatlv1.GuardrailInspection_GUARDRAIL_INSPECTION_COMPLETE}}}, detail: &mecatlv1.GetGuardrailReviewDetailResponse{ReviewId: "r", Concern: "finding", SourceDisplay: "source", NextAction: "release"}}
	cl := newFakeClient(fake)
	coverage, err := cl.ListGuardrailCoverage(context.Background(), "s")
	if err != nil || len(coverage.Entries) != 1 || coverage.Entries[0].Job != "inbound" {
		t.Fatalf("coverage=%+v err=%v", coverage, err)
	}
	detail, err := cl.GetGuardrailReviewDetail(context.Background(), "s", "r")
	if err != nil || detail.Concern != "finding" {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
}
