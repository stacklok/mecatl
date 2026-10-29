package sessnap

import (
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestLegacyInvalidHistoryRejected(t *testing.T) {
	call := legacyCallDTO{ID: "token", Name: "Read", Args: json.RawMessage(`{}`)}
	assistant := legacyMessageDTO{Role: session.RoleAssistant, ToolCalls: []legacyCallDTO{call}}
	result := legacyMessageDTO{Role: session.RoleTool, ToolResult: &legacyResultDTO{CallID: "token", Content: "result", Parts: []session.Content{session.NewTextBlock("typed text")}}}
	for name, history := range map[string][]legacyMessageDTO{
		"duplicate batch":   {{Role: session.RoleAssistant, ToolCalls: []legacyCallDTO{call, call}}, result},
		"concurrent reuse":  {assistant, assistant, result},
		"empty call":        {{Role: session.RoleAssistant, ToolCalls: []legacyCallDTO{{Name: "Read"}}}},
		"wrong call role":   {{Role: session.RoleUser, ToolCalls: []legacyCallDTO{call}}, result},
		"orphan result":     {result},
		"repeated result":   {assistant, result, result},
		"empty result":      {{Role: session.RoleTool, ToolResult: &legacyResultDTO{Content: "unattached"}}},
		"wrong result role": {assistant, {Role: session.RoleAssistant, ToolResult: result.ToolResult}},
		"missing result":    {{Role: session.RoleTool}},
		"wrong replay role": {{Role: session.RoleUser, Reasoning: "opaque"}},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(history)
			if err != nil {
				t.Fatal(err)
			}
			data = withJSONFields(t, legacyFixture(t, "legacy-completed"), map[string]json.RawMessage{"messages": data})
			var snap Snapshot
			if err := json.Unmarshal(data, &snap); err == nil {
				t.Fatal("decoded invalid legacy history")
			}
			if _, err := UnmarshalFormat(data, "sessnap-json/1"); err == nil {
				t.Fatal("restored invalid legacy history")
			}
		})
	}
}

func TestLegacySnapshotRequiresClosedPairing(t *testing.T) {
	fixture := legacyFixture(t, "legacy-completed")
	var source struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(fixture, &source); err != nil {
		t.Fatal(err)
	}
	for _, state := range []session.State{session.StateIdle, session.StateCompleted, session.StateFailed} {
		t.Run(string(state), func(t *testing.T) {
			stateJSON, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			for _, closed := range []bool{false, true} {
				history := source.Messages
				if !closed {
					history = history[:len(history)-1]
				}
				messagesJSON, err := json.Marshal(history)
				if err != nil {
					t.Fatal(err)
				}
				data := withJSONFields(t, fixture, map[string]json.RawMessage{"state": stateJSON, "messages": messagesJSON})
				for name, load := range map[string]func([]byte) (*session.Session, error){
					"JSON": func(data []byte) (*session.Session, error) {
						var snap Snapshot
						if err := json.Unmarshal(data, &snap); err != nil {
							return nil, err
						}
						return snap.Restore()
					},
					"Unmarshal": Unmarshal,
					"/1": func(data []byte) (*session.Session, error) {
						return UnmarshalFormat(data, "sessnap-json/1")
					},
				} {
					s, err := load(data)
					if !closed {
						if err == nil {
							t.Fatalf("%s restored dangling call", name)
						}
						continue
					}
					if err != nil {
						t.Fatalf("%s rejected sequential completed reuse: %v", name, err)
					}
					messages := s.Conversation.Messages
					if s.State != state || len(messages) != len(history) {
						t.Fatalf("%s changed state or history", name)
					}
					if err := session.ValidateToolPairing(messages); err != nil {
						t.Fatalf("%s invalid pairing: %v", name, err)
					}
					if messages[1].ToolCalls[0].ID == messages[3].ToolCalls[0].ID || messages[1].Replay.Bindings[0].PairingID != "reused" || messages[3].Replay.Bindings[0].PairingID != "reused" {
						t.Fatalf("%s lost sequential identity or provider pairing", name)
					}
				}
			}
		})
	}
}
