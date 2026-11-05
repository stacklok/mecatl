package session_test

import (
	"errors"
	"math"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestInputID(t *testing.T) {
	h := testHistoryID(t)
	for _, ordinal := range []uint64{1, 2, math.MaxUint64} {
		v := session.InputID{RunID: session.RunID{HistoryID: h, Ordinal: 1}, Ordinal: ordinal}
		if err := v.Validate(); err != nil {
			t.Fatal(err)
		}
		if !v.Equal(v.Clone()) {
			t.Fatal("identity clone changed value")
		}
		next, err := v.Next()
		if ordinal == math.MaxUint64 {
			if !errors.Is(err, session.ErrOrdinalExhausted) || next != (session.InputID{}) {
				t.Fatal("ordinal overflow must fail with zero output")
			}
		} else if err != nil || next.RunID != v.RunID || next.Ordinal != ordinal+1 {
			t.Fatalf("next ordinal: %v", err)
		}
	}
	for _, v := range []session.InputID{{}, {RunID: session.RunID{HistoryID: h, Ordinal: 1}}, {Ordinal: 1}, {RunID: session.RunID{HistoryID: h}, Ordinal: 1}, {RunID: session.RunID{Ordinal: 1}, Ordinal: 1}} {
		if v.Validate() == nil {
			t.Fatal("invalid input identity validated")
		}
		if next, err := v.Next(); err == nil || next != (session.InputID{}) {
			t.Fatal("invalid identity advanced")
		}
	}
}
