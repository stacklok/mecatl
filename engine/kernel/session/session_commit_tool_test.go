package session

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

func TestSessionCommitToolFactAuthoringPrecedence(t *testing.T) {
	for _, kind := range []string{"invocation", "disposition"} {
		t.Run(kind, func(t *testing.T) {
			s := interactionFixture(t, "active")
			call := RequestedCallID{requestID: requestID(s.Snapshot().HistoryID(), 1, 1), ordinal: 1}
			foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
			interactionCheck(t, err)
			b, err := s.BeginCommit()
			interactionCheck(t, err)
			defer b.Discard() //nolint:errcheck // Deliberately unsubmitted batch.
			interactionCheck(t, b.ReplaceInstructions(s.Snapshot().HistoryID(), Instructions{}))
			before, records := b.admission, append([]*materializedRecord(nil), b.records...)
			oversized := strings.Repeat("x", s.policy.PayloadLimits.MaxStringBytes+1)
			add := func(b *SessionCommit, call RequestedCallID, ids [3]uint64) error {
				if kind == "invocation" {
					return b.AddToolInvocation(call, ToolInvocation{RunID: ids[0], RequestID: ids[1], CallID: ids[2], Name: oversized})
				}
				return b.AddToolDisposition(call, ToolDisposition{RunID: ids[0], RequestID: ids[1], CallID: ids[2], Diagnostic: &oversized})
			}
			for _, tc := range []struct {
				name string
				edit func(*RequestedCallID)
				ids  [3]uint64
				want error
			}{
				{"zero call", func(c *RequestedCallID) { *c = RequestedCallID{} }, [3]uint64{}, codec.ErrInvalidData},
				{"zero history", func(c *RequestedCallID) { c.requestID.runID.historyID = historyid.HistoryID{} }, [3]uint64{}, codec.ErrInvalidData},
				{"zero run", func(c *RequestedCallID) { c.requestID.runID.ordinal = 0 }, [3]uint64{}, codec.ErrInvalidData},
				{"zero request", func(c *RequestedCallID) { c.requestID.requestOrdinal = 0 }, [3]uint64{}, codec.ErrInvalidData},
				{"zero component", func(c *RequestedCallID) { c.ordinal = 0 }, [3]uint64{}, codec.ErrInvalidData},
				{"foreign before DTO fields", func(c *RequestedCallID) { c.requestID.runID.historyID = foreign }, [3]uint64{1, 1, 1}, ErrIdentity},
				{"invalid foreign before scope", func(c *RequestedCallID) { c.requestID.runID.historyID = foreign; c.ordinal = 0 }, [3]uint64{}, codec.ErrInvalidData},
				{"incoming run", nil, [3]uint64{1, 0, 0}, codec.ErrInvalidData},
				{"incoming request", nil, [3]uint64{0, 1, 0}, codec.ErrInvalidData},
				{"incoming call", nil, [3]uint64{0, 0, 1}, codec.ErrInvalidData},
				{"payload bounds before local syntax", nil, [3]uint64{}, codec.ErrLimitExceeded},
			} {
				t.Run(tc.name, func(t *testing.T) {
					id := call
					if tc.edit != nil {
						tc.edit(&id)
					}
					if err := add(b, id, tc.ids); !errors.Is(err, tc.want) {
						t.Fatalf("got %v, want %v", err, tc.want)
					}
					if !reflect.DeepEqual(b.admission, before) || !reflect.DeepEqual(b.records, records) {
						t.Fatal("failed addition changed prior candidate or records")
					}
				})
			}
			copyOfBatch := *b
			for _, invalid := range []*SessionCommit{nil, {}, &copyOfBatch} {
				if err := add(invalid, RequestedCallID{}, [3]uint64{1, 1, 1}); !errors.Is(err, ErrBatch) {
					t.Fatalf("live handle must precede identity/payload: %v", err)
				}
			}
			b.frozen = true
			if err := add(b, call, [3]uint64{}); !errors.Is(err, ErrBatch) {
				t.Fatalf("frozen handle: %v", err)
			}
			b.frozen = false
			interactionCheck(t, b.Discard())
			if err := add(b, call, [3]uint64{}); !errors.Is(err, ErrBatch) {
				t.Fatalf("discarded handle: %v", err)
			}
		})
	}
}
