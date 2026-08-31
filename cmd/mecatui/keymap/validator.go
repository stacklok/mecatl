// Package keymap validates rebindable-key overrides for mecatui.
// It provides action-name normalization and collision detection across key scopes.
package keymap

import (
	"fmt"
	"sort"
	"strings"
)

// Resolved holds a normalised mapping of action name -> chord strings (as provided),
// lowercased and trimmed. Bubble Tea chord parsing is deferred to the UI; the validator
// enforces collisions and bare-rune rules here.
// Unknown actions are rejected by Parse.
type Resolved struct {
	ByAction map[string][]string
}

// validActions is the public action name contract (keyMap struct field names as strings).
var validActions = map[string]struct{}{
	"Submit":           {},
	"Newline":          {},
	"Cancel":           {},
	"ClearPrompt":      {},
	"EditBack":         {},
	"Paste":            {},
	"SelectAll":        {},
	"CopySelection":    {},
	"Quit":             {},
	"QuitD":            {},
	"Suspend":          {},
	"Allow":            {},
	"AllowAlways":      {},
	"Deny":             {},
	"ScrollU":          {},
	"ScrollD":          {},
	"ScrollTop":        {},
	"ScrollBottom":     {},
	"ModeSwitch":       {},
	"MCPPanel":         {},
	"Resources":        {},
	"Prompts":          {},
	"Up":               {},
	"Down":             {},
	"Choose":           {},
	"Close":            {},
	"Refresh":          {},
	"Tasks":            {},
	"Findings":         {},
	"JumpTop":          {},
	"JumpEnd":          {},
	"Agents":           {},
	"NextTab":          {},
	"CancelChild":      {},
	"ExpandTools":      {},
	"Help":             {},
	"Effort":           {},
	"SetGlobalDefault": {},
	"RawArgs":          {},
}

// scope membership per action.
var (
	globalOpen = map[string]struct{}{
		"Submit": {}, "Newline": {}, "Cancel": {}, "ClearPrompt": {}, "EditBack": {}, "Paste": {}, "SelectAll": {}, "CopySelection": {}, "Quit": {}, "QuitD": {},
		"Suspend": {},
		"ScrollU": {}, "ScrollD": {}, "ScrollTop": {}, "ScrollBottom": {},
		"ModeSwitch": {}, "MCPPanel": {}, "Resources": {}, "Prompts": {},
		"Agents": {}, "ExpandTools": {}, "Help": {}, "Effort": {},
	}
	overlayInternal = map[string]struct{}{
		"Up": {}, "Down": {}, "Choose": {}, "Close": {}, "Refresh": {}, "Tasks": {}, "Findings": {},
		"JumpTop": {}, "JumpEnd": {}, "NextTab": {}, "CancelChild": {}, "RawArgs": {},
	}
)

// Parse normalises the input map, rejecting unknown action names and empty chords.
func Parse(in map[string][]string) (Resolved, error) {
	out := Resolved{ByAction: make(map[string][]string, len(in))}
	for action, chords := range in {
		if _, ok := validActions[action]; !ok {
			return Resolved{}, fmt.Errorf("unknown action %q", action)
		}
		norm := make([]string, 0, len(chords))
		seen := make(map[string]struct{}, len(chords))
		for _, c := range chords {
			c = strings.TrimSpace(strings.ToLower(c))
			if c == "" {
				return Resolved{}, fmt.Errorf("empty chord for action %q", action)
			}
			if _, dup := seen[c]; dup {
				// de-duplicate within the action
				continue
			}
			seen[c] = struct{}{}
			norm = append(norm, c)
		}
		if len(norm) == 0 {
			return Resolved{}, fmt.Errorf("no valid chords for action %q", action)
		}
		out.ByAction[action] = norm
	}
	return out, nil
}

// Validate enforces invariant rules over the resolved mapping.
func Validate(res Resolved) error {
	// 1) Reject bare printable runes on globalOpen actions.
	for action, chords := range res.ByAction {
		if _, isGlobal := globalOpen[action]; isGlobal {
			for _, c := range chords {
				if isBarePrintableRune(c) {
					return fmt.Errorf("bare printable chord %q not allowed for global action %q", c, action)
				}
			}
		}
	}
	// 2) Reject collisions within overlayInternal scope.
	if err := rejectScopeCollisions(res, overlayInternal, "overlay-internal"); err != nil {
		return err
	}
	// 3) Reject collisions within globalOpen scope.
	if err := rejectScopeCollisions(res, globalOpen, "global"); err != nil {
		return err
	}
	// 3b) RawArgs and Refresh share default chord r in disjoint surfaces; an
	// explicit rebind of either must keep them disjoint. With both at their
	// defaults the two surfaces never coexist (an overlay never owns the keyboard
	// while the permission modal's full-screen args view is open), so the
	// overlay-internal scope check above deliberately does not fire on the shared
	// default — only an operator rebind that re-overlaps the pair is rejected.
	_, rawRebound := res.ByAction["RawArgs"]
	_, refreshRebound := res.ByAction["Refresh"]
	if rawRebound || refreshRebound {
		raw := res.ByAction["RawArgs"]
		if len(raw) == 0 {
			raw = []string{"r"}
		}
		refresh := res.ByAction["Refresh"]
		if len(refresh) == 0 {
			refresh = []string{"r"}
		}
		if err := rejectPairOverlap(raw, refresh, "RawArgs", "Refresh"); err != nil {
			return err
		}
	}
	// 3c) RawArgs is consulted FIRST inside the full-screen ask-args view
	// (onApprovalKey checks it before the verdict keys), so an explicit RawArgs
	// rebind that overlaps a rebound Allow/AllowAlways/Deny chord would silently
	// shadow that verdict INSIDE the view — a surface where those verdicts are
	// live. Reject the overlap; absent chords can't shadow anything (precedence
	// applies only to what is rebound), and the default r collides with none of
	// the default a/w/d.
	if rawRebound {
		for _, verdict := range []string{"Allow", "AllowAlways", "Deny"} {
			if err := rejectPairOverlap(res.ByAction["RawArgs"], res.ByAction[verdict], "RawArgs", verdict); err != nil {
				return err
			}
		}
	}
	// 4) Approval consistency: Deny must not collide with Allow/AllowAlways/Submit/Cancel.
	deny := res.ByAction["Deny"]
	for _, a := range []string{"Allow", "AllowAlways", "Submit", "Cancel"} {
		if err := rejectPairOverlap(deny, res.ByAction[a], "Deny", a); err != nil {
			return err
		}
	}
	// 5) Submit vs Newline distinct.
	if err := rejectPairOverlap(res.ByAction["Submit"], res.ByAction["Newline"], "Submit", "Newline"); err != nil {
		return err
	}
	// 6) Quit vs QuitD distinct: both are independent double-press guards, so sharing
	// a chord would arm one and confirm the other (an armed ctrl+c confirmed by
	// ctrl+d) — the two quit keys must never share a chord.
	return rejectPairOverlap(res.ByAction["Quit"], res.ByAction["QuitD"], "Quit", "QuitD")
}

// isBarePrintableRune reports true for a single-rune chord (length==1).
// This is a conservative detector: single characters like "a", "?", "/" count as bare.
// Multi-word chords like "ctrl+a", function keys ("f5"), arrows ("up"), and specials
// ("home","end","pgup","pgdown","tab","enter","esc") are not bare.
func isBarePrintableRune(chord string) bool {
	// single visible ASCII rune: letters, digits, punctuation — treat as bare
	if len(chord) == 1 {
		return true
	}
	// Explicit non-bare specials (lowercased already)
	specials := map[string]struct{}{
		"up": {}, "down": {}, "left": {}, "right": {},
		"home": {}, "end": {}, "pgup": {}, "pgdown": {}, "tab": {},
		"enter": {}, "esc": {}, "space": {},
	}
	if _, ok := specials[chord]; ok {
		return false
	}
	// Modifier or function key pattern
	if strings.Contains(chord, "+") {
		return false
	}
	if strings.HasPrefix(chord, "f") {
		// f1..f12 assumed function keys
		digits := strings.TrimPrefix(chord, "f")
		if digits != "" {
			return false
		}
	}
	// Default: treat unknown multi-char strings as non-bare
	return false
}

func rejectScopeCollisions(res Resolved, scope map[string]struct{}, scopeName string) error {
	seen := make(map[string]string)
	for action := range scope {
		chords, ok := res.ByAction[action]
		if !ok {
			continue
		}
		for _, c := range chords {
			if other, dup := seen[c]; dup {
				// same chord bound to two actions in the same scope
				pair := []string{other, action}
				sort.Strings(pair)
				return fmt.Errorf("collision: chord %q shared by %s actions %q and %q", c, scopeName, pair[0], pair[1])
			}
			seen[c] = action
		}
	}
	return nil
}

func rejectPairOverlap(a, b []string, nameA, nameB string) error {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(a))
	for _, c := range a {
		set[c] = struct{}{}
	}
	for _, c := range b {
		if _, ok := set[c]; ok {
			return fmt.Errorf("collision: chord %q shared by %q and %q", c, nameA, nameB)
		}
	}
	return nil
}
