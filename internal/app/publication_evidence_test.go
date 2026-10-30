package app

import (
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/skillfs"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/reflectionstore"
)

func TestProcedurePublicationRejectsExcludedEvidenceBeforeEffects(t *testing.T) {
	for _, kind := range []string{"excluded", "missing-protocol", "v1", "mixed-protocol", "missing-source"} {
		t.Run(kind, func(t *testing.T) {
			input, outcome, digest := reflectionOutcomeFixture(t, learning.CandidateProcedure)
			sources := currentTestSourceStore(t, input.Trajectory)
			candidate := outcome.Candidates[0]
			switch kind {
			case "excluded":
				source, err := sources.Load(t.Context(), input.Trajectory.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				source.ExcludeFromLearning()
				if err := sources.Save(t.Context(), source); err != nil {
					t.Fatal(err)
				}
			case "missing-protocol":
				candidate.Evidence[0].Protocol = ""
			case "v1":
				candidate.Evidence[0].Protocol = "reflection-evidence/v1"
			case "mixed-protocol":
				ref := candidate.Evidence[0]
				ref.Protocol = "reflection-evidence/v1"
				ref.Ordinal++
				candidate.Evidence = append(candidate.Evidence, ref)
			case "missing-source":
				candidate.Evidence[0].SessionID = "missing"
			}
			proposals, skills := memproposal.New(), memskill.New()
			part := learning.ProposalPartition{Principal: "principal", Project: input.Trajectory.Workspace}
			records, err := proposals.StageBatch(t.Context(), part, digest, []learning.Candidate{candidate}, nil)
			if err != nil {
				t.Fatal(err)
			}
			evaluator := &countingPublicationEvaluator{}
			catalog := skillfs.NewAtomicCatalog(nil, nil, nil)
			processor := buildProcedureProcessor(Config{LearningMode: learning.Auto, learningSourceStore: sources, SkillEvaluator: evaluator}, catalogAssets{reflectionRepository: proposals, learnedSkills: skills, liveSkills: catalog, skillOwner: "reflection"})
			if err := processor(t.Context(), records[0], learning.Auto); !errors.Is(err, learning.ErrInvalidEvidence) {
				t.Fatalf("excluded evidence accepted: %v", err)
			}
			skillPart := learning.SkillPartition(part)
			page, err := skills.List(t.Context(), skillPart, learning.SkillList{})
			if err != nil || len(page.Versions)+len(catalog.View(skillPart).Metas)+evaluator.calls != 0 {
				t.Fatalf("excluded evidence caused effects: drafts=%d evaluator=%d err=%v", len(page.Versions), evaluator.calls, err)
			}
		})
	}
}

// secondRunPublicationInput supplies the same exact-run input as durable reflection,
// while the authoritative snapshot and event log contain both current runs.
func secondRunPublicationInput(t *testing.T) (*memstore.Store, learning.Input, []session.Event) {
	t.Helper()
	owner := &session.Principal{Issuer: "issuer", Subject: "owner", GrantType: session.GrantTypeUser}
	source := session.New("two-run-source", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := source.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	store, log := memstore.New(), memstore.NewEventLog()
	var runEvents []session.Event
	for i, run := range []string{"run_aaaaaaaaaaaaaaaaaaaaaaaaaa", "run_bbbbbbbbbbbbbbbbbbbbbbbbbb"} {
		if i > 0 {
			if err := source.Reopen(); err != nil {
				t.Fatal(err)
			}
		}
		source.BeginRun(run)
		prompt, answer := "First run prompt", "First run result"
		if i > 0 {
			prompt, answer = "Remember that I prefer concise Go examples", "Second run result"
		}
		if err := source.RecordUserPrompt(prompt, nil); err != nil {
			t.Fatal(err)
		}
		if err := source.BeginTurn(); err != nil {
			t.Fatal(err)
		}
		if err := source.RecordAssistant(session.NewAssistantMessage(answer, nil)); err != nil {
			t.Fatal(err)
		}
		if err := source.Complete(); err != nil {
			t.Fatal(err)
		}
		runEvents = []session.Event{
			{Type: session.EvUserPrompt, RunID: run, Seq: 1, UserPrompt: &session.UserPromptPayload{Text: prompt}},
			{Type: session.EvTurnStart, RunID: run, Seq: 2},
			{Type: session.EvMessageDelta, RunID: run, Seq: 7, Text: answer},
			{Type: session.EvResult, RunID: run, Seq: 9, Result: &session.ResultPayload{Stop: session.StopEndTurn}},
		}
		for _, event := range runEvents {
			if err := log.Append(t.Context(), source.ID, event); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.Save(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	folded, err := eventsource.Fold(eventsource.SessionMeta{ID: source.ID, Incarnation: source.Incarnation(), LearningEligible: true, Owner: owner, Mode: source.Mode, EnvironmentRef: source.EnvironmentRef, Kind: source.Kind, CreatedAt: source.CreatedAt}, func(yield func(session.Event, error) bool) {
		for _, event := range runEvents {
			if !yield(event, nil) {
				return
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	trajectory := learning.NewSessionTrajectory(folded, "/workspace", session.StopEndTurn, session.Usage{})
	trajectory.RunID = source.RunID()
	trajectory.Kind = source.Kind
	trajectory.Counters = folded.Counters
	trajectory.Current = learning.MessageSpan{Start: 0, End: len(trajectory.Messages)}
	input := learning.NewInput(trajectory, runEvents, nil, nil)
	digest, err := automaticTrajectoryDigest(input)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := currentPromptBinding(input, learning.AdmissionHostRequested)
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := learning.NewAdmissionProvenance(learning.AdmissionHostRequested, learning.AttemptSource{SessionID: source.ID, RunID: learning.DurableRunID(source.RunID()), CanonicalDigest: learning.CanonicalDigest(digest)}, prompt)
	if err != nil {
		t.Fatal(err)
	}
	partition, err := learning.DeriveAttemptPartition(reflectionPrincipal(owner))
	if err != nil {
		t.Fatal(err)
	}
	input, _, failure, err := newLearningEvidenceLoader(store, log).loadForExecution(t.Context(), partition, learning.AttemptRecord{Provenance: provenance}, "/workspace")
	if err != nil || failure != learning.FailureNone {
		t.Fatalf("second-run evidence: failure=%s err=%v", failure, err)
	}
	if len(source.Conversation.Messages) != 4 || len(input.Trajectory.Messages) != 2 || input.Trajectory.Messages[0].Text == source.Conversation.Messages[0].Text {
		t.Fatal("fixture did not retain distinct session-global and run-local coordinates")
	}
	input.Trajectory.Principal = owner.Clone()
	return store, input, runEvents
}

func TestCurrentSecondRunPublication(t *testing.T) {
	for _, kind := range []learning.CandidateKind{learning.CandidateOperatorFact, learning.CandidateProcedure} {
		for _, materialized := range []bool{false, true} {
			name := string(kind) + "/unmaterialized"
			if materialized {
				name = string(kind) + "/materialized"
			}
			t.Run(name, func(t *testing.T) {
				sources, input, events := secondRunPublicationInput(t)
				if materialized {
					material, err := learning.MaterializeEvidence(t.Context(), learning.MaterializationRequest{Trajectory: input.Trajectory, Events: events, Explicit: true})
					if err != nil || material.Disposition != learning.MaterializationSelected {
						t.Fatalf("materialize: %+v %v", material, err)
					}
					trajectory := input.Trajectory
					input = material.Input
					input.Trajectory.Workspace = trajectory.Workspace
					input.Trajectory.Principal = trajectory.Principal
					input.Trajectory.RunID = trajectory.RunID
					input.Trajectory.Kind = trajectory.Kind
					input.Trajectory.Counters = trajectory.Counters
					input.Trajectory.Current = learning.MessageSpan{Start: 0, End: len(input.Trajectory.Messages)}
				}
				ref, err := learning.MessageEvidenceRef(input, 0, "")
				if err != nil {
					t.Fatal(err)
				}
				eventRef, err := learning.EventEvidenceRef(input, len(input.Events)-1, "")
				if err != nil {
					t.Fatal(err)
				}
				_, outcome, _ := reflectionOutcomeFixture(t, kind)
				outcome.Candidates[0].Evidence = []learning.EvidenceRef{ref, eventRef}
				if err := learning.ValidateCandidate(input, outcome.Candidates[0]); err != nil {
					t.Fatal(err)
				}
				digest, err := reflectionInputDigest(input)
				if err != nil {
					t.Fatal(err)
				}
				dir := t.TempDir()
				proposals, err := reflectionstore.New(dir)
				if err != nil {
					t.Fatal(err)
				}
				skills, memory := memskill.New(), memmemory.New()
				catalog := skillfs.NewAtomicCatalog(nil, nil, nil)
				evaluator := &countingPublicationEvaluator{}
				principal := reflectionPrincipal(input.Trajectory.Principal)
				cfg := Config{LearningMode: learning.Auto, Workspace: input.Trajectory.Workspace, TrustProject: true, learningSourceStore: sources, SkillEvaluator: evaluator}
				processor := buildProcedureProcessor(cfg, catalogAssets{reflectionRepository: proposals, learnedSkills: skills, liveSkills: catalog, skillOwner: "reflection"})
				receipt, err := processReflectionOutcome(t.Context(), proposals, memory, memory, principal, input, digest, outcome, learning.DetectSignals(input), learning.Auto, true, input.Trajectory.Workspace, processor)
				if err != nil || receipt.Staged != 1 {
					t.Fatalf("second-run publication: %+v %v", receipt, err)
				}
				part := learning.ProposalPartition{Principal: principal}
				if kind == learning.CandidateProcedure {
					part.Project = input.Trajectory.Workspace
					skillPart := learning.SkillPartition(part)
					page, err := skills.List(t.Context(), skillPart, learning.SkillList{})
					if err != nil || len(page.Versions) != 1 || page.Versions[0].State != learning.SkillActive || len(catalog.View(skillPart).Metas) != 1 || evaluator.calls != 1 {
						t.Fatalf("second-run procedure not published: %+v evaluator=%d err=%v", page, evaluator.calls, err)
					}
				} else if _, found, err := memory.Inspect(t.Context(), outcome.Candidates[0].Key); err != nil || !found || receipt.Promoted != 1 {
					t.Fatalf("second-run fact not promoted: %+v found=%v err=%v", receipt, found, err)
				}
				restarted, err := reflectionstore.New(dir)
				if err != nil {
					t.Fatal(err)
				}
				record, found, err := restarted.Get(t.Context(), part, receipt.ProposalID)
				if err != nil || !found || !proposalSourcesEligible(t.Context(), sources, record) {
					t.Fatalf("second-run eligibility lost after restart: %+v found=%v err=%v", record, found, err)
				}
			})
		}
	}
}
