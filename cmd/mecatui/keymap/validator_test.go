package keymap

import "testing"

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
		"Agents":      {"ctrl+a"},
		"ExpandTools": {"ctrl+a"},
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
		"Suspend": {"ctrl+z"},
		"QuitD":   {"ctrl+d"},
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
