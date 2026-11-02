package sessnap

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func legacyWithFields(t *testing.T, fields map[string]json.RawMessage) []byte {
	t.Helper()
	return withJSONFields(t, legacyFixture(t, "legacy-completed"), fields)
}

func withJSONFields(t *testing.T, data []byte, fields map[string]json.RawMessage) []byte {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	for key, value := range fields {
		if value == nil {
			delete(object, key)
			continue
		}
		object[key] = value
	}
	data, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decodeSnapshot(t *testing.T, data []byte) Snapshot {
	t.Helper()
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func snapshotObject(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func TestCurrentSnapshotJSONRequiresHistoryField(t *testing.T) {
	base := []byte(`{"identity_schema":2,"id":"empty","state":"idle","environment_ref":{"Kind":"mem","ID":"workspace","Revision":"v1"}}`)
	for _, tc := range []struct {
		name      string
		fields    map[string]json.RawMessage
		wantError bool
	}{
		{name: "absent", wantError: true},
		{name: "null", fields: map[string]json.RawMessage{"messages": json.RawMessage(`null`)}},
		{name: "empty", fields: map[string]json.RawMessage{"messages": json.RawMessage(`[]`)}},
		{name: "case insensitive", fields: map[string]json.RawMessage{"MESSAGES": json.RawMessage(`[]`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := withJSONFields(t, base, tc.fields)
			var snap Snapshot
			if err := json.Unmarshal(data, &snap); (err != nil) != tc.wantError {
				t.Fatalf("direct decode error = %v, wantError %v", err, tc.wantError)
			}
			if _, err := UnmarshalFormat(data, Format); (err != nil) != tc.wantError {
				t.Fatalf("format decode error = %v, wantError %v", err, tc.wantError)
			}
		})
	}
}

func TestSnapshotJSONNormalizesSchemaBeforeRestore(t *testing.T) {
	legacy := legacyFixture(t, "legacy-completed")
	loaded := restoreFixture(t, legacy)
	current, err := Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		data   []byte
		format string
	}{
		{name: "legacy", data: legacy, format: "sessnap-json/1"},
		{name: "current", data: current, format: Format},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := decodeSnapshot(t, tc.data)
			if snap.IdentitySchema != 2 {
				t.Fatalf("IdentitySchema = %d, want 2", snap.IdentitySchema)
			}
			if !snap.Incarnation.Valid() {
				t.Fatalf("Incarnation = %q, want valid persisted identity", snap.Incarnation)
			}
			if len(snap.Messages) != len(loaded.Conversation.Messages) {
				t.Fatalf("decoded messages = %d, want %d canonical messages", len(snap.Messages), len(loaded.Conversation.Messages))
			}
			encoded, err := json.Marshal(snap)
			if err != nil {
				t.Fatal(err)
			}
			if got := snapshotObject(t, encoded)["identity_schema"]; !reflect.DeepEqual(got, json.RawMessage("2")) {
				t.Fatalf("re-encoded identity_schema = %s, want 2", got)
			}
			if _, err := UnmarshalFormat(encoded, Format); err != nil {
				t.Fatalf("normalized re-encoding did not load as /2: %v", err)
			}
			if _, err := UnmarshalFormat(tc.data, tc.format); err != nil {
				t.Fatalf("original format did not load: %v", err)
			}
		})
	}
}

func TestLegacySnapshotJSONPreservesStableMetadataDuringNormalization(t *testing.T) {
	metadata := map[string]any{
		"id": "metadata-copy", "state": session.StateCompleted, "mode": session.ModeDefault,
		"limits":           session.Limits{MaxTurns: 10, MaxToolCalls: 20, MaxConsecutiveFailures: 3},
		"counters":         session.Counters{Turns: 2, ToolCalls: 3, ConsecutiveFailures: 1},
		"external_binding": "external-1", "created_at": "2025-01-02T03:04:05Z",
		"pending_workspace_enrollment": session.PendingWorkspaceEnrollment{ID: "enrollment-1", RequiredServices: 1},
		"stop_reason":                  session.StopEndTurn, "kind": session.SessionKindMain,
		"profile": "review", "execution_template_id": "template", "execution_template_revision": "r17",
		"agent_definition_name": "agent", "agent_create_intent": "explicit",
		"provider_id": "provider", "model_id": "model", "reasoning_effort": "high",
		"debug_mcp_servers": []string{"server-a"}, "debug_mcp_tools": []string{"tool-a"},
		"debug_target_fingerprint": "fingerprint",
		"title":                    "A durable title", "title_provenance": session.TitleProvenanceGenerated, "title_revision": 7,
		"title_generation": session.TitleGenerationGenerated, "title_source_prompts": []string{"prompt"},
		"title_attempts": []session.TitleAttempt{{ID: "attempt-1", Outcome: session.TitleAttemptSucceeded}},
		"token_usage": map[session.UsageKind]session.TokenUsage{session.UsageKindMain: {
			Total: session.Usage{InputTokens: 12}, Models: map[string]session.Usage{"model": {InputTokens: 12}},
		}},
		"latest_context_occupancy": session.ContextOccupancy{InputTokens: 42, Estimated: true},
		"retry_disposition":        session.RetryDispositionRetryable, "stream_progress": session.StreamProgressVisible,
		"retry_pending": true, "retry_pending_disposition": session.RetryDispositionRetryable,
		"retry_pending_progress": session.StreamProgressVisible, "last_error": "durable failure", "run_id": "run-1",
		"owner":                            session.Principal{Issuer: "issuer", Subject: "subject", GrantType: "user", Name: "Owner"},
		"authority":                        session.Authority{Provenance: "test"},
		"execution_capabilities":           session.ExecutionCapabilities{Files: true, Shell: true},
		"workspace_enrollment_broker_keys": []string{"broker-1"}, "workspace_enrollment_broker_keys_present": true,
		"environment_ref": session.EnvironmentRef{Kind: session.EnvKindMem, ID: "workspace", Revision: "v1"},
		"placement":       session.PlacementMetadata{Kind: "mem", Label: "fixture", Branch: "branch", Revision: "v1"},
	}
	fields := make(map[string]json.RawMessage, len(metadata))
	for key, value := range metadata {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		fields[key] = encoded
	}
	// Identity-bearing fields and learning eligibility have separate normalization tests.
	for _, field := range reflect.VisibleFields(reflect.TypeFor[Snapshot]()) {
		switch field.Name {
		case "IdentitySchema", "LearningEligible", "Incarnation", "Messages", "Pending", "PendingAuthorization", "Relationship":
			continue
		}
		key, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if _, ok := fields[key]; !ok {
			t.Errorf("stable field %s needs a metadata-copy regression value", field.Name)
		}
	}
	data := legacyWithFields(t, fields)
	snap := decodeSnapshot(t, data)
	encoded, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	got := snapshotObject(t, encoded)
	for key, want := range fields {
		if !jsonEqual(got[key], want) {
			t.Errorf("%s = %s after normalization, want %s", key, got[key], want)
		}
	}
}

func TestLegacySnapshotJSONPreservesRelationshipMetadata(t *testing.T) {
	branch := 3
	inc := session.NewIncarnationID()
	want := session.SessionRelationship{
		ScheduleName: "schedule", OriginSessionID: "origin", OriginIncarnation: inc,
		ParentSessionID: "parent", ParentIncarnation: inc,
		BranchIndex: &branch, TeamID: "team", MemberName: "member",
		DebugTargetID: "debug-target", DebugTargetIncarnation: inc,
	}
	relationship, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	snap := decodeSnapshot(t, legacyWithFields(t, map[string]json.RawMessage{"relationship": relationship}))
	if !reflect.DeepEqual(snap.Relationship, want) {
		t.Fatal("legacy normalization lost relationship metadata or promoted a provider reference")
	}
}

func jsonEqual(a, b json.RawMessage) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}

func TestSnapshotJSONNormalizationKeepsIncarnationAndCanonicalIDsStable(t *testing.T) {
	owner := json.RawMessage(`{"Issuer":"https://issuer.example","Subject":"subject","GrantType":"user","Name":"Owner"}`)
	for _, tc := range []struct {
		name        string
		incarnation json.RawMessage
	}{
		{name: "persisted", incarnation: json.RawMessage(`"` + string(session.NewIncarnationID()) + `"`)},
		{name: "missing", incarnation: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := legacyWithFields(t, map[string]json.RawMessage{
				"owner":       owner,
				"incarnation": tc.incarnation,
			})
			first := decodeSnapshot(t, data)
			second := decodeSnapshot(t, data)
			if first.Incarnation != second.Incarnation || !reflect.DeepEqual(first.Messages, second.Messages) {
				t.Fatal("repeated direct decodes changed normalized identity")
			}
			want := session.LegacyIncarnationID(first.ID, first.CreatedAt.UnixNano(), first.Owner)
			if tc.incarnation != nil {
				if err := json.Unmarshal(tc.incarnation, &want); err != nil {
					t.Fatal(err)
				}
			}
			if first.Incarnation != want {
				t.Fatalf("incarnation = %q, want stored or derived %q", first.Incarnation, want)
			}
			if first.LearningEligible {
				t.Fatal("legacy decode enabled learning")
			}
			encoded, err := json.Marshal(first)
			if err != nil {
				t.Fatal(err)
			}
			reloaded := decodeSnapshot(t, encoded)
			if reloaded.Incarnation != first.Incarnation || !reflect.DeepEqual(reloaded.Messages, first.Messages) {
				t.Fatal("normalized reload changed incarnation or canonical call IDs")
			}
			s, err := Unmarshal(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if s.Incarnation() != first.Incarnation || len(s.Conversation.Messages) != len(first.Messages) ||
				s.Conversation.Messages[1].ToolCalls[0].ID != first.Messages[1].ToolCalls[0].ID ||
				s.Conversation.Messages[3].ToolCalls[0].ID != first.Messages[3].ToolCalls[0].ID {
				t.Fatal("restored normalized snapshot changed incarnation or canonical call IDs")
			}
		})
	}
}

func TestCurrentSnapshotJSONPreservesPersistedIncarnation(t *testing.T) {
	want := session.NewIncarnationID()
	incarnation, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	data := withJSONFields(t, []byte(`{"identity_schema":2,"id":"current","created_at":"2025-01-02T03:04:05Z","state":"idle","messages":[],"environment_ref":{"Kind":"mem","ID":"workspace","Revision":"v1"}}`), map[string]json.RawMessage{"incarnation": incarnation})
	snap := decodeSnapshot(t, data)
	if snap.Incarnation != want {
		t.Fatalf("decoded incarnation = %q, want persisted %q", snap.Incarnation, want)
	}
	for range 2 {
		restored, err := UnmarshalFormat(data, Format)
		if err != nil {
			t.Fatal(err)
		}
		if restored.Incarnation() != want {
			t.Fatalf("restored incarnation = %q, want persisted %q", restored.Incarnation(), want)
		}
		data, err = Marshal(restored)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCurrentSnapshotJSONNormalizesMissingIncarnationWithoutSession(t *testing.T) {
	current, err := Marshal(restoreFixture(t, legacyFixture(t, "legacy-completed")))
	if err != nil {
		t.Fatal(err)
	}
	owner := json.RawMessage(`{"Issuer":"https://issuer.example","Subject":"subject","GrantType":"user","Name":"Owner"}`)
	data := withJSONFields(t, current, map[string]json.RawMessage{
		"owner":       owner,
		"incarnation": nil,
	})
	first := decodeSnapshot(t, data)
	second := decodeSnapshot(t, data)
	want := session.LegacyIncarnationID(first.ID, first.CreatedAt.UnixNano(), first.Owner)
	if first.Incarnation != want || second.Incarnation != want || !reflect.DeepEqual(first.Messages, second.Messages) {
		t.Fatal("current schema missing incarnation did not normalize deterministically")
	}
}

func TestSnapshotJSONNormalizationRejectsInvalidIncarnationWithoutMutatingDestination(t *testing.T) {
	current, err := Marshal(restoreFixture(t, legacyFixture(t, "legacy-completed")))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "legacy", data: legacyWithFields(t, map[string]json.RawMessage{"incarnation": json.RawMessage(`"corrupt"`)})},
		{name: "legacy before migration", data: legacyWithFields(t, map[string]json.RawMessage{
			"incarnation": json.RawMessage(`"corrupt"`),
			"messages":    json.RawMessage(`[{"role":"assistant","replay":{}}]`),
		})},
		{name: "current", data: withJSONFields(t, current, map[string]json.RawMessage{"incarnation": json.RawMessage(`"corrupt"`)})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			valid := decodeSnapshot(t, legacyFixture(t, "legacy-completed"))
			before := valid
			if err := json.Unmarshal(tc.data, &valid); err == nil || err.Error() != "sessnap: invalid incarnation" {
				t.Fatalf("invalid incarnation error = %v", err)
			}
			if !reflect.DeepEqual(valid, before) {
				t.Fatal("failed decode mutated destination")
			}
		})
	}
}

func TestSnapshotJSONRejectsMixedLegacyRelationshipBeforeNormalization(t *testing.T) {
	for _, relationship := range []string{
		`{"call_id":"provider-call","legacy_identity":{"provider_call_id":"schema-2"}}`,
		`{"legacy_identity":null}`,
		`{"LEGACY_IDENTITY":null}`,
	} {
		data := legacyWithFields(t, map[string]json.RawMessage{"relationship": json.RawMessage(relationship)})
		var snap Snapshot
		if err := json.Unmarshal(data, &snap); err == nil || err.Error() != "sessnap: legacy relationship contains schema-2 legacy_identity field" {
			t.Fatalf("mixed relationship error = %v", err)
		}
	}
}

func TestUnmarshalFormatRejectsOuterInnerSchemaMismatchBeforeNormalization(t *testing.T) {
	legacy := legacyWithFields(t, map[string]json.RawMessage{"incarnation": json.RawMessage(`"corrupt"`)})
	if _, err := UnmarshalFormat(legacy, Format); err == nil || err.Error() != `sessnap: format "sessnap-json/2" does not match identity schema 0` {
		t.Fatalf("/2 outer format for /1 input = %v", err)
	}
	current, err := Marshal(restoreFixture(t, legacyFixture(t, "legacy-completed")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UnmarshalFormat(current, "sessnap-json/1"); err == nil || err.Error() != `sessnap: format "sessnap-json/1" does not match identity schema 2` {
		t.Fatalf("/1 outer format for /2 input = %v", err)
	}
}
