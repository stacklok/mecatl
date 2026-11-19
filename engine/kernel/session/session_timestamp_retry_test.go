package session

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
)

// timestampBackend retains exact wire attempts and can lose one accepted ack.
type timestampBackend struct {
	history.Backend
	attempts []history.ByteCommit
	loseAck  bool
}

func (b *timestampBackend) Append(ctx context.Context, id history.HistoryID, expected history.CommitRef, c history.ByteCommit) (history.Observation, error) {
	b.attempts = append(b.attempts, history.ByteCommit{Ref: c.Ref, Bytes: bytes.Clone(c.Bytes)})
	result, err := b.Backend.Append(ctx, id, expected, c)
	if err == nil && b.loseAck {
		b.loseAck = false
		return history.Observation{}, errors.New("lost acknowledgement")
	}
	return result, err
}

func TestSessionTimestampFrozenRetries(t *testing.T) {
	for _, successor := range []bool{false, true} {
		for _, canceled := range []bool{false, true} {
			t.Run("successor="+boolLabel(successor)+"/canceled="+boolLabel(canceled), func(t *testing.T) {
				backend := &timestampBackend{Backend: mem.New()}
				h := newLifecycleHistory(t, backend)
				calls, nanos, forbidden := 0, int64(0), false
				config := Config{Policy: sessionPolicy(), InstanceID: 17, Now: func() time.Time {
					if forbidden {
						t.Fatal("retry or load sampled clock")
					}
					calls++
					nanos--
					return time.Unix(0, nanos)
				}}
				s, err := New(h, testHistoryID(t), config)
				interactionCheck(t, err)
				b, err := s.BeginCommit()
				interactionCheck(t, err)
				interactionCheck(t, b.Create())
				if successor {
					_, err = s.SubmitCommit(t.Context(), b)
					interactionCheck(t, err)
					b, err = s.BeginCommit()
					interactionCheck(t, err)
					interactionCheck(t, b.ReplaceRootInstructions(s.Snapshot().HistoryID(), RootInstructions{}))
				}
				before := calls
				captured := captureCommit(t, b)
				priorAttempts := len(backend.attempts)
				ctx := t.Context()
				if canceled {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				} else {
					backend.loseAck = true
				}
				ref, err := s.SubmitCommit(ctx, b)
				if ref != (history.CommitRef{}) || !errors.Is(err, ErrAcceptanceUnknown) || calls != before+1 || !b.frozen {
					t.Fatalf("initial uncertain submission: %v; calls=%d", err, calls-before)
				}
				if canceled && len(backend.attempts) != priorAttempts {
					t.Fatal("canceled preparation reached backend")
				}
				proposal := b.proposal
				wire, err := codec.Encode(proposal.commit, s.policy.CommitLimits)
				interactionCheck(t, err)
				if proposal.commit.Timestamp != nanos || proposal.commit.InstanceID != 17 || proposal.commit.Records[0].Timestamp != nanos+1 {
					t.Fatal("record and commit metadata were not sampled separately")
				}
				// Advance the clock and change the caller's copied configuration. A
				// frozen retry must not sample or reassemble any metadata.
				nanos -= 10000
				forbidden = true
				config.InstanceID = 99
				cancelCtx, cancel := context.WithCancel(t.Context())
				cancel()
				if _, err := s.SubmitCommit(cancelCtx, b); !errors.Is(err, ErrAcceptanceUnknown) {
					t.Fatalf("second canceled retry: %v", err)
				}
				ref, err = s.SubmitCommit(t.Context(), b)
				interactionCheck(t, err)
				if ref != proposal.ref || calls != before+1 {
					t.Fatal("frozen retry changed identity or sampled clock")
				}
				for _, attempt := range backend.attempts[priorAttempts:] {
					if attempt.Ref != proposal.ref || !bytes.Equal(attempt.Bytes, wire) {
						t.Fatal("retry changed exact frozen wire proposal")
					}
				}
				loaded, err := Load(t.Context(), h, testHistoryID(t), config)
				interactionCheck(t, err)
				if loaded.Snapshot().Head() != ref || captured.RecordAt(captured.RecordCount()-1).MutableClone().Timestamp != proposal.commit.Records[0].Timestamp {
					t.Fatal("load or accepted publication changed record metadata")
				}
			})
		}
	}
}
