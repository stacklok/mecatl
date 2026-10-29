package session_test

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestLearningExclusionIsIrreversible(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "workspace", Revision: "v1"}
	s := session.New("excluded", session.ModeDefault, ref, session.Limits{}, time.Unix(0, 0))
	if !s.LearningEligible() {
		t.Fatal("new session is ineligible")
	}
	s.ExcludeFromLearning()
	s.ExcludeFromLearning()
	if err := s.BeginTurn(); err != nil {
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
	if s.LearningEligible() {
		t.Fatal("later run or history replacement enabled learning")
	}
	child := session.New("fresh", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	if err := child.SeedHistory(session.ForkSnapshot(s.Conversation)); err != nil {
		t.Fatal(err)
	}
	if !child.LearningEligible() {
		t.Fatal("fresh session inherited exclusion")
	}
	if (&session.Session{}).LearningEligible() {
		t.Fatal("zero-value session is eligible")
	}
}
