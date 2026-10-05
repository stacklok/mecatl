package keymap

import (
	"strings"
	"testing"
)

func TestParseUnknownAction(t *testing.T) {
	_, err := Parse(map[string][]string{"Bogus": {"ctrl+a"}})
	if err == nil {
		t.Fatalf("expected error for unknown action")
	}
}

func TestParseEmptyChord(t *testing.T) {
	_, err := Parse(map[string][]string{"Agents": {""}})
	if err == nil {
		t.Fatalf("expected error for empty chord")
	}
}

func TestParseAcceptsEditBack(t *testing.T) {
	res, err := Parse(map[string][]string{"EditBack": {"up"}})
	if err != nil {
		t.Fatalf("EditBack must be a valid action: %v", err)
	}
	if err := Validate(res); err != nil {
		t.Fatalf("EditBack=up must validate: %v", err)
	}
}

func TestValidateEditBackGlobalCollision(t *testing.T) {
	// EditBack is a globalOpen action: binding its chord to another global action is a
	// collision that must be rejected.
	res, err := Parse(map[string][]string{"EditBack": {"ctrl+q"}, "Submit": {"ctrl+q"}})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(res); err == nil {
		t.Fatalf("expected a global-scope collision for EditBack vs Submit")
	}
}

func TestValidateBareRuneOnGlobal(t *testing.T) {
	res, err := Parse(map[string][]string{"Agents": {"a"}})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(res); err == nil {
		t.Fatalf("expected bare rune rejection")
	}
}

func TestValidateOverlayCollision(t *testing.T) {
	res, err := Parse(map[string][]string{
		"Refresh": {"r"},
		"Tasks":   {"r"},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(res); err == nil {
		t.Fatalf("expected overlay collision rejection")
	}
}

func TestValidateGlobalCollision(t *testing.T) {
	res, err := Parse(map[string][]string{
		"Agents":    {"ctrl+a"},
		"Toolcalls": {"ctrl+a"},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(res); err == nil {
		t.Fatalf("expected global collision rejection")
	}
}

func TestValidateApprovalConsistency(t *testing.T) {
	res, err := Parse(map[string][]string{
		"Deny":   {"enter"},
		"Submit": {"enter"},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(res); err == nil {
		t.Fatalf("expected approval collision rejection")
	}
}

func TestValidateSubmitNewlineDistinct(t *testing.T) {
	res, err := Parse(map[string][]string{
		"Submit":  {"enter"},
		"Newline": {"enter"},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(res); err == nil {
		t.Fatalf("expected submit/newline collision rejection")
	}
}

// TestValidateQuitQuitDDistinct pins rule 6 (issue #504): the two independent
// double-press quit guards must never share a chord, or arming one would let the
// other confirm it.
func TestValidateQuitQuitDDistinct(t *testing.T) {
	res, err := Parse(map[string][]string{
		"Quit":  {"ctrl+x"},
		"QuitD": {"ctrl+x"},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(res); err == nil {
		t.Fatalf("expected quit/quitD collision rejection")
	}
}

// TestValidateNewActionsParse confirms the two new global actions are recognised
// and accepted with valid modified chords.
func TestValidateNewActionsParse(t *testing.T) {
	res, err := Parse(map[string][]string{
		"Suspend":     {"ctrl+z"},
		"QuitD":       {"ctrl+d"},
		"ClearPrompt": {"ctrl+u"},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(res); err != nil {
		t.Fatalf("validate ctrl+z/ctrl+d should pass: %v", err)
	}
}

func TestValidAgentsOverride(t *testing.T) {
	res, err := Parse(map[string][]string{"Agents": {"ctrl+\\"}})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(res); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

// TestValidateRawArgsRefreshDefaultCoexist pins rule 3b's negative arm: with
// NEITHER action explicitly rebound, both hold the default bare "r" in disjoint
// surfaces (the overlay vs the ask-args view) — that is NOT a collision.
func TestValidateRawArgsRefreshDefaultCoexist(t *testing.T) {
	res, err := Parse(map[string][]string{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(res); err != nil {
		t.Fatalf("no rebinds must validate (the shared default r is disjoint): %v", err)
	}
}

// TestValidateRawArgsVerdictRebindDisjoint pins rule 3c: inside the full-screen
// ask-args view RawArgs is consulted before the verdict keys, so an explicit
// RawArgs rebind overlapping a rebound Allow/AllowAlways/Deny chord would
// silently shadow that verdict INSIDE the view (where it is live) — reject it.
// The default RawArgs chord r overlaps none of the default a/w/d, and an
// ABSENT (non-rebound) verdict chord can't be shadowed: precedence applies
// only to what is rebound.
func TestValidateRawArgsVerdictRebindDisjoint(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   map[string][]string
		ok   bool
	}{
		{"rawargs onto the allow chord", map[string][]string{"RawArgs": {"a"}, "Allow": {"a"}}, false},
		{"rawargs onto the allow-always chord", map[string][]string{"RawArgs": {"w"}, "AllowAlways": {"w"}}, false},
		{"rawargs onto the deny chord", map[string][]string{"RawArgs": {"d"}, "Deny": {"d"}}, false},
		{"rawargs a, verdict defaults", map[string][]string{"RawArgs": {"a"}}, true},
		{"rawargs d, verdict defaults", map[string][]string{"RawArgs": {"d"}}, true},
		{"rawargs w, verdict defaults", map[string][]string{"RawArgs": {"w"}}, true},
		{"both rebound disjoint", map[string][]string{"RawArgs": {"ctrl+f20"}, "Deny": {"n"}}, true},
		{"verdict rebound onto the raw default", map[string][]string{"Deny": {"r"}}, true},
		{"defaults stay valid", map[string][]string{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Parse(tc.in)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			err = Validate(res)
			if tc.ok && err != nil {
				t.Fatalf("validate should pass: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("validate should reject the RawArgs/verdict overlap")
			}
		})
	}
}

// TestValidateRawArgsRefreshRebindDisjoint pins rule 3b: an explicit rebind of
// EITHER RawArgs or Refresh must keep the pair disjoint — rebinding Refresh to
// "r" while RawArgs still defaults to "r" (or vice versa) collides.
func TestValidateRawArgsRefreshRebindDisjoint(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   map[string][]string
		ok   bool
	}{
		{"rebind refresh onto the raw default", map[string][]string{"Refresh": {"r"}}, false},
		{"rebind rawargs onto the refresh default", map[string][]string{"RawArgs": {"r"}}, false},
		{"rebind both to the same chord", map[string][]string{"RawArgs": {"x"}, "Refresh": {"x"}}, false},
		{"rebind rawargs away, refresh stays default", map[string][]string{"RawArgs": {"ctrl+f20"}}, true},
		{"rebind refresh away, rawargs stays default", map[string][]string{"Refresh": {"ctrl+f21"}}, true},
		{"rebind both disjoint", map[string][]string{"RawArgs": {"ctrl+f20"}, "Refresh": {"ctrl+f21"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Parse(tc.in)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			err = Validate(res)
			if tc.ok && err != nil {
				t.Fatalf("validate should pass: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("validate should reject the RawArgs/Refresh overlap")
			}
		})
	}
}

func TestToolcallsValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   map[string][]string
		ok   bool
	}{
		{"defaults", map[string][]string{}, true},
		{"toolcalls overlaps rebound allow", map[string][]string{"Toolcalls": {"ctrl+t"}, "Allow": {"ctrl+t"}}, false},
		{"toolcalls overlaps default allow", map[string][]string{"Toolcalls": {"enter"}}, false},
		{"toolcalls overlaps default deny", map[string][]string{"Toolcalls": {"esc"}}, false},
		{"toolcalls overlaps default always allow", map[string][]string{"Toolcalls": {"w"}}, false},
		{"toolcalls has safe chord", map[string][]string{"Toolcalls": {"ctrl+f4"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Parse(tc.in)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if err := Validate(res); (err == nil) != tc.ok {
				t.Fatalf("validate = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestToolcallsValidationRejectsApprovalDetailAndNavigationCollisions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    map[string][]string
		other string
	}{
		{"RawArgs default", map[string][]string{"Toolcalls": {"r"}}, "RawArgs"},
		{"RawArgs rebind", map[string][]string{"Toolcalls": {"ctrl+f4"}, "RawArgs": {"ctrl+f4"}}, "RawArgs"},
		{"tab", map[string][]string{"Toolcalls": {"tab"}}, "Tab"},
		{"left", map[string][]string{"Toolcalls": {"left"}}, "Left"},
		{"right", map[string][]string{"Toolcalls": {"right"}}, "Right"},
		{"down", map[string][]string{"Toolcalls": {"down"}}, "Down"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Parse(tc.in)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			err = Validate(res)
			if err == nil || !strings.Contains(err.Error(), `"Toolcalls" and "`+tc.other+`"`) {
				t.Fatalf("Validate(%v) = %v, want Toolcalls/%s collision", tc.in, err, tc.other)
			}
		})
	}
}

func TestValidateConversationActionsAgainstDefaultGlobals(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   map[string][]string
	}{
		{"toolcalls overlaps default agents", map[string][]string{"Toolcalls": {"f6"}}},
		{"expand conversation overlaps default toolcalls", map[string][]string{"ExpandConversation": {"ctrl+t"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Parse(tc.in)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if err := Validate(res); err == nil {
				t.Fatal("validate must reject a conversation action that shadows a default global chord")
			}
		})
	}
}

func TestExpandConversationGlobalValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   map[string][]string
		ok   bool
	}{
		{"bare rune", map[string][]string{"ExpandConversation": {"x"}}, false},
		{"global collision", map[string][]string{"ExpandConversation": {"ctrl+f9"}, "Agents": {"ctrl+f9"}}, false},
		{"safe function key", map[string][]string{"ExpandConversation": {"f9"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Parse(tc.in)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if err := Validate(res); (err == nil) != tc.ok {
				t.Fatalf("validate = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestParseRejectsDeprecatedExpandTools(t *testing.T) {
	_, err := Parse(map[string][]string{"ExpandTools": {"ctrl+t"}})
	if err == nil || !strings.Contains(err.Error(), `unknown action "ExpandTools"`) {
		t.Fatalf("Parse deprecated alias error = %v, want unknown action", err)
	}
}
