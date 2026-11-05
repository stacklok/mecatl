package session

import (
	"context"
	"testing"
)

var snapshotSink Snapshot

// appendCopyCost counts the old elements copied on each observed reallocation.
// It also rejects hidden prefix copies even if they keep the same capacity.
func appendCopyCost[T any](t *testing.T, before, after []T) int {
	t.Helper()
	if len(after) < len(before) {
		t.Fatal("accepted backing shrank")
	}
	if len(before) == 0 {
		return 0
	}
	if &before[0] == &after[0] {
		return 0
	}
	if len(after) <= cap(before) {
		t.Fatal("accepted prefix copied despite spare capacity")
	}
	return len(before)
}

func TestSnapshotConstantCaptureAndAmortizedAppendStorage(t *testing.T) {
	s, _ := internalFixture(t)
	b, err := s.BeginCommit()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Create(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitCommit(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	var copies [4]int
	var growth, spare [4]bool
	for n := 0; n < 1024; n++ {
		// Three small accepted commits per run exercise every array independently.
		for phase := 0; phase < 3; phase++ {
			before := s.Snapshot()
			b, err := s.BeginCommit()
			if err != nil {
				t.Fatal(err)
			}
			switch phase {
			case 0:
				if _, err := b.StartRun(); err != nil {
					t.Fatal(err)
				}
			case 1:
				if _, err := b.AddInput(AddInputRequest{RunID: before.ActiveRun(), Messages: internalByteMessages([]byte{0xff, 0, 'x'})}); err != nil {
					t.Fatal(err)
				}
			case 2:
				if err := b.Interrupt(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.SubmitCommit(context.Background(), b); err != nil {
				t.Fatal(err)
			}
			after := s.Snapshot()
			costs := [4]int{appendCopyCost(t, before.records, after.records), appendCopyCost(t, before.runs, after.runs), appendCopyCost(t, before.inputs, after.inputs), appendCopyCost(t, before.commits, after.commits)}
			grew := [4]bool{len(after.records) > len(before.records), len(after.runs) > len(before.runs), len(after.inputs) > len(before.inputs), len(after.commits) > len(before.commits)}
			for i, c := range costs {
				copies[i] += c
				if c > 0 {
					growth[i] = true
				} else if grew[i] {
					spare[i] = true
				}
			}
			// The immutable facts themselves must never be decoded/copied again.
			if before.records[0] != after.records[0] || before.records[len(before.records)-1] != after.records[len(before.records)-1] {
				t.Fatal("accepted record identity changed")
			}
			if n%128 == 0 {
				if allocations := testing.AllocsPerRun(100, func() { snapshotSink = s.Snapshot() }); allocations != 0 {
					t.Fatalf("capture allocated: %g", allocations)
				}
				capture := snapshotSink
				if capture.head != after.head || len(capture.records) != len(after.records) || &capture.records[0] != &after.records[0] || len(capture.commits) != len(after.commits) || &capture.commits[0] != &after.commits[0] {
					t.Fatal("capture copied or changed bounds")
				}
				if len(after.runs) > 0 && (&capture.runs[0] != &after.runs[0] || len(capture.runs) != len(after.runs)) {
					t.Fatal("capture copied runs")
				}
				if len(after.inputs) > 0 && (&capture.inputs[0] != &after.inputs[0] || len(capture.inputs) != len(after.inputs)) {
					t.Fatal("capture copied inputs")
				}
			}
		}
	}
	final := s.Snapshot()
	lengths := [4]int{len(final.records), len(final.runs), len(final.inputs), len(final.commits)}
	for i, total := range copies {
		// A loose linear upper bound, not an assertion about Go's growth factors.
		if total > 16*lengths[i] || !growth[i] || !spare[i] {
			t.Fatalf("array %d: copied=%d length=%d growth=%t spare=%t", i, total, lengths[i], growth[i], spare[i])
		}
	}
}

func TestSnapshotChildBoundsAcrossSpareCapacityAndGrowth(t *testing.T) {
	s, _ := internalFixture(t)
	b, err := s.BeginCommit()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Create(); err != nil {
		t.Fatal(err)
	}
	run, err := b.StartRun()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddInput(AddInputRequest{RunID: run, Messages: internalByteMessages([]byte("first"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitCommit(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	var sawSpare, sawGrowth bool
	for n := 0; n < 128; n++ {
		old := s.Snapshot()
		oldRun := old.RunAt(0)
		oldInput := oldRun.InputAt(0)
		oldRecord := oldRun.RecordAt(1)
		before := old.RecordCount()
		b, err := s.BeginCommit()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := b.AddInput(AddInputRequest{RunID: run, Messages: internalByteMessages([]byte("later"))}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SubmitCommit(context.Background(), b); err != nil {
			t.Fatal(err)
		}
		next := s.Snapshot()
		if &old.records[0] == &next.records[0] {
			sawSpare = true
		} else {
			sawGrowth = true
		}
		if old.RecordCount() != before || oldRun.RecordCount() != before-1 || oldRun.InputCount() != before-2 || oldInput.ID().Ordinal != 1 || string(internalInputBytes(oldInput)) != "first" {
			t.Fatal("old snapshot/run/input changed")
		}
		input, ok := oldRecord.UserInput()
		if !ok || string(internalInputBytes(input)) != "first" || oldRecord.record != next.records[2] {
			t.Fatal("old record handle changed")
		}
	}
	if !sawSpare || !sawGrowth {
		t.Fatal("fixture did not exercise both storage paths")
	}
}
