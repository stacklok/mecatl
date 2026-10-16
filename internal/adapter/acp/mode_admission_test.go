package acp_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestACPModesFollowServerAdmission pins that ACP starts a session in the
// server's default mode and offers exactly the modes the server admits: the
// ceiling bounds the list, and auto appears only with an enforcing checker.
func TestACPModesFollowServerAdmission(t *testing.T) {
	for _, tc := range []struct {
		name        string
		def, ceil   session.PermissionMode
		checker     bool
		wantModes   []string
		wantCurrent string
	}{
		{"strict ceiling", session.ModeDefault, session.ModeAccept, false, []string{"plan", "default", "acceptEdits"}, "default"},
		{"yolo without checker", session.ModeYolo, session.ModeYolo, false, []string{"plan", "default", "acceptEdits", "yolo"}, "yolo"},
		{"auto with checker", session.ModeAuto, session.ModeAuto, true, []string{"plan", "default", "acceptEdits", "auto"}, "auto"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newServiceCfg(t, mockllm.New(), allowRules(), func(c *server.Config) {
				c.DefaultMode, c.MaxAllowedMode, c.EnforcingCheckerConfigured = tc.def, tc.ceil, tc.checker
			})
			e, cleanup := startAgent(t, svc)
			defer cleanup()
			res := e.call("session/new", map[string]any{"cwd": testCWD(t), "mcpServers": []any{}})
			var ns struct {
				SessionID string `json:"sessionId"`
				Modes     struct {
					CurrentModeID  string `json:"currentModeId"`
					AvailableModes []struct {
						ID string `json:"id"`
					} `json:"availableModes"`
				} `json:"modes"`
			}
			if err := json.Unmarshal(res, &ns); err != nil {
				t.Fatalf("session/new: %s: %v", res, err)
			}
			var got []string
			for _, m := range ns.Modes.AvailableModes {
				got = append(got, m.ID)
			}
			if !slices.Equal(got, tc.wantModes) || ns.Modes.CurrentModeID != tc.wantCurrent {
				t.Fatalf("modes = %v current %q, want %v current %q", got, ns.Modes.CurrentModeID, tc.wantModes, tc.wantCurrent)
			}
			last := tc.wantModes[len(tc.wantModes)-1]
			e.call("session/set_mode", map[string]any{"sessionId": ns.SessionID, "modeId": "plan"})
			e.call("session/set_mode", map[string]any{"sessionId": ns.SessionID, "modeId": last})
			sess, err := svc.GetSession(t.Context(), session.SessionID(ns.SessionID))
			if err != nil || string(sess.Mode) != last {
				t.Fatalf("set_mode %s: mode %v err %v", last, sess, err)
			}
		})
	}
}
