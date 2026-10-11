package eventsource_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/session"
)

func TestFoldRestoresHookHighWaterFromMetadataAndLog(t *testing.T) {
	creation := meta()
	creation.HookRevision = 4
	for _, tc := range []struct {
		name   string
		events []session.Event
		want   uint64
	}{
		{"metadata only", nil, 5},
		{"committed tail", []session.Event{{Type: session.EvHook, Hook: &session.HookPayload{HookID: "hook_tail", SessionID: creation.ID, Revision: 9}}}, 10},
		{"legacy unknown", []session.Event{{Type: session.EvHook, Hook: &session.HookPayload{}}}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := eventsource.Fold(creation, seq(tc.events))
			if err != nil {
				t.Fatal(err)
			}
			if got := s.NextHookRevision(); got != tc.want {
				t.Fatalf("next revision %d, want %d", got, tc.want)
			}
		})
	}
}
