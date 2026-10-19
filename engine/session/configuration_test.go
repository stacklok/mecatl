package session

import (
	"reflect"
	"testing"
	"time"
)

func TestTightenConfigurationIsAtomicAndNeverWidens(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   PermissionMode
		limits Limits
		state  State
		ok     bool
	}{
		{"same", ModeDefault, Limits{10, 20, 3}, StateIdle, true},
		{"tighter", ModePlan, Limits{1, 2, 1}, StateIdle, true},
		{"wider mode", ModeAccept, Limits{1, 2, 1}, StateIdle, false},
		{"unknown mode", "other", Limits{1, 2, 1}, StateIdle, false},
		{"wider turns", ModePlan, Limits{11, 2, 1}, StateIdle, false},
		{"wider tools", ModePlan, Limits{1, 21, 1}, StateIdle, false},
		{"wider failures", ModePlan, Limits{1, 2, 4}, StateIdle, false},
		{"unlimited", ModePlan, Limits{}, StateIdle, false},
		{"negative", ModePlan, Limits{-1, 2, 1}, StateIdle, false},
		{"running", ModePlan, Limits{1, 2, 1}, StateRunning, false},
		{"completed", ModePlan, Limits{1, 2, 1}, StateCompleted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New("successor", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: "/workspace", Revision: "v1"}, Limits{10, 20, 3}, time.Unix(1, 0))
			s.State = tc.state
			before := *s
			err := s.TightenConfiguration(tc.mode, tc.limits)
			if (err == nil) != tc.ok {
				t.Fatalf("TightenConfiguration = %v, want success=%v", err, tc.ok)
			}
			if tc.ok {
				before.Mode, before.Limits = tc.mode, tc.limits
			}
			if !reflect.DeepEqual(*s, before) {
				t.Fatal("unexpected aggregate mutation")
			}
		})
	}
	s := New("unlimited", ModeAccept, EnvironmentRef{Kind: EnvKindLocal, ID: "/workspace", Revision: "v1"}, Limits{}, time.Unix(1, 0))
	if err := s.TightenConfiguration(ModeDefault, Limits{MaxTurns: 1}); err != nil {
		t.Fatal(err)
	}
	s.Counters.Turns = 1
	if err := s.TightenConfiguration(ModePlan, s.Limits); err == nil {
		t.Fatal("tightened an already-run session")
	}
}
