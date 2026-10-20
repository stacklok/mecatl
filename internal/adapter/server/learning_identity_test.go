package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestLegacyProposalMaterializationPreviewAndApproval(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile("../../../engine/learning/testdata/legacy-evidence-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var frozen learning.Input
	if err := json.Unmarshal(raw, &frozen); err != nil {
		t.Fatal(err)
	}
	ref, err := learning.ResolveEvidenceHandle(frozen, "m:1")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := learning.NewCandidate(frozen, learning.Candidate{Kind: learning.CandidateOperatorFact, Key: "user/output", Value: "concise", Evidence: []learning.EvidenceRef{ref}})
	if err != nil {
		t.Fatal(err)
	}
	repo := memproposal.New()
	part := learning.ProposalPartition{Principal: "principal"}
	records, err := repo.StageBatchManifest(ctx, part, *frozen.Manifest, []learning.Candidate{candidate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if records[0].ID != "proposal-32901eff92290ebf19d2caaeabe152b2" ||
		ref.Protocol != "reflection-evidence/v1" || ref.Digest != "59bc2a8648323c05c68704393730035379cab174e1f725a2e245bd2400b0db11" ||
		records[0].InputDigest != "99e557bcd168e94aff42917a49eac21c83c37621458d2aa49972d041eab9daef" {
		t.Fatalf("frozen proposal identity changed: %+v", records[0])
	}
	snapshot := []byte(`{"id":"legacy-evidence-session","state":"completed","mode":"default","environment_ref":{"kind":"local","id":"/fixture","revision":"v1"},"messages":[{"role":"user","text":"remember legacy coordinates"},{"role":"assistant","tool_calls":[{"ID":"provider-call-legacy","Name":"Read"}],"reasoning":"private-replay"},{"role":"tool","tool_result":{"CallID":"provider-call-legacy","Content":"legacy result"}}]}`)
	source, err := sessnap.Unmarshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for i := range source.Conversation.Messages {
		source.Conversation.Messages[i].Replay = nil
	}
	store := memstore.New()
	if err := store.Save(ctx, source); err != nil {
		t.Fatal(err)
	}
	promotions := 0
	svc := &Service{cfg: Config{Store: store, Proposals: repo, ProposalPrincipal: func(*session.Principal) string { return "principal" }, ProposalManifest: repo.GetManifest, PromoteProposal: func(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, v learning.ProposalVersion, _ bool) (learning.ProposalRecord, error) {
		promotions++
		return repo.ClaimPromotion(ctx, p, id, v)
	}}}
	detail, err := svc.GetLearningProposal(ctx, string(records[0].ID), "")
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Evidence[0].Available || detail.Evidence[0].ToolCallId != string(source.Conversation.Messages[1].ToolCalls[0].ID) {
		t.Fatalf("detail %+v", detail)
	}
	if detail.Evidence[0].Protocol != string(learning.ReflectionEvidenceV1) || detail.Evidence[0].AttachmentUnavailable {
		t.Fatalf("verified citation lost protocol/attachment status: %+v", detail.Evidence[0])
	}
	encoded, err := protojson.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"provider-call-legacy", "private-replay", "reasoning_item_id"} {
		if strings.Contains(string(encoded), bad) {
			t.Fatalf("public proposal leaked %q", bad)
		}
	}
	list, err := svc.ListLearningProposals(ctx, "", "", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if list.Proposals[0].Evidence[0].ToolCallId != "" || !list.Proposals[0].Evidence[0].AttachmentUnavailable || list.Proposals[0].Evidence[0].Protocol != string(learning.ReflectionEvidenceV1) {
		t.Fatal("metadata-only list exposed unverified attachment")
	}
	// An abandoned continuation does not invalidate independently proven history.
	migratedJSON := strings.Replace(string(snapshot), `"state":"completed"`, `"state":"awaiting","pending":{"ask_id":"obsolete-ask","call":"missing","tool":"Read"}`, 1)
	migrated, err := sessnap.Unmarshal([]byte(migratedJSON))
	if err != nil {
		t.Fatal(err)
	}
	if _, pending := migrated.PendingAsk(); pending || migrated.State != session.StateIdle {
		t.Fatal("migration retained continuation authority")
	}
	if err := store.Save(ctx, migrated); err != nil {
		t.Fatal(err)
	}
	if preview, err := svc.GetLearningProposal(ctx, string(records[0].ID), ""); err != nil || !preview.Evidence[0].Available {
		t.Fatalf("verified migrated preview lost: %+v %v", preview, err)
	}
	for _, defect := range []string{"pruned", "substituted"} {
		changed, err := sessnap.Unmarshal([]byte(migratedJSON))
		if err != nil {
			t.Fatal(err)
		}
		if defect == "pruned" {
			changed.Conversation.Messages = nil
		} else {
			changed.Conversation.Messages[1].ToolCalls[0].Name = "tampered"
		}
		if err := store.Save(ctx, changed); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.DecideLearningProposal(ctx, string(records[0].ID), string(records[0].Version), "approve", "", ""); !errors.Is(err, ErrFailedPrecondition) {
			t.Fatalf("%s source authorized promotion: %v", defect, err)
		}
		unchanged, _, err := repo.Get(ctx, part, records[0].ID)
		if err != nil || unchanged.Version != records[0].Version || unchanged.Status != records[0].Status || promotions != 0 {
			t.Fatalf("invalid evidence mutated proposal: %+v %v promotions=%d", unchanged, err, promotions)
		}
	}
	if err := store.Save(ctx, migrated); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DecideLearningProposal(ctx, string(records[0].ID), string(records[0].Version), "approve", "", ""); err != nil {
		t.Fatal(err)
	}
	// A changed source cannot authorize a promotion, even with the original stored
	// immutable proposal/manifest/digests still present.
	source.Conversation.Messages[1].ToolCalls[0].Name = "tampered"
	if err := store.Save(ctx, source); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.rematerializeProposal(ctx, part, records[0]); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("changed source accepted: %v", err)
	}
}

func TestLegacyV0PublicCitationWithoutCanonicalAttachment(t *testing.T) {
	ctx := context.Background()
	var fixture struct {
		Input learning.Input
		Ref   learning.EvidenceRef
	}
	raw, err := os.ReadFile("../../../engine/learning/testdata/legacy-evidence-v0.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	source, err := sessnap.Unmarshal([]byte(`{"id":"legacy-evidence-session","state":"completed","mode":"default","environment_ref":{"kind":"local","id":"/fixture","revision":"v1"},"messages":[{"role":"assistant","tool_calls":[{"ID":"provider-call-legacy","Name":"Read"}],"reasoning":"private-replay"},{"role":"assistant","tool_calls":[{"ID":"provider-call-legacy","Name":"Read"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	store := memstore.New()
	if err := store.Save(ctx, source); err != nil {
		t.Fatal(err)
	}
	repo := memproposal.New()
	part := learning.ProposalPartition{Principal: "principal"}
	candidate, err := learning.NewCandidate(fixture.Input, learning.Candidate{Kind: learning.CandidateOperatorFact, Key: "user/output", Value: "concise", Evidence: []learning.EvidenceRef{fixture.Ref}})
	if err != nil {
		t.Fatal(err)
	}
	records, err := repo.StageBatch(ctx, part, strings.Repeat("a", 64), []learning.Candidate{candidate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	promotions := 0
	svc := &Service{cfg: Config{Store: store, Proposals: repo, ProposalPrincipal: func(*session.Principal) string { return "principal" }, PromoteProposal: func(context.Context, learning.ProposalPartition, learning.ProposalID, learning.ProposalVersion, bool) (learning.ProposalRecord, error) {
		promotions++
		return learning.ProposalRecord{}, nil
	}}}
	citation, status := svc.learningEvidenceCitation(ctx, fixture.Ref)
	if status != "available" || citation.ToolCallID != "" || !strings.Contains(citation.Preview, "Read") || strings.Contains(citation.Preview, "provider-call-legacy") {
		t.Fatalf("citation %+v status=%q", citation, status)
	}
	projection := svc.toProtoLearningProposal(ctx, learning.ProposalRecord{Candidate: learning.Candidate{Evidence: []learning.EvidenceRef{fixture.Ref}}}, nil, true)
	public := projection.Evidence[0]
	if !public.Available || !public.AttachmentUnavailable || public.ToolCallId != "" || public.Protocol != string(learning.ReflectionEvidenceLegacyV0) || !strings.Contains(public.Preview, "Read") {
		t.Fatalf("old citation availability depends on invocation attachment: %+v", public)
	}
	if _, err := svc.DecideLearningProposal(ctx, string(records[0].ID), string(records[0].Version), "approve", "", ""); !errors.Is(err, ErrFailedPrecondition) || promotions != 0 {
		t.Fatalf("unattached display authorized promotion: %v promotions=%d", err, promotions)
	}
	withoutLink := fixture.Ref
	withoutLink.ToolCallID = ""
	if _, status := svc.learningEvidenceCitation(ctx, withoutLink); status != "available" {
		t.Fatal("historical display fixture without an invocation reference did not verify")
	}
	candidate.Evidence = []learning.EvidenceRef{withoutLink}
	records, err = repo.StageBatch(ctx, part, strings.Repeat("b", 64), []learning.Candidate{candidate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DecideLearningProposal(ctx, string(records[0].ID), string(records[0].Version), "approve", "", ""); !errors.Is(err, ErrFailedPrecondition) || promotions != 0 {
		t.Fatalf("omitting invocation link authorized historical evidence: %v promotions=%d", err, promotions)
	}
	source.Conversation.Messages = nil
	if err := store.Save(ctx, source); err != nil {
		t.Fatal(err)
	}
	if available, _, _ := svc.learningEvidenceStatus(ctx, fixture.Ref); available {
		t.Fatal("missing source verified from proposal alone")
	}
}
