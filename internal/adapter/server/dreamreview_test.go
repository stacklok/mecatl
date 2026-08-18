package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type dreamReviewerStub struct {
	generated    int
	decided      int
	review       DreamReview
	receipt      DreamReceipt
	genErr       error
	decideErr    error
	lastTarget   DreamTarget
	lastID       string
	lastDecision DreamDecision
}

func (s *dreamReviewerStub) Generate(_ context.Context, target DreamTarget) (DreamReview, error) {
	s.generated++
	s.lastTarget = target
	if s.review.ID != "" || s.genErr != nil {
		return s.review, s.genErr
	}
	return DreamReview{ID: "opaque", Target: target}, nil
}

func (s *dreamReviewerStub) Decide(_ context.Context, id string, decision DreamDecision) (DreamReceipt, error) {
	s.decided++
	s.lastID, s.lastDecision = id, decision
	if s.receipt.ID != "" || s.decideErr != nil {
		return s.receipt, s.decideErr
	}
	return DreamReceipt{ID: id, Disposition: decision}, nil
}

func TestManualDreamServiceValidationAndOwnershipGate(t *testing.T) {
	stub := &dreamReviewerStub{}
	caps := DreamCapabilities{
		Generate: true, Decide: true,
		Targets: map[DreamTarget]DreamTargetCapability{
			DreamTargetProjectMemory: {Generate: true, Decide: true},
		},
	}
	svc := &Service{cfg: Config{DreamReviewer: stub, DreamCapabilities: caps}}
	if _, err := svc.GenerateDream(context.Background(), "workspace/path"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("open target accepted: %v", err)
	}
	if _, err := svc.DecideDream(context.Background(), "", DreamDecisionApply); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty id accepted: %v", err)
	}
	if _, err := svc.DecideDream(context.Background(), "opaque", "rewrite"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("open decision accepted: %v", err)
	}
	if _, err := svc.GenerateDream(context.Background(), DreamTargetProjectMemory); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DecideDream(context.Background(), "opaque", DreamDecisionDismiss); err != nil {
		t.Fatal(err)
	}
	if stub.generated != 1 || stub.decided != 1 {
		t.Fatalf("delegation counts = %d/%d", stub.generated, stub.decided)
	}

	owned := &Service{cfg: Config{OwnershipEnforced: true, DreamReviewer: stub, DreamCapabilities: caps}}
	ownedCaps := owned.ManualDreamCapabilities()
	if ownedCaps.Generate || ownedCaps.Decide || ownedCaps.Targets[DreamTargetProjectMemory].Generate || ownedCaps.UnavailableReason == "" {
		t.Fatalf("ownership capabilities = %+v", ownedCaps)
	}
	if _, err := owned.GenerateDream(context.Background(), DreamTargetProjectMemory); !errors.Is(err, ErrDreamUnavailable) {
		t.Fatalf("ownership generation = %v", err)
	}
	if _, err := owned.DecideDream(context.Background(), "opaque", DreamDecisionApply); !errors.Is(err, ErrDreamUnavailable) {
		t.Fatalf("ownership decision = %v", err)
	}
	if stub.generated != 1 || stub.decided != 1 {
		t.Fatal("ownership gate reached reviewer")
	}
}

func TestManualDreamDefaultCapabilitiesUnavailable(t *testing.T) {
	caps := (&Service{}).ManualDreamCapabilities()
	if caps.Generate || caps.Decide || caps.UnavailableReason == "" {
		t.Fatalf("zero capabilities = %+v", caps)
	}
	if _, err := (&Service{}).GenerateDream(context.Background(), DreamTargetProjectMemory); !errors.Is(err, ErrDreamUnavailable) {
		t.Fatalf("zero service generation = %v", err)
	}
}

func dreamWireService(stub *dreamReviewerStub) *Service {
	return &Service{cfg: Config{
		DreamReviewer: stub,
		DreamCapabilities: DreamCapabilities{Generate: true, Decide: true, Targets: map[DreamTarget]DreamTargetCapability{
			DreamTargetProjectMemory: {Generate: true, Decide: true},
			DreamTargetUserModel:     {Generate: true, Decide: true},
		}},
	}}
}

func TestDreamProtoProjectionRepairsInvalidUTF8AndBoundsCapabilities(t *testing.T) {
	bad := "bad\xff"
	review := DreamReview{
		ID: bad, Target: DreamTarget(bad), ExpiresAt: time.Unix(123, 0), PlannedOperations: 1, PlannedSourceCount: 2,
		Operations: []DreamOperation{{
			Kind: bad, Survivor: DreamParticipant{Key: bad, Value: bad, Description: bad},
			Sources:     []DreamParticipant{{Key: "first", Value: bad, Description: bad}, {Key: "second", Value: bad, Description: bad}},
			Replacement: DreamReplacement{Value: bad, Description: bad}, Reason: bad, ExactDuplicateEligible: true,
		}},
	}
	messages := []proto.Message{
		toProtoDreamReview(review),
		toProtoDreamReceipt(DreamReceipt{ID: bad, Target: DreamTarget(bad), Disposition: DreamDecision(bad)}),
		toProtoDreamCapabilities(DreamCapabilities{
			UnavailableReason: strings.Repeat("x", maxDreamUnavailableReasonRunes+10) + bad,
			Targets:           map[DreamTarget]DreamTargetCapability{DreamTargetProjectMemory: {}},
		}),
	}
	for _, message := range messages {
		if _, err := proto.Marshal(message); err != nil {
			t.Fatalf("proto.Marshal(%T): %v", message, err)
		}
	}
	plan := messages[0].(*mecatlv1.DreamReviewPlan)
	if got := plan.GetOperations()[0].GetSources(); len(got) != 2 || got[0].GetKey() != "first" || got[1].GetKey() != "second" || !plan.GetOperations()[0].GetExactDuplicateEligible() {
		t.Fatalf("ordered operation projection = %+v", plan.GetOperations()[0])
	}
	caps := messages[2].(*mecatlv1.ManualDreamCapabilities)
	if got := utf8.RuneCountInString(caps.GetProjectMemory().GetUnavailableReason()); got != maxDreamUnavailableReasonRunes {
		t.Fatalf("bounded reason runes = %d", got)
	}
	wireCaps := dreamWireService(&dreamReviewerStub{}).capabilities().GetManualDream()
	if !wireCaps.GetProjectMemory().GetGenerate() || !wireCaps.GetUserModel().GetDecide() {
		t.Fatalf("manual dream capability projection = %+v", wireCaps)
	}
	owned := dreamWireService(&dreamReviewerStub{})
	owned.cfg.OwnershipEnforced = true
	ownedCaps := owned.capabilities().GetManualDream()
	if ownedCaps.GetProjectMemory().GetGenerate() || ownedCaps.GetProjectMemory().GetUnavailableReason() == "" {
		t.Fatalf("owned manual dream capability projection = %+v", ownedCaps)
	}
}

func TestDreamGRPCWireAndStatusMapping(t *testing.T) {
	stub := &dreamReviewerStub{review: DreamReview{
		ID: "plan-1", Target: DreamTargetProjectMemory, ExpiresAt: time.Unix(123, 0),
		PlannedOperations: 1, PlannedSourceCount: 2,
	}}
	h := NewHarnessServer(dreamWireService(stub))
	generated, err := h.GenerateDreamPlan(context.Background(), &mecatlv1.GenerateDreamPlanRequest{Target: "project_memory"})
	if err != nil {
		t.Fatal(err)
	}
	if generated.GetPlan().GetId() != "plan-1" || stub.lastTarget != DreamTargetProjectMemory || stub.decided != 0 {
		t.Fatalf("generation = %+v, stub = %+v", generated.GetPlan(), stub)
	}

	stub.receipt = DreamReceipt{ID: "plan-1", Target: DreamTargetProjectMemory, Disposition: DreamDecisionDismiss, Planned: 2, Skipped: 2}
	decided, err := h.DecideDreamPlan(context.Background(), &mecatlv1.DecideDreamPlanRequest{PlanId: "plan-1", Decision: "dismiss"})
	if err != nil {
		t.Fatal(err)
	}
	if decided.GetReceipt().GetSkippedSourceCount() != 2 || stub.lastID != "plan-1" || stub.lastDecision != DreamDecisionDismiss {
		t.Fatalf("decision = %+v, stub = %+v", decided.GetReceipt(), stub)
	}

	if _, err = h.GenerateDreamPlan(context.Background(), &mecatlv1.GenerateDreamPlanRequest{Target: "path"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad target status = %v", status.Code(err))
	}
	if _, err = h.DecideDreamPlan(context.Background(), &mecatlv1.DecideDreamPlanRequest{PlanId: "plan-1", Decision: "rewrite"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad decision status = %v", status.Code(err))
	}
	if _, err = h.DecideDreamPlan(context.Background(), &mecatlv1.DecideDreamPlanRequest{Decision: "apply"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty id status = %v", status.Code(err))
	}

	statusCases := []struct {
		err  error
		code codes.Code
	}{
		{ErrDreamUnavailable, codes.Unimplemented}, {ErrDreamNotFound, codes.NotFound},
		{ErrDreamInProgress, codes.Aborted}, {ErrDreamConflict, codes.FailedPrecondition},
		{ErrDreamTerminalConflict, codes.AlreadyExists}, {ErrDreamCapacity, codes.ResourceExhausted},
		{ErrDreamGenerateFailed, codes.Internal}, {context.DeadlineExceeded, codes.DeadlineExceeded},
		{errors.New("raw model output: secret memory"), codes.Internal},
	}
	for _, tc := range statusCases {
		stub.genErr, stub.review = tc.err, DreamReview{}
		_, err = h.GenerateDreamPlan(context.Background(), &mecatlv1.GenerateDreamPlanRequest{Target: "project_memory"})
		if status.Code(err) != tc.code {
			t.Errorf("GenerateDreamPlan(%v) code = %v, want %v", tc.err, status.Code(err), tc.code)
		}
		if strings.Contains(status.Convert(err).Message(), "secret memory") {
			t.Fatal("raw backend error leaked over gRPC")
		}
	}

	stub.decideErr = ErrDreamApplyFailed
	stub.receipt = DreamReceipt{ID: "plan-1", Target: DreamTargetProjectMemory, Disposition: DreamDecisionApply, Planned: 2, Applied: 1, Failed: 1}
	partial, err := h.DecideDreamPlan(context.Background(), &mecatlv1.DecideDreamPlanRequest{PlanId: "plan-1", Decision: "apply"})
	if err != nil || partial.GetReceipt().GetAppliedSourceCount() != 1 || partial.GetReceipt().GetFailedSourceCount() != 1 {
		t.Fatalf("partial receipt = %+v, %v", partial, err)
	}
}

func TestDreamHTTPParityPathHandlingAndErrors(t *testing.T) {
	stub := &dreamReviewerStub{review: DreamReview{ID: "plan-1", Target: DreamTargetUserModel, ExpiresAt: time.Unix(123, 0)}}
	srv := httptest.NewServer(NewHTTPHandler(dreamWireService(stub)))
	defer srv.Close()

	post := func(path, body string) (*http.Response, []byte) {
		t.Helper()
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var payload bytes.Buffer
		_, _ = payload.ReadFrom(resp.Body)
		return resp, payload.Bytes()
	}

	resp, payload := post("/v1/dream/plans", `{"target":"user_model"}`)
	if resp.StatusCode != http.StatusOK || stub.lastTarget != DreamTargetUserModel {
		t.Fatalf("generate status=%d body=%s target=%q", resp.StatusCode, payload, stub.lastTarget)
	}
	var generated mecatlv1.GenerateDreamPlanResponse
	if err := json.Unmarshal(payload, &generated); err != nil || generated.GetPlan().GetId() != "plan-1" {
		t.Fatalf("generate response = %s, %v", payload, err)
	}

	stub.receipt = DreamReceipt{ID: "path-plan", Target: DreamTargetUserModel, Disposition: DreamDecisionDismiss, Planned: 3, Skipped: 3}
	resp, payload = post("/v1/dream/plans/path-plan/decision", `{"decision":"dismiss"}`)
	if resp.StatusCode != http.StatusOK || stub.lastID != "path-plan" || stub.lastDecision != DreamDecisionDismiss {
		t.Fatalf("decide status=%d body=%s id=%q decision=%q", resp.StatusCode, payload, stub.lastID, stub.lastDecision)
	}
	before := stub.decided
	resp, _ = post("/v1/dream/plans/path-plan/decision", `{"plan_id":"body-plan","decision":"dismiss"}`)
	if resp.StatusCode != http.StatusBadRequest || stub.decided != before {
		t.Fatalf("body id status=%d decided=%d", resp.StatusCode, stub.decided)
	}

	stub.decideErr = ErrDreamApplyFailed
	stub.receipt = DreamReceipt{ID: "partial", Target: DreamTargetUserModel, Disposition: DreamDecisionApply, Planned: 2, Applied: 1, Failed: 1}
	resp, payload = post("/v1/dream/plans/partial/decision", `{"decision":"apply"}`)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(payload, []byte(`"failed_source_count":1`)) {
		t.Fatalf("partial status=%d body=%s", resp.StatusCode, payload)
	}

	stub.genErr, stub.review = errors.New("raw model output: private memory"), DreamReview{}
	resp, payload = post("/v1/dream/plans", `{"target":"project_memory"}`)
	if resp.StatusCode != http.StatusInternalServerError || bytes.Contains(payload, []byte("private memory")) {
		t.Fatalf("raw error status=%d body=%s", resp.StatusCode, payload)
	}

	for dreamErr, want := range map[error]int{
		ErrDreamUnavailable:      http.StatusNotImplemented,
		ErrDreamNotFound:         http.StatusNotFound,
		ErrDreamInProgress:       http.StatusConflict,
		ErrDreamConflict:         http.StatusPreconditionFailed,
		ErrDreamTerminalConflict: http.StatusGone,
		ErrDreamCapacity:         http.StatusTooManyRequests,
		context.DeadlineExceeded: http.StatusGatewayTimeout,
	} {
		stub.genErr = dreamErr
		resp, _ = post("/v1/dream/plans", `{"target":"project_memory"}`)
		if resp.StatusCode != want {
			t.Errorf("generate error %v status=%d, want %d", dreamErr, resp.StatusCode, want)
		}
	}

	for body, want := range map[string]int{
		`{"target":"bad"}`: http.StatusBadRequest,
		`{"target":"project_memory","workspace":"/tmp"}`: http.StatusBadRequest,
	} {
		resp, _ = post("/v1/dream/plans", body)
		if resp.StatusCode != want {
			t.Errorf("generate body %s status=%d, want %d", body, resp.StatusCode, want)
		}
	}
}
