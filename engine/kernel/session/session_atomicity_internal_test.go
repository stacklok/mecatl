package session

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history"
)

// detachedSessionState is a test oracle, not a snapshot: all locators, encoded
// records and decoded mutable DTO storage are copied, including admission counters.
func detachedSessionState(state sessionState) sessionState {
	v := &state.view
	v.records = slices.Clone(v.records)
	for i, r := range v.records {
		owned := *r
		owned.value = r.value.Clone()
		owned.input = r.input.Clone()
		v.records[i] = &owned
	}
	v.runs = slices.Clone(v.runs)
	v.inputs = slices.Clone(v.inputs)
	v.commits = slices.Clone(v.commits)
	return state
}

func populatedAtomicitySession(t *testing.T) *Session {
	t.Helper()
	s, _ := internalFixture(t)
	b, err := s.BeginCommit()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Create(); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		run, err := b.StartRun()
		if err != nil {
			t.Fatal(err)
		}
		for _, content := range []string{"first", "second"} {
			if _, err := b.AddInput(AddInputRequest{RunID: run, Content: []byte(content)}); err != nil {
				t.Fatal(err)
			}
		}
		if n == 0 {
			if err := b.Interrupt(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.SubmitCommit(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSessionPopulatedApplyRejectionPreservesPrivateState(t *testing.T) {
	s := populatedAtomicitySession(t)
	old := s.Snapshot()
	oldRun, oldInput, oldRecord := old.RunAt(1), old.RunAt(1).InputAt(0), old.RecordAt(2)
	before := detachedSessionState(s.state)
	b, err := s.BeginCommit()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddInput(AddInputRequest{RunID: old.ActiveRun(), Content: []byte("third")}); err != nil {
		t.Fatal(err)
	}
	if err := b.Interrupt(); err != nil {
		t.Fatal(err)
	}
	run, err := b.StartRun()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddInput(AddInputRequest{RunID: run, Content: []byte("successor")}); err != nil {
		t.Fatal(err)
	}
	records := slices.Clone(b.records)
	if err := b.Discard(); err != nil {
		t.Fatal(err)
	}
	good, err := s.prepare(records)
	if err != nil {
		t.Fatal(err)
	}
	bad := good.commit.Clone()
	wrongCloseout, err := NewRunInterruptedRecord(RunInterrupted{ID: old.ActiveRun()}, s.policy.PayloadLimits)
	if err != nil {
		t.Fatal(err)
	}
	bad.Records = append(bad.Records, wrongCloseout.Format())
	wire, err := codec.Encode(bad, s.policy.CommitLimits)
	if err != nil {
		t.Fatal(err)
	}
	accepted := history.Commit{Ref: history.CommitRef{Sequence: bad.Sequence, CID: contentid.Sum(wire)}, Commit: bad}
	if err := s.Apply(accepted); !errors.Is(err, ErrTransition) {
		t.Fatalf("invalid final Apply: %v", err)
	}
	if !reflect.DeepEqual(before, detachedSessionState(s.state)) {
		t.Fatal("rejected prefix changed counters, locators or accepted DTOs")
	}
	oldState := before
	oldState.view = old
	if !reflect.DeepEqual(before, detachedSessionState(oldState)) {
		t.Fatal("rejected prefix corrupted an old snapshot")
	}
	if oldRun.InputCount() != 2 || oldRun.RecordCount() != 3 || string(oldInput.Content()) != "first" || !oldRecord.Format().Equal(before.view.records[2].value) {
		t.Fatal("rejection changed independent child handles")
	}
	if err := s.Apply(history.Commit{Ref: good.ref, Commit: good.commit}); err != nil {
		t.Fatal(err)
	}
}

func TestDetachedSessionStateOracleDetectsInPlaceMutation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*sessionState)
	}{
		{"encoded bytes", func(s *sessionState) { s.view.records[2].value.Payload[0] ^= 1 }},
		{"decoded bytes", func(s *sessionState) { s.view.records[2].input.Content[0] ^= 1 }},
		{"record locator", func(s *sessionState) { s.view.records[2] = s.view.records[3] }},
		{"input locator", func(s *sessionState) { s.view.inputs[0] = s.view.inputs[1] }},
		{"run locator", func(s *sessionState) { s.view.runs[0].inputStart++ }},
		{"commit locator", func(s *sessionState) { s.view.commits[0].recordStart++ }},
		{"record admission", func(s *sessionState) { s.records++ }},
		{"payload admission", func(s *sessionState) { s.payloadBytes++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := populatedAtomicitySession(t)
			aliased := s.Snapshot()
			before := detachedSessionState(s.state)
			tc.mutate(&s.state)
			if reflect.DeepEqual(before, detachedSessionState(s.state)) {
				t.Fatal("detached oracle missed injected mutation")
			}
			if !reflect.DeepEqual(aliased, s.Snapshot()) {
				t.Fatal("negative control no longer reproduces the old snapshot-oracle weakness")
			}
			// Only this subtest's state is restored; no production hooks are used.
			s.state = detachedSessionState(before)
			if !reflect.DeepEqual(before, detachedSessionState(s.state)) {
				t.Fatal("test state restoration")
			}
		})
	}
}
