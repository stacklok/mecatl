package session

import (
	"bytes"
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

// closeoutEnvelopeBackend counts all attempts, not just successful acceptance.
type closeoutEnvelopeBackend struct {
	history.Backend
	appends int
}

func (b *closeoutEnvelopeBackend) Append(ctx context.Context, id history.HistoryID, expected history.CommitRef, c history.ByteCommit) (history.Observation, error) {
	b.appends++
	return b.Backend.Append(ctx, id, expected, c)
}

// closeoutWidthPrefix constructs a real accepted chain through sequence 22, idle.
// The extra empty input makes start/interruption parity put the next start at 23.
func closeoutWidthPrefix(t *testing.T) (*Session, *closeoutEnvelopeBackend) {
	t.Helper()
	fixture, limits := internalFixture(t)
	backend := &closeoutEnvelopeBackend{Backend: mem.New()}
	h, err := history.New(backend, history.Policy{CommitLimits: limits, RetryWindow: 16, MaxPageBytes: limits.MaxBytes, MaxPageCommits: 2})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(h, fixture.Snapshot().HistoryID(), fixture.policy)
	if err != nil {
		t.Fatal(err)
	}
	for sequence := 1; sequence <= 22; sequence++ {
		b, err := s.BeginCommit()
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case sequence == 1:
			err = b.Create()
		case sequence == 3:
			_, err = b.AddUserInput(AddUserInputRequest{RunID: s.Snapshot().ActiveRun()})
		case s.Snapshot().ActiveRun() == (RunID{}):
			_, err = b.StartRun()
		default:
			err = b.Interrupt()
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SubmitCommit(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	if s.Snapshot().Head().Sequence != 22 || s.Snapshot().ActiveRun() != (RunID{}) {
		t.Fatal("width fixture endpoint")
	}
	return s, backend
}

func TestSessionFutureCloseoutEnvelopeSequenceWidth(t *testing.T) {
	for _, exact := range []bool{false, true} {
		for _, path := range []string{"prepare", "submit", "apply"} {
			t.Run(map[bool]string{false: "one byte short", true: "exact fit"}[exact]+"/"+path, func(t *testing.T) {
				prefix, backend := closeoutWidthPrefix(t)
				id := prefix.Snapshot().HistoryID()
				run := RunID{HistoryID: id, Ordinal: uint64(prefix.Snapshot().RunCount()) + 1}
				start, err := NewRunStartedRecord(RunStarted{ID: run}, prefix.policy.PayloadLimits)
				if err != nil {
					t.Fatal(err)
				}
				candidate, err := prefix.prepare([]Record{start})
				if err != nil {
					t.Fatal(err)
				}
				interruption, err := NewRunInterruptedRecord(RunInterrupted{ID: run}, prefix.policy.PayloadLimits)
				if err != nil {
					t.Fatal(err)
				}
				closeout := format.Commit{FormatVersion: 1, Sequence: 24, Parent: new(candidate.ref.CID), Records: []format.Record{interruption.Format()}}
				wire, err := codec.Encode(closeout, prefix.policy.CommitLimits)
				if err != nil {
					t.Fatal(err)
				}
				// MaxBytes admits conservative codec accounting, not raw wire length.
				// Measure that actual admission cost rather than guessing headers.
				budget, err := codec.NewBudget(prefix.policy.CommitLimits)
				if err != nil {
					t.Fatal(err)
				}
				if err := closeout.CheckCBOR(&budget, 0, math.MaxInt); err != nil {
					t.Fatal(err)
				}
				limit := budget.EstimatedBytes()
				if !exact {
					limit--
				}
				policy := prefix.policy
				policy.CommitLimits.MaxBytes = limit
				policy.CommitLimits.MaxStringBytes = min(policy.CommitLimits.MaxStringBytes, limit)
				policy.CommitLimits.MaxByteStringBytes = min(policy.CommitLimits.MaxByteStringBytes, limit)
				if err := policy.Validate(); err != nil {
					t.Fatal(err)
				}
				// The candidate itself must fit; only the future closeout is too large.
				if candidate.ref.Sequence != 23 {
					t.Fatal("candidate sequence")
				}
				if _, err := codec.Encode(candidate.commit, policy.CommitLimits); err != nil {
					t.Fatalf("candidate must fit: %v", err)
				}
				beforeWidth := closeout.Clone()
				beforeWidth.Sequence = 23
				beforeWidthWire, err := codec.Encode(beforeWidth, prefix.policy.CommitLimits)
				if err != nil || len(beforeWidthWire) >= len(wire) {
					t.Fatalf("fixture did not cross a CBOR sequence width: %v", err)
				}
				// Scalar accounting reserves maximum-width headers, so sequence
				// growth changes wire bytes but not the conservative byte budget.
				var want error
				if !exact {
					want = codec.ErrLimitExceeded
				}
				if _, err := codec.Encode(closeout, policy.CommitLimits); !errors.Is(err, want) {
					t.Fatalf("measured closeout boundary: %v; want %v", err, want)
				}

				// Preserve the real canonical prefix/head, but supply a tighter policy
				// only at this private fixture boundary. We do not claim that Load
				// under a stricter policy accepts every earlier envelope in the prefix.
				h, err := history.New(backend, history.Policy{CommitLimits: policy.CommitLimits, RetryWindow: 16, MaxPageBytes: limit, MaxPageCommits: 2})
				if err != nil {
					t.Fatal(err)
				}
				s, err := New(h, id, policy)
				if err != nil {
					t.Fatal(err)
				}
				s.state = detachedSessionState(prefix.state)
				before := detachedSessionState(s.state)
				attempts := backend.appends
				var ref history.CommitRef
				switch path {
				case "prepare":
					var p prepared
					p, err = s.prepare([]Record{start})
					if !exact && !reflect.DeepEqual(p, prepared{}) {
						t.Fatal("failed prepare returned a proposal")
					}
					if exact && (p.ref != candidate.ref || !p.commit.Equal(candidate.commit)) {
						t.Fatal("exact preparation changed canonical proposal")
					}
				case "submit":
					b, beginErr := s.BeginCommit()
					if beginErr != nil {
						t.Fatal(beginErr)
					}
					got, addErr := b.StartRun()
					if addErr != nil || got != run {
						t.Fatalf("start addition: %v", addErr)
					}
					ref, err = s.SubmitCommit(context.Background(), b)
					if !exact && ref != (history.CommitRef{}) {
						t.Fatal("rejected submit returned accepted ref")
					}
				case "apply":
					// Accepted replay checks its own envelope, not hypothetical closeout.
					want = nil
					result, appendErr := h.Append(context.Background(), id, candidate.commit)
					if appendErr != nil || result.Status != history.StatusAccepted || result.Proposal != candidate.ref {
						t.Fatalf("asserted acceptance: %v", appendErr)
					}
					attempts = backend.appends
					err = s.Apply(history.Commit{Ref: candidate.ref, Commit: candidate.commit})
				}
				if !errors.Is(err, want) {
					t.Fatalf("%s: %v; want %v", path, err, want)
				}
				if !exact && path == "apply" {
					if backend.appends != attempts || s.Snapshot().Head() != candidate.ref || s.Snapshot().ActiveRun() != run {
						t.Fatal("hard-valid Apply failed to publish without I/O")
					}
					if _, err := s.prepareInterruption(); !errors.Is(err, codec.ErrLimitExceeded) {
						t.Fatalf("readable active prefix unexpectedly has write headroom: %v", err)
					}
					return
				}
				if !exact {
					if backend.appends != attempts || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
						t.Fatal("rejection appended or changed accepted state/counters")
					}
					b, err := s.BeginCommit()
					if err != nil {
						t.Fatalf("rejected candidate retained batch slot: %v", err)
					}
					if got, err := b.StartRun(); err != nil || got != run {
						t.Fatalf("rejection consumed run ordinal: %v", err)
					}
					if err := b.Discard(); err != nil {
						t.Fatal(err)
					}
					return
				}
				if path == "prepare" {
					if backend.appends != attempts {
						t.Fatal("prepare performed I/O")
					}
					result, err := h.Append(context.Background(), id, candidate.commit)
					if err != nil || result.Status != history.StatusAccepted {
						t.Fatalf("accept prepared: %v", err)
					}
					if err := s.Apply(history.Commit{Ref: candidate.ref, Commit: candidate.commit}); err != nil {
						t.Fatal(err)
					}
				} else if path == "apply" && backend.appends != attempts {
					t.Fatal("Apply performed I/O")
				}
				p, err := s.prepareInterruption()
				if err != nil || !p.commit.Equal(closeout) {
					t.Fatalf("actual exact-fit closeout: %v", err)
				}
				encoded, err := codec.Encode(p.commit, policy.CommitLimits)
				if err != nil || !bytes.Equal(encoded, wire) || budget.EstimatedBytes() != limit {
					t.Fatalf("closeout differs from measured exact admission size: %v", err)
				}
				b, err := s.BeginCommit()
				if err != nil {
					t.Fatal(err)
				}
				if err := b.Interrupt(); err != nil {
					t.Fatal(err)
				}
				ref, err = s.SubmitCommit(context.Background(), b)
				if err != nil || ref != p.ref || ref.Sequence != 24 || s.Snapshot().ActiveRun() != (RunID{}) {
					t.Fatalf("exact-fit closeout submit: %v", err)
				}
			})
		}
	}
}
