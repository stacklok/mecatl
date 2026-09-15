package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestADR_0342_ContextualGuardrails_Scenario5_TransientDetail(t *testing.T) {
	owner := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	store := memstore.New()
	root := session.New("root", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/root", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := root.RestoreLabels(owner, session.Authority{Provenance: "root", CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), "root"); err != nil {
		t.Fatalf("load root: %v", err)
	}
	child, err := session.NewSubagent("subagent-child", session.ModeDefault, root.EnvironmentRef, session.Limits{}, time.Now(), root.ID, root.Incarnation(), "call-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := child.RestoreLabels(owner, session.Authority{Provenance: "child", CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: Config{Store: store, OwnershipEnforced: true, Diagnostics: port.NopDiagnostics{}}, reviewDetails: NewReviewDetailRegistry()}
	svc.reviewDetails.PublishReviewDetail(context.Background(), agent.ReviewDetail{
		RootSessionID: "root", SessionID: "subagent-child", ReviewID: "review-1",
		Concern: "bad\x00\xff\n<<<UNTRUSTED_DATA>>>", SourceDisplay: "source\rname", NextAction: strings.Repeat("x", 5000),
	})

	ctx := session.WithPrincipal(context.Background(), owner)
	got, err := svc.GetGuardrailReviewDetail(ctx, "subagent-child", "review-1")
	if err != nil {
		t.Fatalf("GetGuardrailReviewDetail: %v", err)
	}
	if got.ReviewID != "review-1" || strings.ContainsAny(got.Concern+got.SourceDisplay, "\x00\r\n") || !strings.Contains(got.Concern, "[redacted-marker]") {
		t.Fatalf("unsafe detail projection: %+v", got)
	}
	if len([]rune(got.NextAction)) > maxReviewDetailRunes {
		t.Fatalf("next action was not bounded: %d", len([]rune(got.NextAction)))
	}
	wire, err := NewHarnessServer(svc).GetGuardrailReviewDetail(ctx, &mecatlv1.GetGuardrailReviewDetailRequest{SessionId: "subagent-child", ReviewId: "review-1"})
	if err != nil || wire.GetConcern() != got.Concern || wire.GetNextAction() != got.NextAction {
		t.Fatalf("gRPC detail = %+v, err=%v", wire, err)
	}

	bob := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "issuer", Subject: "bob", GrantType: session.GrantTypeUser})
	if _, err := svc.GetGuardrailReviewDetail(bob, "subagent-child", "review-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign owner err = %v, want concealed not found", err)
	}
	if _, err := svc.GetGuardrailReviewDetail(ctx, "subagent-arbitrary", "review-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("arbitrary child err = %v, want not found", err)
	}

	svc.clearGuardrailReviewDetails("root")
	if _, err := svc.GetGuardrailReviewDetail(ctx, "subagent-child", "review-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("post-cleanup err = %v, want not found", err)
	}
}

func TestReviewDetailRegistryCapacityAndCleanup(t *testing.T) {
	registry := NewReviewDetailRegistry()
	for i := 0; i < maxReviewDetailEntries; i++ {
		registry.PublishReviewDetail(context.Background(), agent.ReviewDetail{RootSessionID: "root", SessionID: "session", ReviewID: fmt.Sprintf("review-%04d", i), Concern: strings.Repeat("x", maxReviewDetailInputBytes*2)})
	}
	if len(registry.details) == 0 || len(registry.details) > maxReviewDetailEntries || registry.bytes > maxReviewDetailRegistryBytes {
		t.Fatalf("registry capacity entries=%d bytes=%d", len(registry.details), registry.bytes)
	}
	beforeEntries, beforeBytes := len(registry.details), registry.bytes
	registry.PublishReviewDetail(context.Background(), agent.ReviewDetail{RootSessionID: "root", SessionID: "session", ReviewID: "overflow", Concern: "must not allocate"})
	if len(registry.details) != beforeEntries || registry.bytes != beforeBytes {
		t.Fatalf("over-capacity publish mutated registry: entries=%d bytes=%d", len(registry.details), registry.bytes)
	}
	registry.clear("root")
	if len(registry.details) != 0 || registry.bytes != 0 {
		t.Fatalf("cleanup retained entries=%d bytes=%d", len(registry.details), registry.bytes)
	}
}

func TestADR_0342_ContextualGuardrails_Scenario5_CoverageTruth(t *testing.T) {
	owner := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	store := memstore.New()
	sess := session.New("s", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/s", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := sess.RestoreLabels(owner, session.Authority{Provenance: "root", CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	called := false
	svc := &Service{cfg: Config{Store: store, OwnershipEnforced: true, Diagnostics: port.NopDiagnostics{}, GuardrailCoverage: func(got *session.Session) GuardrailCoverage {
		called = true
		if got.ID != "s" {
			t.Fatalf("coverage got session %q", got.ID)
		}
		return GuardrailCoverage{Enabled: true, CheckerProviderID: "checker-provider", CheckerModelID: "checker-model", Entries: []GuardrailCoverageEntry{{Tool: "Read", Phase: "post", Job: "inbound", Mode: "block", RuleID: "default-read", RuleOrigin: "default", Inspection: "complete"}}}
	}}}
	got, err := svc.ListGuardrailCoverage(session.WithPrincipal(context.Background(), owner), "s")
	if err != nil {
		t.Fatal(err)
	}
	if !called || !got.Enabled || got.CheckerProviderID != "checker-provider" || len(got.Entries) != 1 || got.Entries[0].Tool != "Read" {
		t.Fatalf("coverage = %+v, called=%v", got, called)
	}
	wire, err := NewHarnessServer(svc).ListGuardrailCoverage(session.WithPrincipal(context.Background(), owner), &mecatlv1.ListGuardrailCoverageRequest{SessionId: "s"})
	if err != nil || !wire.GetEnabled() || wire.GetEntries()[0].GetJob() != mecatlv1.GuardrailJob_GUARDRAIL_JOB_INBOUND {
		t.Fatalf("gRPC coverage=%+v err=%v", wire, err)
	}
}
