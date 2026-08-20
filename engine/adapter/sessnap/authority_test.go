package sessnap_test

import (
	"bytes"
	"encoding/json"
	"iter"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

func TestADR_0233_AuthorityEvaluator_Scenario2_SetRoundTripsThroughSnapshotAndFold(t *testing.T) {
	t.Parallel()
	bound := authorityBinding(t)
	s := session.New("authority-round-trip", session.ModeDefault, "/workspace", session.Limits{}, time.Unix(1, 0).UTC())
	if err := s.BindAuthority(bound); err != nil {
		t.Fatalf("BindAuthority: %v", err)
	}

	restored, err := sessnap.Unmarshal(mustMarshal(t, s))
	if err != nil {
		t.Fatalf("snapshot restore: %v", err)
	}
	assertAuthorityBinding(t, restored, bound)

	folded, err := eventsource.Fold(eventsource.SessionMeta{
		ID: "authority-round-trip", Mode: session.ModeDefault, Workspace: "/workspace",
		CreatedAt: time.Unix(1, 0).UTC(), Authority: &bound,
	}, emptySeq())
	if err != nil {
		t.Fatalf("event fold: %v", err)
	}
	assertAuthorityBinding(t, folded, bound)
}

func TestADR_0233_AuthorityEvaluator_Scenario2_PayloadExcludesSensitiveRuntimeData(t *testing.T) {
	t.Parallel()
	bound := authorityBinding(t)
	s := session.New("authority-payload", session.ModeDefault, "/workspace/secret", session.Limits{}, time.Unix(1, 0).UTC())
	if err := s.BindAuthority(bound); err != nil {
		t.Fatalf("BindAuthority: %v", err)
	}

	line := mustMarshal(t, s)
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(line, &payload); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	authority, ok := payload["authority"]
	if !ok {
		t.Fatal("snapshot omitted bound authority")
	}
	for _, forbidden := range []string{"path", "credential", "token", "header", "catalog", "runner", "issuer", "subject", "/workspace", "secret"} {
		if bytes.Contains(bytes.ToLower(authority), []byte(forbidden)) {
			t.Errorf("authority payload leaked forbidden runtime data %q: %s", forbidden, authority)
		}
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario2_UndecodableSetFailsClosedLoudly(t *testing.T) {
	t.Parallel()
	line := []byte(`{"id":"malformed","state":"idle","mode":"default","limits":{},"counters":{},"workspace":"/workspace","created_at":"1970-01-01T00:00:01Z","authority":{"capability_set":{"tools":"not-a-list"},"provenance":"derived","definition_identity":"explicit:reviewer"}}`)
	if _, err := sessnap.Unmarshal(line); err == nil || !strings.Contains(err.Error(), "authority") {
		t.Fatalf("malformed claimed authority error = %v, want named authority failure", err)
	}

	legacy := []byte(`{"id":"legacy","state":"idle","mode":"default","limits":{},"counters":{},"workspace":"/workspace","created_at":"1970-01-01T00:00:01Z"}`)
	restored, err := sessnap.Unmarshal(legacy)
	if err != nil {
		t.Fatalf("legacy snapshot restore: %v", err)
	}
	if _, bound := restored.BoundAuthority(); bound {
		t.Fatal("pre-feature snapshot restored as bound")
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario2_NoSilentBoundButEmptyState(t *testing.T) {
	t.Parallel()
	line := []byte(`{"id":"empty","state":"idle","mode":"default","limits":{},"counters":{},"workspace":"/workspace","created_at":"1970-01-01T00:00:01Z","authority":{"capability_set":{"tools":"not-a-list"},"provenance":"derived","definition_identity":"explicit:reviewer"}}`)
	restored, err := sessnap.Unmarshal(line)
	if err == nil {
		t.Fatal("malformed authority restored successfully")
	}
	if restored != nil {
		t.Fatal("malformed authority returned a bound-but-empty session")
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario2_RestoreRejectsIncompleteAuthorityClaims(t *testing.T) {
	t.Parallel()
	const snapshotPrefix = `{"id":"incomplete","state":"idle","mode":"default","limits":{},"counters":{},"workspace":"/workspace","created_at":"1970-01-01T00:00:01Z",`
	for name, authority := range map[string]string{
		"null authority claim":                    `null`,
		"authority object without capability set": `{}`,
		"null capability set":                     `{"capability_set":null,"provenance":"derived"}`,
		"empty capability set":                    `{"capability_set":{},"provenance":"derived"}`,
		"malformed capability set":                `{"capability_set":{"tools":"not-a-list"},"provenance":"derived"}`,
	} {
		t.Run(name, func(t *testing.T) {
			restored, err := sessnap.Unmarshal([]byte(snapshotPrefix + `"authority":` + authority + `}`))
			if err == nil {
				t.Fatal("incomplete authority claim restored successfully")
			}
			if restored != nil {
				t.Fatal("incomplete authority claim returned a session")
			}
		})
	}

	legacy, err := sessnap.Unmarshal([]byte(snapshotPrefix + `"profile":"default"}`))
	if err != nil {
		t.Fatalf("legacy snapshot restore: %v", err)
	}
	if _, bound := legacy.BoundAuthority(); bound {
		t.Fatal("authority-absent legacy snapshot restored as bound")
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario2_TerminalRecoveryPreservesSet(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		terminal func(*session.Session) error
		recover  func(*session.Session) error
	}{
		{name: "completed/reopen", terminal: (*session.Session).Complete, recover: (*session.Session).Reopen},
		{name: "cancelled/interrupt", terminal: (*session.Session).Cancel, recover: (*session.Session).Interrupt},
		{name: "failed/recover", terminal: (*session.Session).Fail, recover: (*session.Session).Recover},
		{name: "running/abandon", terminal: func(*session.Session) error { return nil }, recover: (*session.Session).Abandon},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := session.New("authority-recovery", session.ModeDefault, "/workspace", session.Limits{}, time.Unix(1, 0).UTC())
			bound := authorityBinding(t)
			if err := s.BindAuthority(bound); err != nil {
				t.Fatalf("BindAuthority: %v", err)
			}
			if tc.name == "running/abandon" {
				if err := s.BeginTurn(); err != nil {
					t.Fatalf("BeginTurn: %v", err)
				}
			}
			if err := tc.terminal(s); err != nil {
				t.Fatalf("terminal transition: %v", err)
			}
			if err := tc.recover(s); err != nil {
				t.Fatalf("recovery: %v", err)
			}
			assertAuthorityBinding(t, s, bound)
		})
	}
}

func emptySeq() iter.Seq2[session.Event, error] {
	return func(func(session.Event, error) bool) {}
}

func authorityBinding(t *testing.T) session.Authority {
	t.Helper()
	return session.Authority{
		CapabilitySet:      governance.CapabilitySet{Tools: []string{"Read", "Grep"}, RemainingDelegationDepth: 2, FileSystem: true},
		Provenance:         "derived",
		DefinitionIdentity: "explicit:reviewer",
	}
}

func assertAuthorityBinding(t *testing.T, s *session.Session, want session.Authority) {
	t.Helper()
	got, bound := s.BoundAuthority()
	if !bound {
		t.Fatal("session authority is not bound")
	}
	if !reflect.DeepEqual(got.CapabilitySet, want.CapabilitySet) || got.Provenance != want.Provenance || got.DefinitionIdentity != want.DefinitionIdentity {
		t.Fatalf("authority = %+v, want %+v", got, want)
	}
}
