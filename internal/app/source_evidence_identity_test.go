package app

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/memattempt"
	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type legacyEvidenceReadView struct {
	port.EventLog
	events []session.Event
}

func (v legacyEvidenceReadView) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		for _, event := range v.events {
			if !yield(session.CloneEventIdentity(event), nil) {
				return
			}
		}
	}
}

func TestRecoveredLegacyAttemptRetainsIdentityAndVerifiesSource(t *testing.T) {
	ctx := context.Background()
	const run = "run_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	source, err := sessnap.Unmarshal([]byte(`{"id":"legacy-attempt-source","state":"completed","mode":"default","environment_ref":{"kind":"local","id":"/fixture","revision":"v1"},"messages":[{"role":"user","text":"remember this workflow"},{"role":"assistant","tool_calls":[{"ID":"provider-old-attempt","Name":"Read"}]},{"role":"tool","tool_result":{"CallID":"provider-old-attempt","Content":"result"}},{"role":"assistant","text":"done"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	store := memstore.New()
	if err := store.Save(ctx, source); err != nil {
		t.Fatal(err)
	}
	events := memstore.NewEventLog()
	rawEvents := []string{
		`{"Type":"user_prompt","Seq":1,"UserPrompt":{"Text":"remember this workflow"}}`,
		`{"Type":"turn.start","Seq":2}`,
		`{"Type":"tool.call","Seq":3,"ToolCall":{"ID":"provider-old-attempt","Name":"Read"}}`,
		`{"Type":"tool.result","Seq":4,"ToolResult":{"CallID":"provider-old-attempt","Content":"result"}}`,
		`{"Type":"turn.start","Seq":5}`,
		`{"Type":"message.delta","Seq":6,"Text":"done"}`,
		`{"Type":"result","Seq":7,"Result":{"Stop":"end_turn"}}`,
	}
	for _, raw := range rawEvents {
		event, err := eventcodec.Unmarshal([]byte(strings.Replace(raw, `{`, `{"RunID":"`+run+`",`, 1)))
		if err != nil {
			t.Fatal(err)
		}
		event.RunID = run
		if err := events.Append(ctx, source.ID, event); err != nil {
			t.Fatal(err)
		}
	}
	// The frozen provider-era run view is verification input, never executable
	// history. Its source identity/provenance is not rewritten after migration.
	trajectory := learning.NewTrajectory(source.ID, "", session.StopEndTurn, session.Usage{}, []session.Message{
		session.NewUserMessage("remember this workflow"),
		session.NewAssistantMessage("", []session.ToolCall{{ID: "provider-old-attempt", Name: "Read"}}),
		session.NewToolMessage(session.NewToolResult("provider-old-attempt", "result")),
		session.NewAssistantMessage("done", nil),
	})
	trajectory.Kind = source.Kind
	trajectory.Counters = session.Counters{Turns: 2, ToolCalls: 1}
	trajectory.Current = learning.MessageSpan{Start: 0, End: 4}
	trajectory.RunID = run
	input := learning.NewInput(trajectory, nil, nil, nil)
	const digest = "4ea923271ab45909c078ba85a52d524e558480b63eb76cf1b71314166309fbdc"
	if got, err := automaticTrajectoryDigest(input); err != nil || got != digest {
		t.Fatalf("frozen trajectory digest changed: %q %v", got, err)
	}
	prompt := learning.CurrentPromptBinding{Ordinal: 0, Digest: "ee8614fc32b59c2c7106b7671947085b401b94eaceeeb323aeae827e5fc8b4dd", Origin: learning.PromptOriginCurrentPrincipal}
	provenance, err := learning.NewAdmissionProvenance(learning.AdmissionHostRequested, learning.AttemptSource{SessionID: source.ID, RunID: run, CanonicalDigest: learning.CanonicalDigest(digest)}, prompt)
	if err != nil {
		t.Fatal(err)
	}
	partition, err := learning.DeriveAttemptPartition(reflectionPrincipal(nil))
	if err != nil {
		t.Fatal(err)
	}
	id, err := learning.DeterministicAttemptID(string(partition), provenance.Source)
	if err != nil {
		t.Fatal(err)
	}
	record := learning.AttemptRecord{ID: id, Provenance: provenance}
	if id != "attempt-b9ab00e0a814302e039eda743946c53c" {
		t.Fatalf("frozen attempt identity changed: %s", id)
	}
	before, _ := json.Marshal(record)
	loader := newLearningEvidenceLoader(store, events)
	verified, projection, failure, err := loader.loadForExecution(ctx, partition, record, "")
	if err != nil || failure != learning.FailureNone {
		t.Fatalf("recovered legacy attempt: failure=%q err=%v", failure, err)
	}
	recoveredDigest, err := automaticTrajectoryDigest(verified)
	if err != nil || recoveredDigest != digest {
		t.Fatalf("original digest changed: %q err=%v", recoveredDigest, err)
	}
	after, _ := json.Marshal(record)
	if string(before) != string(after) {
		t.Fatal("rewrote stored attempt identity")
	}
	projected, _ := json.Marshal(projection)
	if strings.Contains(string(projected), "provider-old-attempt") {
		t.Fatalf("provider identity leaked to recovered reflector: %s", projected)
	}
	if len(projection.Messages) != 4 || projection.Messages[2].ToolResult.Content != "result" {
		t.Fatalf("lost recovered evidence: %+v", projection)
	}
	// Both source readers retain the actual append-owned durable coordinates;
	// neither may turn a retained iterator index into an original evidence ordinal.
	var durable []session.Event
	for event, err := range events.Read(ctx, source.ID) {
		if err != nil {
			t.Fatal(err)
		}
		identity := event.LegacyIdentity()
		if identity == nil || identity.Coordinate == "" || identity.Ordinal != -1 {
			t.Fatalf("append lost source identity: %+v", identity)
		}
		durable = append(durable, event)
	}
	for record, err := range events.ReadAfter(ctx, source.ID, "", port.ReadOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		if record.Event.LegacyIdentity().Coordinate != string(record.Cursor) {
			t.Fatal("event and cursor readers disagree about durable position")
		}
	}
	projectedSource, err := eventcodec.Project(durable, source.Incarnation())
	if err != nil {
		t.Fatal(err)
	}
	persisted := append([]session.Event(nil), projectedSource.Events...)
	call := *persisted[2].ToolCall
	call.ID = "persisted-canonical-occurrence"
	persisted[2].ToolCall = &call
	result := *persisted[3].ToolResult
	result.CallID = call.ID
	persisted[3].ToolResult = &result
	for i, event := range persisted {
		encoded, err := eventcodec.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		persisted[i], err = eventcodec.Unmarshal(encoded)
		if err != nil {
			t.Fatal(err)
		}
	}
	unknown := append([]session.Event(nil), durable...)
	for _, index := range []int{2, 3} {
		// These legacy fixture rows have neither a source coordinate nor RunID.
		// Do not reappend them: a real append would correctly supply a coordinate.
		unknown[index], err = eventcodec.Unmarshal([]byte(rawEvents[index]))
		if err != nil {
			t.Fatal(err)
		}
	}
	unknownProjection, err := eventcodec.Project(unknown[2:4], source.Incarnation())
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range unknownProjection.Events {
		if event.ToolCall != nil || event.ToolResult != nil || event.LegacyIdentity().Ordinal != -1 || event.LegacyIdentity().Coordinate != "" {
			t.Fatal("unknown source acquired an invocation anchor")
		}
	}
	if _, err := learning.LegacyVerificationInput(trajectory, unknownProjection.Events); !errors.Is(err, learning.ErrInvalidEvidence) {
		t.Fatalf("unanchored recovery established bindings: %v", err)
	}
	for name, view := range map[string]struct {
		events    []session.Event
		available bool
	}{
		"event-only-durable-source": {durable, true},
		"persisted-binding-reload":  {persisted, true},
		"unknown-call-anchors":      {unknown, false},
		"pruned-run-start":          {durable[1:], false},
		"pruned-call":               {append(append([]session.Event(nil), durable[:2]...), durable[3:]...), false},
	} {
		t.Run(name, func(t *testing.T) {
			viewLoader := newLearningEvidenceLoader(store, legacyEvidenceReadView{events: view.events})
			recovered, display, failure, err := viewLoader.loadForExecution(ctx, partition, record, "")
			if !view.available {
				if err != nil || failure != learning.FailureEvidenceUnavailable {
					t.Fatalf("unproven source: failure=%q err=%v", failure, err)
				}
				return
			}
			if err != nil || failure != learning.FailureNone {
				t.Fatalf("durable source unavailable: failure=%q err=%v", failure, err)
			}
			if got, err := automaticTrajectoryDigest(recovered); err != nil || got != digest {
				t.Fatalf("frozen identity changed: %q %v", got, err)
			}
			bound, err := display.ReflectionInput()
			if err != nil || bound.Protocol != learning.ReflectionEvidenceLegacyV0 {
				t.Fatalf("lost private verified binding: %v", err)
			}
			for _, event := range view.events {
				if event.LegacyIdentity().Ordinal != -1 {
					t.Fatal("test supplied a fabricated original ordinal")
				}
			}
			if name == "persisted-binding-reload" && display.Events[2].Evidence.ToolCallID != call.ID {
				t.Fatalf("renamed persisted occurrence: %+v", display.Events[2].Evidence)
			}
		})
	}
	// Exercise the real restart worker through reflection and durable publication.
	attempts := memattempt.New(wallclock.Clock{})
	record, err = attempts.Create(ctx, partition, learning.AttemptCreate{ID: id, Provenance: provenance})
	if err != nil {
		t.Fatal(err)
	}
	proposals := memproposal.New()
	provider := mockllm.New(mockllm.TextTurn(`{"kind":"proposed","candidates":[{"kind":"operator_fact","key":"user/output","value":"concise","evidence":["m:1"]}]}`))
	cfg := Config{Model: "mock", LearningMode: learning.Review, LearningSensitivity: learning.Balanced, LearningAttemptTimeout: time.Second}
	if err := recoverAttempt(ctx, cfg, regForTest(provider, providerOpenAI, "mock"), store, events, attempts, proposals, catalogAssets{userModelStore: memmemory.New()}, appTestPlacementProvider{}, "legacy-local", learning.AttemptWork{Partition: partition, Record: record}); err != nil {
		t.Fatal(err)
	}
	published, found, err := attempts.Get(ctx, partition, id)
	if err != nil || !found || published.State != learning.AttemptCompleted || published.Outcome != learning.AttemptOutcomeSucceeded || published.ProposalID == "" {
		t.Fatalf("recovered publication: %+v found=%v err=%v", published, found, err)
	}
	proposal, found, err := proposals.Get(ctx, learning.ProposalPartition{Principal: reflectionPrincipal(nil)}, published.ProposalID)
	if err != nil || !found || proposal.Candidate.Evidence[0].ResolvedProtocol() != learning.ReflectionEvidenceLegacyV0 {
		t.Fatalf("published evidence: %+v found=%v err=%v", proposal, found, err)
	}
	if err := learning.ResolveEvidence(verified, proposal.Candidate.Evidence[0]); err != nil {
		t.Fatal(err)
	}

	// Unattached historical content cannot substitute for a verified run source.
	abandoned, err := sessnap.Unmarshal([]byte(`{"id":"legacy-attempt-source","state":"awaiting","mode":"default","environment_ref":{"kind":"local","id":"/fixture","revision":"v1"},"messages":[{"role":"assistant","tool_calls":[{"ID":"duplicate","Name":"Read"},{"ID":"duplicate","Name":"Read"}]}],"pending":{"ask_id":"ask","call":"duplicate","tool":"Read"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, pending := abandoned.PendingAsk(); pending || abandoned.State != session.StateIdle {
		t.Fatal("fixture retained continuation authority")
	}
	if err := store.Save(ctx, abandoned); err != nil {
		t.Fatal(err)
	}
	if _, failure := loader.Load(ctx, partition, record, ""); failure != learning.FailureNone {
		t.Fatal("independently proven prior run was blocked by abandoned continuation")
	}
	unproven := legacyEvidenceReadView{events: append(append([]session.Event(nil), durable[:2]...), durable[3:]...)}
	loader = newLearningEvidenceLoader(store, unproven)
	if _, failure := loader.Load(ctx, partition, record, ""); failure != learning.FailureEvidenceUnavailable {
		t.Fatal("unattached snapshot history substituted for pruned run evidence")
	}
	beforeMigrated, err := sessnap.Marshal(abandoned)
	if err != nil {
		t.Fatal(err)
	}
	freshAttempts := memattempt.New(wallclock.Clock{})
	queued, err := freshAttempts.Create(ctx, partition, learning.AttemptCreate{ID: id, Provenance: provenance})
	if err != nil {
		t.Fatal(err)
	}
	if err := recoverAttempt(ctx, cfg, nil, store, unproven, freshAttempts, proposals, catalogAssets{}, nil, "legacy-local", learning.AttemptWork{Partition: partition, Record: queued}); err != nil {
		t.Fatalf("unavailable recovery: %v", err)
	}
	unchanged, _, err := freshAttempts.Get(ctx, partition, id)
	if err != nil || unchanged.State != learning.AttemptFailed || unchanged.Outcome != learning.AttemptOutcomeFailed || unchanged.FailureCode != learning.FailureEvidenceUnavailable {
		t.Fatalf("unproven attempt executed: %+v %v", unchanged, err)
	}
	reloaded, err := store.Load(ctx, abandoned.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterMigrated, err := sessnap.Marshal(reloaded)
	if err != nil || string(beforeMigrated) != string(afterMigrated) {
		t.Fatalf("abandoned source mutated: %v", err)
	}
	foreign, _ := learning.DeriveAttemptPartition("foreign")
	if _, failure := loader.Load(ctx, foreign, record, ""); failure != learning.FailureEvidenceUnavailable {
		t.Fatal("owner isolation bypassed")
	}
	missing := newLearningEvidenceLoader(store, memstore.NewEventLog())
	if _, failure := missing.Load(ctx, partition, record, ""); failure != learning.FailureEvidenceUnavailable {
		t.Fatal("missing source recovered from digest")
	}
}
