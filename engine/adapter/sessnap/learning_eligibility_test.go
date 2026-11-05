package sessnap

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestLearningEligibilityPersistsAcrossSnapshotLifecycle(t *testing.T) {
	for _, history := range []string{`null`, `[]`, `[{"role":"user","text":"old prompt"}]`} {
		for _, storedIncarnation := range []bool{false, true} {
			fields := map[string]json.RawMessage{"messages": json.RawMessage(history), "learning_eligible": json.RawMessage(`true`)}
			if storedIncarnation {
				encoded, err := json.Marshal(session.NewIncarnationID())
				if err != nil {
					t.Fatal(err)
				}
				fields["incarnation"] = encoded
			}
			data := withJSONFields(t, []byte(`{"id":"legacy-learning","state":"idle","environment_ref":{"Kind":"mem","ID":"workspace","Revision":"v1"}}`), fields)
			s := restoreFixture(t, data)
			if s.LearningEligible() {
				t.Fatal("legacy input enabled learning")
			}
			assertExcludedSnapshotLifecycle(t, s)
		}
	}
	s := restoreFixture(t, legacyFixture(t, "legacy-completed"))
	if err := s.Reopen(); err != nil {
		t.Fatal(err)
	}
	assertExcludedSnapshotLifecycle(t, s)
}

func assertExcludedSnapshotLifecycle(t *testing.T, s *session.Session) {
	t.Helper()
	for range 2 {
		if err := s.BeginTurn(); err != nil {
			t.Fatal(err)
		}
		s.BeginRun("later-current-run")
		if err := s.RecordAssistant(session.NewAssistantMessage("current answer", nil)); err != nil {
			t.Fatal(err)
		}
		if err := s.Complete(); err != nil {
			t.Fatal(err)
		}
		if err := s.Reopen(); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceHistoryAtBoundary([]session.Message{session.NewAssistantMessage("compacted", nil)}); err != nil {
			t.Fatal(err)
		}
		snap, err := Of(s)
		if err != nil {
			t.Fatal(err)
		}
		if snap.LearningEligible {
			t.Fatal("save enabled learning")
		}
		s, err = snap.Restore()
		if err != nil {
			t.Fatal(err)
		}
		wire, err := Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		s, err = UnmarshalFormat(wire, Format)
		if err != nil {
			t.Fatal(err)
		}
		if s.LearningEligible() {
			t.Fatal("later run, compaction, or reload enabled learning")
		}
	}
}

func TestCurrentLearningEligibilityRoundTripAndMissingFailClosed(t *testing.T) {
	s := session.New("current-learning", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "workspace", Revision: "v1"}, session.Limits{}, time.Unix(0, 0))
	for _, eligible := range []bool{true, false} {
		if !eligible {
			s.ExcludeFromLearning()
		}
		for range 2 {
			snap, err := Of(s)
			if err != nil {
				t.Fatal(err)
			}
			direct, err := snap.Restore()
			if err != nil {
				t.Fatal(err)
			}
			wire, err := Marshal(direct)
			if err != nil {
				t.Fatal(err)
			}
			s = restoreFixture(t, wire)
			if s.LearningEligible() != eligible {
				t.Fatal("current eligibility changed on restore")
			}
			missing := withJSONFields(t, wire, map[string]json.RawMessage{"learning_eligible": nil})
			if restoreFixture(t, missing).LearningEligible() {
				t.Fatal("missing eligibility defaulted to true")
			}
		}
	}
}
