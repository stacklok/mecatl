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

func TestTightenConfigurationRanksAutoAndYolo(t *testing.T) {
	cases := []struct {
		name   string
		stored PermissionMode
		mode   PermissionMode
		ok     bool
	}{
		{"yolo to yolo", ModeYolo, ModeYolo, true},
		{"auto to auto", ModeAuto, ModeAuto, true},
		{"yolo to auto", ModeYolo, ModeAuto, true},
		{"yolo to plan", ModeYolo, ModePlan, true},
		{"auto to acceptEdits", ModeAuto, ModeAccept, true},
		{"auto to yolo", ModeAuto, ModeYolo, false},
		{"acceptEdits to auto", ModeAccept, ModeAuto, false},
		{"default to yolo", ModeDefault, ModeYolo, false},
		{"unknown target", ModeYolo, PermissionMode("bogus"), false},
		{"unknown stored", PermissionMode("bogus"), ModePlan, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New("successor", tc.stored, EnvironmentRef{Kind: EnvKindLocal, ID: "/workspace", Revision: "v1"}, Limits{10, 20, 3}, time.Unix(1, 0))
			if err := s.TightenConfiguration(tc.mode, s.Limits); (err == nil) != tc.ok {
				t.Fatalf("TightenConfiguration(%q -> %q) = %v, want success=%v", tc.stored, tc.mode, err, tc.ok)
			}
		})
	}
}

func TestPermissionModeRank(t *testing.T) {
	order := []PermissionMode{ModePlan, ModeDefault, ModeAccept, ModeAuto, ModeYolo}
	for i, m := range order {
		if got := m.Rank(); got != i+1 {
			t.Fatalf("%q.Rank() = %d, want %d", m, got, i+1)
		}
	}
	for _, m := range []PermissionMode{"", "bogus", "trusted"} {
		if got := m.Rank(); got != 0 {
			t.Fatalf("%q.Rank() = %d, want 0", m, got)
		}
	}
}
