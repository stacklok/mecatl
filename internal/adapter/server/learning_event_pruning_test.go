package server

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestFrozenLegacyEventCannotRebindToLaterIdenticalRun(t *testing.T) {
	ctx := context.Background()
	source := session.New("old-event-source", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/fixture", Revision: "v1"}, session.Limits{}, time.Unix(1, 0))
	store := memstore.New()
	if err := store.Save(ctx, source); err != nil {
		t.Fatal(err)
	}
	// Frozen provider-era event identity. RunID and durable position were not hash inputs.
	const frozen = `{"protocol":"reflection-evidence/v1","trajectory":{"SessionID":"old-event-source"},"events":[{"Type":"tool.call","Seq":77,"Turn":1,"ToolCall":{"id":"provider-event-id","name":"Read"}}],"manifest":{"protocol":"reflection-evidence/v1","source":{"domain":"mecatl/reflection-evidence/source/v1","value":"old-event-source"},"entries":[{"handle":"e:0","locator":"event","event_sequence":0,"digest":"0cd818b05187cd1e09419a3339c439dd2589dafe84bd373d260327ee6e8a7dc2"}],"digest":"18293244bf89889b1f7f8910b6308897838a725879da602baf0bc05f8aabe186"}}`
	var input learning.Input
	if err := json.Unmarshal([]byte(frozen), &input); err != nil {
		t.Fatal(err)
	}
	ref, err := learning.EventEvidenceRef(input, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := learning.NewCandidate(input, learning.Candidate{Kind: learning.CandidateOperatorFact, Key: "user/output", Value: "concise", Evidence: []learning.EvidenceRef{ref}})
	if err != nil {
		t.Fatal(err)
	}
	repo := memproposal.New()
	part := learning.ProposalPartition{Principal: "principal"}
	records, err := repo.StageBatchManifest(ctx, part, *input.Manifest, []learning.Candidate{candidate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	decode := func(raw, coordinate string, ordinal int) session.Event {
		t.Helper()
		event, err := eventcodec.Unmarshal([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		event = event.WithLegacySource("fixture-log", coordinate)
		p := event.LegacyIdentity()
		p.Incarnation, p.Ordinal = source.Incarnation(), ordinal
		return event.WithLegacyIdentity(*p)
	}
	original := decode(`{"Type":"tool.call","RunID":"original-run","Seq":77,"Turn":1,"ToolCall":{"ID":"provider-event-id","Name":"Read"}}`, "byte:0", 0)
	later := decode(`{"Type":"tool.call","RunID":"later-run","Seq":77,"Turn":1,"ToolCall":{"ID":"provider-event-id","Name":"Read"}}`, "byte:400", -1)
	full := memstore.NewEventLog()
	for _, event := range []session.Event{original, later} {
		if err := full.Append(ctx, source.ID, event); err != nil {
			t.Fatal(err)
		}
	}
	promotions := 0
	svc := &Service{cfg: Config{Store: store, EventLog: full, Proposals: repo, ProposalManifest: repo.GetManifest, ProposalPrincipal: func(*session.Principal) string { return "principal" }, PromoteProposal: func(context.Context, learning.ProposalPartition, learning.ProposalID, learning.ProposalVersion, bool) (learning.ProposalRecord, error) {
		promotions++
		return records[0], nil
	}}}
	if _, _, err := svc.rematerializeProposal(ctx, part, records[0]); err != nil {
		t.Fatalf("genuine original unavailable: %v", err)
	}
	if detail, err := svc.GetLearningProposal(ctx, string(records[0].ID), ""); err != nil || !detail.Evidence[0].Available {
		t.Fatalf("original preview: %+v %v", detail, err)
	}
	// Direct v0 verification must use the same original-coordinate rule.
	v0 := ref
	v0.Protocol, v0.AggregateDigest = learning.ReflectionEvidenceLegacyV0, ""
	v0.EventSeq = new(int64(77))
	if _, err := learning.VerifyEvidenceSource(learning.Trajectory{SessionID: source.ID}, []session.Event{original}, source.Incarnation(), v0); err != nil {
		t.Fatal(err)
	}
	for name, sourceEvents := range map[string][]session.Event{
		"unknown-before": {later, original},
		"unknown-after":  {original, later},
	} {
		t.Run(name, func(t *testing.T) {
			log := memstore.NewEventLog()
			for _, event := range sourceEvents {
				if err := log.Append(ctx, source.ID, event); err != nil {
					t.Fatal(err)
				}
			}
			svc.cfg.EventLog = log
			if _, err := learning.VerifyEvidenceSource(learning.Trajectory{SessionID: source.ID}, sourceEvents, source.Incarnation(), v0); err != nil {
				t.Fatalf("unrelated unknown prevented verification: %v", err)
			}
			verified, _, err := svc.rematerializeProposal(ctx, part, records[0])
			if err != nil {
				t.Fatal(err)
			}
			got, err := learning.EventEvidenceRef(verified, 0, "")
			if err != nil || !reflect.DeepEqual(got, ref) {
				t.Fatalf("frozen reference changed: %+v %v", got, err)
			}
			if detail, err := svc.GetLearningProposal(ctx, string(records[0].ID), ""); err != nil || !detail.Evidence[0].Available {
				t.Fatalf("preview lost original: %+v %v", detail, err)
			}
			if _, status := svc.learningEvidenceCitation(ctx, v0); status != "available" {
				t.Fatalf("direct preview lost original: %s", status)
			}
			before := promotions
			if _, err := svc.DecideLearningProposal(ctx, string(records[0].ID), string(records[0].Version), "approve", "", ""); err != nil || promotions != before+1 {
				t.Fatalf("original approval: %v promotions=%d", err, promotions)
			}
		})
	}
	promotions = 0
	for _, ordinal := range []int{-1, 1} {
		t.Run(map[int]string{-1: "unknown-original-ordinal", 1: "proven-later-ordinal"}[ordinal], func(t *testing.T) {
			p := later.LegacyIdentity()
			p.Ordinal = ordinal
			replacement := later.WithLegacyIdentity(*p)
			if _, err := learning.VerifyEvidenceSource(learning.Trajectory{SessionID: source.ID}, []session.Event{replacement}, source.Incarnation(), v0); !errors.Is(err, learning.ErrInvalidEvidence) {
				t.Fatalf("replacement verified: %v", err)
			}
			pruned := memstore.NewEventLog()
			if err := pruned.Append(ctx, source.ID, replacement); err != nil {
				t.Fatal(err)
			}
			svc.cfg.EventLog = pruned
			if _, _, err := svc.rematerializeProposal(ctx, part, records[0]); !errors.Is(err, ErrFailedPrecondition) {
				t.Fatalf("replacement rematerialized: %v", err)
			}
			if _, err := svc.GetLearningProposal(ctx, string(records[0].ID), ""); !errors.Is(err, ErrFailedPrecondition) {
				t.Fatalf("replacement preview accepted: %v", err)
			}
			if _, status := svc.learningEvidenceCitation(ctx, v0); status == "available" {
				t.Fatal("replacement direct preview available")
			}
			if _, err := svc.DecideLearningProposal(ctx, string(records[0].ID), string(records[0].Version), "approve", "", ""); !errors.Is(err, ErrFailedPrecondition) {
				t.Fatalf("replacement approved: %v", err)
			}
			current, _, err := repo.Get(ctx, part, records[0].ID)
			if err != nil || current.Version != records[0].Version || promotions != 0 {
				t.Fatalf("unavailable source changed proposal: %+v %v promotions=%d", current, err, promotions)
			}
		})
	}
	foreign := later.LegacyIdentity()
	foreign.Incarnation = session.NewIncarnationID()
	for name, invalidEvents := range map[string][]session.Event{
		"duplicate-proven-coordinate": {original, original},
		"foreign-unknown-provenance":  {original, later.WithLegacyIdentity(*foreign)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := learning.VerifyEvidenceSource(learning.Trajectory{SessionID: source.ID}, invalidEvents, source.Incarnation(), v0); !errors.Is(err, learning.ErrInvalidEvidence) {
				t.Fatalf("invalid provenance verified: %v", err)
			}
			log := memstore.NewEventLog()
			for _, event := range invalidEvents {
				if err := log.Append(ctx, source.ID, event); err != nil {
					t.Fatal(err)
				}
			}
			svc.cfg.EventLog = log
			if _, _, err := svc.rematerializeProposal(ctx, part, records[0]); !errors.Is(err, ErrFailedPrecondition) {
				t.Fatalf("invalid provenance rematerialized: %v", err)
			}
			if _, status := svc.learningEvidenceCitation(ctx, v0); status == "available" {
				t.Fatal("invalid provenance previewed")
			}
			if _, err := svc.DecideLearningProposal(ctx, string(records[0].ID), string(records[0].Version), "approve", "", ""); !errors.Is(err, ErrFailedPrecondition) || promotions != 0 {
				t.Fatalf("invalid provenance approved: %v", err)
			}
		})
	}
	// Retaining the genuine original at its proven coordinate still works after pruning.
	surviving := memstore.NewEventLog()
	if err := surviving.Append(ctx, source.ID, original); err != nil {
		t.Fatal(err)
	}
	svc.cfg.EventLog = surviving
	if _, _, err := svc.rematerializeProposal(ctx, part, records[0]); err != nil {
		t.Fatalf("surviving original unavailable: %v", err)
	}
}
