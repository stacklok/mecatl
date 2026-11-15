package session_test

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestModelSettingsSubmitLoadExactOwnershipAndOmission(t *testing.T) {
	h, s, p := newIteratorSession(t)
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	scope := s.Snapshot().HistoryID()
	binding := session.ModelBinding{Route: "r", API: "a", Model: "m", Settings: session.ModelSettings{
		ReasoningEffort: new(" target-native/尾 \n"), MaxOutputTokens: new(int64(9223372036854775807)),
	}}
	want := binding.Clone()
	mustBatch(t, b.ReplaceModelBinding(scope, binding))
	*binding.Settings.ReasoningEffort = "changed"
	*binding.Settings.MaxOutputTokens = -1
	submitBatch(t, s, b)
	old := s.Snapshot()
	for seam, candidate := range iteratorSessions(t, h, s, p) {
		t.Run(seam, func(t *testing.T) {
			snapshot := candidate.Snapshot()
			v, ok := snapshot.ModelBinding()
			fromRecord, recordOK := snapshot.RecordAt(1).ModelBinding()
			if !ok || !recordOK || v != fromRecord || v.HistoryID() != scope || !bindingMatches(v, want) {
				t.Fatal("typed settings changed or lost scope")
			}
			if n := testing.AllocsPerRun(100, func() {
				effort, e := v.Settings().ReasoningEffort()
				tokens, m := v.Settings().MaxOutputTokens()
				if !e || effort != *want.Settings.ReasoningEffort || !m || tokens != *want.Settings.MaxOutputTokens {
					panic("settings scalar")
				}
			}); n != 0 {
				t.Fatalf("settings scalar navigation allocated %g times", n)
			}
			clear(snapshot.RecordAt(1).MutableClone().Payload)
			if !bindingMatches(v, want) {
				t.Fatal("format export aliased settings")
			}
		})
	}
	b = beginBatch(t, s)
	for _, settings := range []session.ModelSettings{
		{ReasoningEffort: new("")}, {ReasoningEffort: new("\xff")},
		{MaxOutputTokens: new(int64(0))}, {MaxOutputTokens: new(int64(-1))},
	} {
		binding.Settings = settings
		if err := b.ReplaceModelBinding(scope, binding); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatalf("invalid settings accepted: %v", err)
		}
	}
	binding.Settings = session.ModelSettings{}
	mustBatch(t, b.ReplaceModelBinding(scope, binding))
	submitBatch(t, s, b)
	for _, candidate := range iteratorSessions(t, h, s, p) {
		v, ok := candidate.Snapshot().ModelBinding()
		_, e := v.Settings().ReasoningEffort()
		_, m := v.Settings().MaxOutputTokens()
		if !ok || e || m || candidate.Snapshot().RecordCount() != 3 {
			t.Fatal("nil settings inherited controls or invalid additions retained")
		}
	}
	v, _ := old.ModelBinding()
	if !bindingMatches(v, want) {
		t.Fatal("replacement changed pinned settings")
	}
	var zero session.ModelSettingsView
	if v, ok := zero.ReasoningEffort(); ok || v != "" {
		t.Fatal("zero effort")
	}
	if v, ok := zero.MaxOutputTokens(); ok || v != 0 {
		t.Fatal("zero max tokens")
	}
}
