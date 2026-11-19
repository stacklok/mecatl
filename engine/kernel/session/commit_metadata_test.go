package session

import (
	"errors"
	"iter"
	"math"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

func TestRecordMetadataZero(t *testing.T) {
	var record RecordView
	if stamp, ok := record.Timestamp(); ok || !stamp.IsZero() {
		t.Fatal("zero record has a timestamp")
	}
	if metadata, ok := record.Commit(); ok || metadata != (CommitMetadata{}) {
		t.Fatal("zero record has enclosing metadata")
	}
	var metadata CommitMetadata
	if metadata.Ref() != (history.CommitRef{}) || !metadata.Timestamp().IsZero() || metadata.InstanceID() != 0 {
		t.Fatal("zero metadata is not safe")
	}
}

func TestMetadataTimestampConversion(t *testing.T) {
	for _, nanos := range []int64{0, -1, math.MinInt64, math.MaxInt64} {
		metadata := CommitMetadata{timestamp: nanos, instanceID: 1}
		if got := metadata.Timestamp(); !got.Equal(time.Unix(0, nanos).UTC()) || got.Location() != time.UTC {
			t.Fatalf("commit timestamp %d = %v", nanos, got)
		}
		record := RecordView{record: &materializedRecord{value: format.Record{Timestamp: nanos}}}
		if got, present := record.Timestamp(); !present || !got.Equal(time.Unix(0, nanos).UTC()) || got.Location() != time.UTC {
			t.Fatalf("record timestamp %d = %v, %t", nanos, got, present)
		}
	}
}

func TestAcceptedRecordMetadataTraversalAndLifetime(t *testing.T) {
	h := newSessionHistory(t)
	scope := testHistoryID(t)
	now := int64(0)
	forbidden := false
	config := Config{Policy: sessionPolicy(), InstanceID: 7, Now: func() time.Time {
		if forbidden {
			t.Fatal("inspection or load sampled clock")
		}
		return time.Unix(0, now)
	}}
	s, err := New(h, scope, config)
	interactionCheck(t, err)
	b, err := s.BeginCommit()
	interactionCheck(t, err)
	interactionCheck(t, b.Create())
	now = -1
	run, err := b.StartRun()
	interactionCheck(t, err)
	early := captureCommit(t, b)
	now = 5
	_, err = b.AddUserInput(run, UserInput{Parts: []ContentPart{
		{Media: &Media{Kind: MediaKindDocument, Source: MediaSourceInline, InlineData: []byte{1, 2, 3}, MIMEType: "application/octet-stream"}},
		{Text: new("nonempty sibling")},
	}})
	interactionCheck(t, err)
	proposed := captureCommit(t, b)
	want := make([]format.Record, proposed.RecordCount())
	for i := range want {
		want[i] = proposed.RecordAt(i).MutableClone()
	}
	now = -9
	ref, err := s.SubmitCommit(t.Context(), b)
	interactionCheck(t, err)
	pinned := s.Snapshot()
	pinnedRun := pinned.RunAt(0)
	between, err := pinned.RecordsBetween(history.CommitRef{}, ref)
	interactionCheck(t, err)
	metadata, present := pinned.RecordAt(0).Commit()
	if !present || metadata.Ref() != ref || !metadata.Timestamp().Equal(time.Unix(0, -9).UTC()) || metadata.InstanceID() != 7 {
		t.Fatal("accepted metadata differs from prepared envelope")
	}
	checkRecord := func(record RecordView, i int, accepted bool) {
		t.Helper()
		stamp, present := record.Timestamp()
		if !present || !stamp.Equal(time.Unix(0, want[i].Timestamp).UTC()) || !record.MutableClone().Equal(want[i]) {
			t.Fatal("timestamp, presence or payload changed")
		}
		got, present := record.Commit()
		if present != accepted || (accepted && got != metadata) || (!accepted && got != (CommitMetadata{})) {
			t.Fatal("enclosing commit association changed")
		}
	}
	checkSequence := func(seq iter.Seq[RecordView], offset, count int, accepted bool) {
		t.Helper()
		i := 0
		for record := range seq {
			if i >= count {
				t.Fatal("pinned sequence grew")
			}
			checkRecord(record, offset+i, accepted)
			i++
		}
		if i != count {
			t.Fatal("sequence lost records")
		}
	}
	checkPinned := func() {
		t.Helper()
		checkSequence(early.Records(), 0, len(want)-1, false)
		checkSequence(proposed.Records(), 0, len(want), false)
		for i := range want {
			checkRecord(proposed.RecordAt(i), i, false)
			checkRecord(pinned.RecordAt(i), i, true)
		}
		checkSequence(pinned.Records(), 0, len(want), true)
		checkSequence(between, 0, len(want), true)
		checkSequence(pinnedRun.Records(), 1, len(want)-1, true)
		for i := 0; i < pinnedRun.RecordCount(); i++ {
			checkRecord(pinnedRun.RecordAt(i), i+1, true)
		}
	}
	forbidden = true
	checkPinned()
	// Explicit cloning preserves only the record envelope, and detaches bytes.
	clone := pinned.RecordAt(2).MutableClone()
	clone.Timestamp = 123
	clone.Payload[0] ^= 0xff
	checkPinned()
	input, _ := pinned.RecordAt(2).UserInput()
	media, _ := input.Parts().At(0).Media()
	copied := media.InlineData().Copy()
	copied[0] = 99
	if media.InlineData().At(0) != 1 {
		t.Fatal("selected leaf copy mutated accepted backing")
	}

	// A mixed CommitView retains accepted metadata but never adds it to proposals,
	// even after acceptance, discard or rejected submission.
	for _, outcome := range []string{"submit", "discard", "failure"} {
		forbidden = false
		b, err = s.BeginCommit()
		interactionCheck(t, err)
		now--
		interactionCheck(t, b.ReplaceRootInstructions(scope, RootInstructions{}))
		mixed := captureCommit(t, b)
		last := mixed.RecordCount() - 1
		proposal := mixed.RecordAt(last)
		stamp, _ := proposal.Timestamp()
		if got, ok := mixed.RecordAt(0).Commit(); !ok || got != metadata {
			t.Fatal("accepted CommitView prefix lost metadata")
		}
		switch outcome {
		case "submit":
			_, err = s.SubmitCommit(t.Context(), b)
			interactionCheck(t, err)
			fresh := s.Snapshot().RecordAt(last)
			if got, ok := fresh.Commit(); !ok || got.Ref() != s.Snapshot().Head() || !got.Timestamp().Equal(time.Unix(0, now).UTC()) {
				t.Fatal("fresh accepted record lacks successor metadata")
			}
		case "discard":
			interactionCheck(t, b.Discard())
		case "failure":
			// Violate the fixed policy only to force deterministic preflight failure.
			policy := s.policy
			s.policy.CommitLimits.MaxBytes = 1
			_, err = s.SubmitCommit(t.Context(), b)
			s.policy = policy
			if err == nil {
				t.Fatal("preflight unexpectedly accepted")
			}
		}
		forbidden = true
		for record := range mixed.Records() {
			if record == proposal {
				if got, ok := record.Commit(); ok || got != (CommitMetadata{}) {
					t.Fatal("retained proposal acquired enclosing commit")
				}
				if got, ok := record.Timestamp(); !ok || got != stamp {
					t.Fatal("retained proposal timestamp changed")
				}
			}
		}
		checkPinned()
	}

	// Force both record and commit-index reallocation; old handles never point
	// into either growable index and keep their original answers.
	forbidden = false
	for range 20 {
		b, err = s.BeginCommit()
		interactionCheck(t, err)
		now--
		interactionCheck(t, b.ReplaceRootInstructions(scope, RootInstructions{}))
		_, err = s.SubmitCommit(t.Context(), b)
		interactionCheck(t, err)
	}
	forbidden = true
	loaded, err := Load(t.Context(), h, scope, Config{Policy: sessionPolicy(), InstanceID: 99, Now: config.Now})
	interactionCheck(t, err)
	checkPinned()
	for _, snapshot := range []Snapshot{s.Snapshot(), loaded.Snapshot()} {
		for i := range want {
			checkRecord(snapshot.RecordAt(i), i, true)
			if i > 0 {
				checkRecord(snapshot.RunAt(0).RecordAt(i-1), i, true)
			}
		}
		prefix, err := snapshot.RecordsBetween(history.CommitRef{}, ref)
		interactionCheck(t, err)
		checkSequence(prefix, 0, len(want), true)
		i := 0
		for record := range snapshot.Records() {
			if i == len(want) {
				break
			}
			checkRecord(record, i, true)
			i++
		}
		i = 1
		for record := range snapshot.RunAt(0).Records() {
			if i == len(want) {
				break
			}
			checkRecord(record, i, true)
			i++
		}
		// The complete grouping index must retain every original envelope value.
		for _, entry := range snapshot.commits {
			for _, record := range snapshot.records[entry.recordStart : entry.recordStart+entry.recordCount] {
				if record.commit != entry.metadata {
					t.Fatal("records and grouping do not share one stable metadata backing")
				}
			}
			seq, err := snapshot.RecordsBetween(history.CommitRef{}, entry.ref)
			interactionCheck(t, err)
			count := 0
			for range seq {
				count++
			}
			if count != entry.recordStart+entry.recordCount || entry.metadata.Ref() != entry.ref || entry.metadata.InstanceID() != 7 {
				t.Fatal("commit grouping lost metadata or bounds")
			}
		}
	}
}

func TestRecordMetadataUncertainSubmission(t *testing.T) {
	backend := &timestampBackend{Backend: mem.New(), loseAck: true}
	h := newLifecycleHistory(t, backend)
	s, err := New(h, testHistoryID(t), testConfigFor(sessionPolicy()))
	interactionCheck(t, err)
	b, err := s.BeginCommit()
	interactionCheck(t, err)
	interactionCheck(t, b.Create())
	proposed := captureCommit(t, b).RecordAt(0)
	if _, err := s.SubmitCommit(t.Context(), b); !errors.Is(err, ErrAcceptanceUnknown) {
		t.Fatalf("expected uncertain acceptance: %v", err)
	}
	if _, ok := proposed.Commit(); ok || s.Snapshot().RecordCount() != 0 {
		t.Fatal("uncertainty published local metadata")
	}
	ref, err := s.SubmitCommit(t.Context(), b)
	interactionCheck(t, err)
	if _, ok := proposed.Commit(); ok {
		t.Fatal("retry mutated retained proposal")
	}
	got, ok := s.Snapshot().RecordAt(0).Commit()
	if !ok || got.Ref() != ref {
		t.Fatal("retry did not publish accepted metadata")
	}
}
