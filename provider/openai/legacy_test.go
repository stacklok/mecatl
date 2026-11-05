package openai

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestLegacyResponsesReplayFallbackPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, reasoning string
		want            []reasoningItem
	}{
		{"supported v1", `{"v":1,"items":[{"i":"rs_a","e":"encrypted-a"},{"i":"rs_b","e":"encrypted-b","n":1}]}`, []reasoningItem{{ID: "rs_a", Blob: "encrypted-a"}, {ID: "rs_b", Blob: "encrypted-b", After: 1}}},
		{"bare ciphertext", "encrypted-a", nil},
		{"old version", `{"v":0,"items":[{"i":"rs_a","e":"encrypted-a"}]}`, nil},
		{"unknown version", `{"v":2,"items":[{"i":"rs_a","e":"encrypted-a"}]}`, nil},
		{"incomplete item", `{"v":1,"items":[{"i":"rs_a","e":"encrypted-a"},{"i":"rs_b"}]}`, nil},
		{"negative position", `{"v":1,"items":[{"i":"rs_a","e":"encrypted-a","n":-1}]}`, nil},
		{"malformed", "{", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(map[string]string{"reasoning": tc.reasoning, "reasoning_item_id": "rs_stale_must_not_reactivate", "phase": "future_phase"})
			if err != nil {
				t.Fatal(err)
			}
			env, err := unpackReplay(&session.ProviderReplay{Format: "mecatl.legacy-message", Version: 1, Data: data})
			if err != nil || env.Phase != "future_phase" || !reflect.DeepEqual(env.Reasoning, tc.want) {
				t.Fatal("legacy replay changed supported items, fallback behavior or phase")
			}
		})
	}
	for _, data := range [][]byte{[]byte(`{"reasoning":42}`), []byte(`null`), {0xff}} {
		env, err := unpackReplay(&session.ProviderReplay{Format: "mecatl.legacy-message", Version: 1, Data: data})
		if err != nil || len(env.Reasoning) != 0 || env.Phase != "" {
			t.Fatal("malformed generic legacy envelope must remain a no-op")
		}
	}
}
