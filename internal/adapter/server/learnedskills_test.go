package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/learning"
)

func TestLearnedSkillAPIIsPartitionedCASAndPublishes(t *testing.T) {
	repository := memskill.New()
	partition := learning.SkillPartition{Principal: reflectionPrincipal(nil)}
	draft, err := repository.CreateDraft(context.Background(), partition, "agent", learning.SkillBundle{Name: "review-code", Description: "Review code", Body: "Inspect changes."}, learning.SkillProvenance{Origin: learning.SkillProvenanceLegacyModel})
	if err != nil {
		t.Fatal(err)
	}
	evaluated, err := repository.RecordEvaluation(context.Background(), partition, "agent", draft.ID, draft.Version, draft.Revision, learning.SkillEvaluation{Verdict: learning.EvaluationPass, FixtureIDs: []string{"f"}, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	staged, err := repository.Stage(context.Background(), partition, "agent", draft.ID, draft.Version, evaluated.Revision)
	if err != nil {
		t.Fatal(err)
	}
	published := 0
	svc := &Service{cfg: Config{LearnedSkills: repository, PublishLearnedSkills: func(context.Context, learning.SkillPartition) error { published++; return nil }, SkillActionAvailable: func(learning.SkillPartition, string) (bool, string) { return true, "" }}}
	listed, err := svc.ListLearnedSkills(context.Background(), &mecatlv1.ListLearnedSkillsRequest{Limit: 1})
	if err != nil || len(listed.GetSkills()) != 1 {
		t.Fatalf("list: %+v %v", listed, err)
	}
	activated, err := svc.ActivateLearnedSkill(context.Background(), &mecatlv1.MutateLearnedSkillRequest{OwnerAgent: "agent", Id: string(staged.ID), Version: string(staged.Version), ExpectedRevision: string(staged.Revision)})
	if err != nil {
		t.Fatal(err)
	}
	if activated.GetSkill().GetState() != string(learning.SkillActive) || published != 2 {
		t.Fatalf("activate=%+v publishes=%d", activated, published)
	}
	_, err = svc.ArchiveLearnedSkill(context.Background(), &mecatlv1.MutateLearnedSkillRequest{OwnerAgent: "agent", Id: string(staged.ID), Version: string(staged.Version), ExpectedRevision: string(staged.Revision)})
	if !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("stale CAS err=%v", err)
	}
}

func TestLearnedSkillRollbackExternalCollisionFailsBeforeDurableMutation(t *testing.T) {
	repository := memskill.New()
	ctx := context.Background()
	partition := learning.SkillPartition{Principal: reflectionPrincipal(nil)}
	first := createActiveLearnedSkill(t, repository, partition, "protected", "first")
	second := createActiveLearnedSkill(t, repository, partition, "protected", "second")
	first, _, err := repository.Get(ctx, partition, "agent", first.ID, first.Version)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: Config{
		LearnedSkills:             repository,
		LearnedSkillNameAvailable: func(string) bool { return false },
		SkillActionAvailable:      func(learning.SkillPartition, string) (bool, string) { return true, "" },
	}}
	_, err = svc.RollbackLearnedSkill(ctx, &mecatlv1.RollbackLearnedSkillRequest{
		OwnerAgent: "agent", Id: string(second.ID), TargetVersion: string(first.Version), ExpectedRevision: string(second.Revision),
	})
	if !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("rollback collision err=%v", err)
	}
	firstAfter, _, getErr := repository.Get(ctx, partition, "agent", first.ID, first.Version)
	if getErr != nil {
		t.Fatal(getErr)
	}
	secondAfter, _, getErr := repository.Get(ctx, partition, "agent", second.ID, second.Version)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if firstAfter.State != learning.SkillArchived || secondAfter.State != learning.SkillActive || firstAfter.Revision != first.Revision || secondAfter.Revision != second.Revision {
		t.Fatalf("collision mutated repository: first=%+v second=%+v", firstAfter, secondAfter)
	}
}

func createActiveLearnedSkill(t *testing.T, repository learning.SkillRepository, partition learning.SkillPartition, name, body string) learning.SkillVersion {
	t.Helper()
	ctx := context.Background()
	value, err := repository.CreateDraft(ctx, partition, "agent", learning.SkillBundle{Name: name, Description: "Protected", Body: body}, learning.SkillProvenance{Origin: learning.SkillProvenanceLegacyModel})
	if err == nil {
		value, err = repository.RecordEvaluation(ctx, partition, "agent", value.ID, value.Version, value.Revision, learning.SkillEvaluation{Verdict: learning.EvaluationPass, FixtureIDs: []string{"f"}, At: time.Now()})
	}
	if err == nil {
		value, err = repository.Stage(ctx, partition, "agent", value.ID, value.Version, value.Revision)
	}
	if err == nil {
		value, err = repository.Activate(ctx, partition, "agent", value.ID, value.Version, value.Revision)
	}
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestDecodeLearnedSkillMutationIsStrictAndBounded(t *testing.T) {
	for _, body := range []string{
		`{"owner_agent":"a","owner_agent":"b"}`,
		`{"unknown":true}`,
		`{"owner_agent":"a"} {}`,
		`{"owner_agent":"` + strings.Repeat("a", learning.MaxSkillOwnerBytes+1) + `"}`,
		strings.Repeat(" ", 16<<10) + `{}`,
	} {
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		recorder := httptest.NewRecorder()
		if _, ok := (*HTTPHandler)(nil).decodeLearnedSkillMutation(recorder, req); ok {
			t.Fatalf("accepted invalid body %q", body[:min(len(body), 80)])
		}
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"project":"/p","owner_agent":"a","version":"v","expected_revision":"r"}`))
	if got, ok := (*HTTPHandler)(nil).decodeLearnedSkillMutation(httptest.NewRecorder(), req); !ok || got.Project != "/p" {
		t.Fatalf("valid body = %+v ok=%v", got, ok)
	}
}

func TestLearnedSkillProjectionRepairsAndBoundsText(t *testing.T) {
	value := learning.SkillVersion{ID: "id", Version: "v", Revision: "r", State: learning.SkillDraft, OwnerAgent: "a\xff", Bundle: learning.SkillBundle{Name: "n", Description: "d\xff", Body: strings.Repeat("x", learning.MaxSkillBodyBytes) + "\xff"}}
	got := toProtoLearnedSkill(value)
	if !strings.Contains(got.GetOwnerAgent(), "�") || !strings.Contains(got.GetDescription(), "�") {
		t.Fatalf("invalid UTF-8 not repaired: %+v", got)
	}
	if len(got.GetBody()) > learning.MaxSkillBodyBytes {
		t.Fatalf("body unbounded: %d", len(got.GetBody()))
	}
}

func TestLearnedSkillExternalCollisionBlocksActivation(t *testing.T) {
	repository := memskill.New()
	p := learning.SkillPartition{Principal: reflectionPrincipal(nil)}
	d, _ := repository.CreateDraft(context.Background(), p, "agent", learning.SkillBundle{Name: "protected", Description: "Protected", Body: "body"}, learning.SkillProvenance{Origin: learning.SkillProvenanceLegacyModel})
	e, _ := repository.RecordEvaluation(context.Background(), p, "agent", d.ID, d.Version, d.Revision, learning.SkillEvaluation{Verdict: learning.EvaluationPass, FixtureIDs: []string{"f"}, At: time.Now()})
	staged, _ := repository.Stage(context.Background(), p, "agent", d.ID, d.Version, e.Revision)
	svc := &Service{cfg: Config{LearnedSkills: repository, LearnedSkillNameAvailable: func(string) bool { return false }, SkillActionAvailable: func(learning.SkillPartition, string) (bool, string) { return true, "" }}}
	_, err := svc.ActivateLearnedSkill(context.Background(), &mecatlv1.MutateLearnedSkillRequest{OwnerAgent: "agent", Id: string(staged.ID), Version: string(staged.Version), ExpectedRevision: string(staged.Revision)})
	if !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("collision err=%v", err)
	}
}
