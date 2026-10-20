package sessnap

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestAmbiguousLegacyContinuationBecomesPromptable(t *testing.T) {
	s := restoreFixture(t, legacyFixture(t, "legacy-ambiguous-pending"))
	if s.State != session.StateIdle {
		t.Fatalf("ambiguous legacy state = %s, want idle", s.State)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("new turn: %v", err)
	}
}

func assertAbandonmentCycles(t *testing.T, s *session.Session) {
	t.Helper()
	want := session.CloneMessages(s.Conversation.Messages)
	for range 2 {
		assertAbandoned(t, s)
		snap, err := Of(s)
		if err != nil {
			t.Fatal(err)
		}
		s, err = snap.Restore()
		if err != nil {
			t.Fatal(err)
		}
		assertAbandoned(t, s)
		data, err := Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("identity_hold")) {
			t.Fatal("abandoned continuation persisted")
		}
		s = restoreFixture(t, data)
		assertAbandoned(t, s)
		if !sameStoredHistory(t, s.Conversation.Messages, want) {
			t.Fatal("cycles changed or duplicated migrated history")
		}
	}
}

func TestLegacyAbandonmentProofBoundaries(t *testing.T) {
	for _, state := range []session.State{session.StateAwaiting, session.StateAuthorizing} {
		for _, defect := range []string{"missing-control", "missing-key", "duplicate-key", "existing-result"} {
			t.Run(string(state)+"/"+defect, func(t *testing.T) {
				var old Snapshot
				if err := json.Unmarshal(legacyFixture(t, "legacy-authorizing"), &old); err != nil {
					t.Fatal(err)
				}
				old.State = state
				calls := old.legacyMessages[2].ToolCalls // same-tool siblings
				if state == session.StateAwaiting {
					old.legacyPending = &legacyPendingAskDTO{AskID: "obsolete-ask", Call: calls[0].ID, Tool: calls[0].Name, Args: []byte(`{"display":true}`)}
					old.legacyAuthorization = nil
				}
				switch defect {
				case "missing-control":
					if state == session.StateAwaiting {
						old.legacyPending.Call = ""
					} else {
						old.legacyAuthorization.Call.ID = ""
					}
				case "missing-key":
					calls[0].ID = ""
				case "duplicate-key":
					calls[0].ID = calls[1].ID
				case "existing-result":
					old.legacyMessages = append(old.legacyMessages, legacyMessageDTO{Role: session.RoleTool, ToolResult: &legacyResultDTO{CallID: calls[0].ID, Content: "already completed", Parts: []session.Content{{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{1, 2, 3}}}}})
				}
				old.PendingWorkspaceEnrollment = &session.PendingWorkspaceEnrollment{}
				old.RetryPending, old.LastError = true, "old continuation failure"
				var err error
				old.legacyPendingBytes, err = json.Marshal(old.legacyPending)
				if err != nil {
					t.Fatal(err)
				}
				old.legacyAuthorizationBytes, err = json.Marshal(old.legacyAuthorization)
				if err != nil {
					t.Fatal(err)
				}
				wire, err := json.Marshal(old)
				if err != nil {
					t.Fatal(err)
				}
				first := restoreFixture(t, wire)
				second := restoreFixture(t, wire)
				if !reflect.DeepEqual(first.Conversation.Messages, second.Conversation.Messages) {
					t.Fatal("independent reads disagree")
				}
				if defect == "existing-result" {
					result := first.Conversation.Messages[3].ToolResult
					if result == nil || result.Content != "already completed" || result.IsError || !bytes.Equal(result.Parts[0].Data, []byte{1, 2, 3}) {
						t.Fatal("existing completed result changed")
					}
				}
				if defect == "missing-key" || defect == "duplicate-key" {
					m := first.Conversation.Messages[2]
					if m.Legacy.Ordinal != 2 || len(m.Legacy.Unsupported) == 0 || (defect == "duplicate-key" && len(m.ToolCalls) != 0) {
						t.Fatal("ambiguous siblings gained guessed links or lost original provenance")
					}
				}
				assertAbandonmentCycles(t, first)

				// Direct pre-schema literals carry provider strings in public fields.
				// Decode just those fields without Snapshot's schema-aware decoder.
				type plain Snapshot
				var direct plain
				if err := json.Unmarshal(wire, &direct); err != nil {
					t.Fatal(err)
				}
				s, err := Snapshot(direct).Restore()
				if err != nil {
					t.Fatal(err)
				}
				assertAbandonmentCycles(t, s)
			})
		}
	}
}

func TestCandidateHoldNormalizesAtDecoderBoundary(t *testing.T) {
	for _, state := range []session.State{session.StateAwaiting, session.StateAuthorizing} {
		t.Run(string(state), func(t *testing.T) {
			data := legacyFixture(t, "candidate-held")
			if state == session.StateAuthorizing {
				data = bytes.Replace(data, []byte(`"state": "awaiting"`), []byte(`"state": "authorizing"`), 1)
				data = bytes.Replace(data, []byte(`eyJ2ZXJzaW9uIjoxLCJzdGF0ZSI6ImF3YWl0aW5nIiwicGVuZGluZyI6ImV5SkJjMnRKUkNJNkltOWljMjlzWlhSbExXRnpheUlzSWtOaGJHd2lPaUp0YVhOemFXNW5JbjA9IiwicGVuZGluZ193b3Jrc3BhY2VfZW5yb2xsbWVudCI6ImUzMD0iLCJyZXRyeV9wZW5kaW5nIjp0cnVlLCJsYXN0X2Vycm9yIjoib2xkIGNvbnRpbnVhdGlvbiBmYWlsdXJlIn0=`), []byte(`eyJ2ZXJzaW9uIjoxLCJzdGF0ZSI6ImF1dGhvcml6aW5nIiwicGVuZGluZ19hdXRob3JpemF0aW9uIjoiZXlKaGRYUm9iM0pwZW1GMGFXOXVJanA3SW1sa0lqb2liMkp6YjJ4bGRHVXRZWFYwYUNJc0ltSnBibVJwYm1jaU9pSndjbWwyWVhSbExXSnBibVJwYm1jaWZTd2lZMkZzYkNJNmV5SnBaQ0k2SW0xcGMzTnBibWNpZlgwPSJ9`), 1)
			}
			var raw struct {
				Messages []messageDTO `json:"messages"`
			}
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			s := restoreFixture(t, data)
			messages := s.Conversation.Messages
			if len(messages) != 5 || messages[3].ToolResult == nil || messages[3].ToolResult.CallID != "canonical-b" || !messages[3].ToolResult.IsError {
				t.Fatal("did not close only the provably outstanding call")
			}
			if messages[1].ToolResult.Content != "completed result" || messages[1].ToolResult.IsError || !bytes.Equal(messages[1].ToolResult.Parts[0].Data, []byte{4, 5, 6}) {
				t.Fatal("completed result overwritten")
			}
			unattached := session.HistoricalMessage(messages[2])
			if messages[2].ToolResult != nil || messages[2].Text != "readable row\nunattached result" || unattached == nil || !unattached.AttachmentUnavailable || !bytes.Equal(unattached.Parts[0].Data, []byte{1, 2, 3}) {
				t.Fatal("unattached result content/media lost or attached")
			}
			for i := range 3 {
				if !reflect.DeepEqual(messages[i].Legacy, raw.Messages[i].Legacy) {
					t.Fatal("immutable provenance changed")
				}
			}
			second := restoreFixture(t, data)
			if !reflect.DeepEqual(messages, second.Conversation.Messages) {
				t.Fatal("independent candidate reads disagree")
			}
			var snap Snapshot
			if err := json.Unmarshal(data, &snap); err != nil || snap.State != session.StateIdle {
				t.Fatal("Snapshot decode did not normalize", err)
			}
			assertAbandonmentCycles(t, s)
		})
	}
}

func TestUnattachedLegacyResultPreservesTypedContent(t *testing.T) {
	for _, state := range []session.State{session.StateAwaiting, session.StateAuthorizing} {
		t.Run(string(state), func(t *testing.T) {
			var old Snapshot
			if err := json.Unmarshal(legacyFixture(t, "legacy-ambiguous-pending"), &old); err != nil {
				t.Fatal(err)
			}
			old.State = state
			if state == session.StateAuthorizing {
				old.legacyPending, old.legacyPendingBytes = nil, nil
				old.legacyAuthorization = &legacyPendingAuthorizationDTO{}
			}
			parts := []session.Content{
				session.NewTextBlock("typed result text"),
				session.NewResourceLinkBlock("fixture:asset", "asset", "title", "description", "image/png", 3, []string{"user"}),
				{BlockKind: session.BlockImage, Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{1, 2, 3}},
			}
			old.legacyMessages = append(old.legacyMessages, legacyMessageDTO{Role: session.RoleTool, Text: "row text", Parts: []contentDTO{{Kind: session.MediaAudio, MIMEType: "audio/wav", Data: []byte{4, 5, 6}}}, ToolResult: &legacyResultDTO{CallID: "provider-duplicate", Content: "result summary", Parts: parts}})
			s, err := old.Restore()
			if err != nil {
				t.Fatal(err)
			}
			m := s.Conversation.Messages[1]
			history := session.HistoricalMessage(m)
			if m.ToolResult != nil || m.Legacy.ResultOrdinal != -1 || m.Legacy.ResultSlot != -1 || history == nil || history.Text != "row text\nresult summary" || !history.AttachmentUnavailable {
				t.Fatal("unattached result lost text or gained an authoritative link")
			}
			if len(history.Parts) != 4 || !bytes.Equal(history.Parts[0].Data, []byte{4, 5, 6}) || !reflect.DeepEqual(history.Parts[1:], parts) {
				t.Fatal("unattached result lost typed content/media")
			}
			if len(m.Parts) != 1 || m.Parts[0].BlockKind != "" {
				t.Fatal("tool-result blocks leaked into message media")
			}
			assertAbandonmentCycles(t, s)
		})
	}
}

func TestCandidateHoldRejectsCorruption(t *testing.T) {
	fixture := legacyFixture(t, "candidate-held")
	for _, tc := range []struct{ field, value string }{
		{"identity_hold", `null`},
		{"identity_hold", `{"Reason":"other","Payload":"e30="}`},
		{"identity_hold", `{"Reason":"legacy-continuation-unproven","Payload":"e30=","future":true}`},
		{"state", `"idle"`},
		{"state", `"authorizing"`},
		{"pending", `{"AskID":"old"}`},
		{"pending_authorization", `{}`},
		{"pending_workspace_enrollment", `{}`},
		{"messages", `[{"role":"tool","tool_result":{"CallID":"orphan"}}]`},
		{"messages", `[{"role":"assistant","tool_calls":[{"ID":"a","Name":"Read"}]},{"role":"assistant","tool_calls":[{"ID":"b","Name":"Read"}],"replay":{"Bindings":[{"CallID":"b","PairingID":"engine_a"}]}}]`},
	} {
		t.Run(tc.field+"/"+tc.value, func(t *testing.T) {
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(fixture, &wire); err != nil {
				t.Fatal(err)
			}
			wire[tc.field] = []byte(tc.value)
			data, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Unmarshal(data); err == nil {
				t.Fatal("accepted corrupt candidate hold")
			}
			var snap Snapshot
			if err := json.Unmarshal(data, &snap); err == nil {
				t.Fatal("direct decoder accepted corrupt candidate hold")
			}
		})
	}
	for _, payload := range []string{`{"version":99,"state":"awaiting"}`, `{"version":1,"state":"awaiting","future":true}`, `{"version":1,"state":"awaiting"} {}`, `{"version":1,"state":"awaiting","pending":"eA=="}`, string(bytes.Repeat([]byte("x"), (16<<20)+1))} {
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(fixture, &wire); err != nil {
			t.Fatal(err)
		}
		var err error
		wire["identity_hold"], err = json.Marshal(candidateHold{Reason: "legacy-continuation-unproven", Payload: []byte(payload)})
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Unmarshal(data); err == nil {
			t.Fatal("accepted corrupt or oversized hold payload")
		}
	}
}
