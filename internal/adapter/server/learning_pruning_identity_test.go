package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestFrozenV1ProposalSurvivesUnselectedPrefixAndReplayPruning(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile("../../../engine/learning/testdata/legacy-evidence-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var frozen learning.Input
	if err := json.Unmarshal(raw, &frozen); err != nil {
		t.Fatal(err)
	}
	// Frozen v1 selection at original coordinates 1..3; coordinate 0 is unselected.
	const manifestJSON = `{"protocol":"reflection-evidence/v1","source":{"domain":"mecatl/reflection-evidence/source/v1","value":"legacy-evidence-session"},"entries":[{"handle":"m:0","locator":"message","original_message":1,"digest":"fe5688c076fb5a35112fa49814db63c1ef1af41eabc7e3ecbea2f2dc80c1e851","component":"message:1"},{"handle":"m:1","locator":"message","original_message":2,"digest":"59bc2a8648323c05c68704393730035379cab174e1f725a2e245bd2400b0db11","component":"tool:2","tool_call_ids":["provider-call-legacy"]},{"handle":"m:2","locator":"message","original_message":3,"digest":"c3efd120284c6d3eaec2d1fa73125edbc6d4d4bcb7f4a08cf885f18fe77df928","component":"tool:2","tool_call_ids":["provider-call-legacy"]}],"digest":"ec765ff1dc42cb131f87fd5d3c08e0f8041a3b7faf506d792763e194cf59c33a"}`
	if err := json.Unmarshal([]byte(manifestJSON), &frozen.Manifest); err != nil {
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
	source, err := sessnap.Unmarshal([]byte(`{"id":"legacy-evidence-session","state":"completed","mode":"default","environment_ref":{"kind":"local","id":"/fixture","revision":"v1"},"messages":[{"role":"user","text":"unselected prefix"},{"role":"user","text":"remember legacy coordinates"},{"role":"assistant","tool_calls":[{"ID":"provider-call-legacy","Name":"Read"}],"reasoning":"private-replay"},{"role":"tool","tool_result":{"CallID":"provider-call-legacy","Content":"legacy result"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	store := memstore.New()
	repo := memproposal.New()
	part := learning.ProposalPartition{Principal: "principal"}
	records, err := repo.StageBatchManifest(ctx, part, *frozen.Manifest, []learning.Candidate{candidate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{cfg: Config{Store: store, Proposals: repo, ProposalManifest: repo.GetManifest, ProposalPrincipal: func(*session.Principal) string { return "principal" }, PromoteProposal: func(ctx context.Context, p learning.ProposalPartition, id learning.ProposalID, v learning.ProposalVersion, _ bool) (learning.ProposalRecord, error) {
		return repo.ClaimPromotion(ctx, p, id, v)
	}}}
	if err := store.Save(ctx, source); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.rematerializeProposal(ctx, part, records[0]); err != nil {
		t.Fatal(err)
	}
	retained := session.CloneMessages(source.Conversation.Messages[1:])
	for i := range retained {
		retained[i].Replay = nil
	}
	if err := source.ReplaceHistoryAtBoundary(retained); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, source); err != nil {
		t.Fatal(err)
	}
	source, err = store.Load(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	in, backed, err := svc.rematerializeProposal(ctx, part, records[0])
	if err != nil || !backed {
		t.Fatalf("pruned manifest: backed=%v err=%v", backed, err)
	}
	recovered, err := learning.ResolveEvidenceHandle(in, "m:1")
	if err != nil || !reflect.DeepEqual(ref, recovered) {
		t.Fatalf("changed reference after prune: %+v %v", recovered, err)
	}
	preview, err := svc.GetLearningProposal(ctx, string(records[0].ID), "")
	if err != nil || !preview.Evidence[0].Available {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	if _, err := svc.DecideLearningProposal(ctx, string(records[0].ID), string(records[0].Version), "approve", "", ""); err != nil {
		t.Fatal(err)
	}
	persisted, found, err := repo.GetManifest(ctx, part, records[0].ID)
	if err != nil || !found || !reflect.DeepEqual(persisted, *frozen.Manifest) {
		t.Fatalf("stored manifest changed: %+v %v", persisted, err)
	}
	for name, messages := range map[string][]session.Message{"tampered": session.CloneMessages(retained), "deleted": session.CloneMessages(retained[:1])} {
		t.Run(name, func(t *testing.T) {
			if name == "tampered" {
				messages[1].ToolCalls[0].Name = "changed"
			}
			if err := source.ReplaceHistoryAtBoundary(messages); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(ctx, source); err != nil {
				t.Fatal(err)
			}
			if _, _, err := svc.rematerializeProposal(ctx, part, records[0]); !errors.Is(err, ErrFailedPrecondition) {
				t.Fatalf("changed selected source verified: %v", err)
			}
		})
	}
}
