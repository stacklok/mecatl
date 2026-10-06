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

const (
	actionSubmit             = "Submit"
	actionNewline            = "Newline"
	actionCancel             = "Cancel"
	actionClearPrompt        = "ClearPrompt"
	actionEditBack           = "EditBack"
	actionPaste              = "Paste"
	actionSelectAll          = "SelectAll"
	actionCopySelection      = "CopySelection"
	actionQuit               = "Quit"
	actionQuitD              = "QuitD"
	actionSuspend            = "Suspend"
	actionAllow              = "Allow"
	actionAllowAlways        = "AllowAlways"
	actionDeny               = "Deny"
	actionScrollU            = "ScrollU"
	actionScrollD            = "ScrollD"
	actionScrollTop          = "ScrollTop"
	actionScrollBottom       = "ScrollBottom"
	actionModeSwitch         = "ModeSwitch"
	actionMCPPanel           = "MCPPanel"
	actionResources          = "Resources"
	actionPrompts            = "Prompts"
	actionUp                 = "Up"
	actionDown               = "Down"
	actionChoose             = "Choose"
	actionClose              = "Close"
	actionRefresh            = "Refresh"
	actionTasks              = "Tasks"
	actionFindings           = "Findings"
	actionJumpTop            = "JumpTop"
	actionJumpEnd            = "JumpEnd"
	actionAgents             = "Agents"
	actionToolcalls          = "Toolcalls"
	actionExpandConversation = "ExpandConversation"
	actionNextTab            = "NextTab"
	actionCancelChild        = "CancelChild"
	actionHelp               = "Help"
	actionEffort             = "Effort"
	actionSetGlobalDefault   = "SetGlobalDefault"
	actionRawArgs            = "RawArgs"
)

// validActions is the public action name contract (keyMap struct field names as strings).
var validActions = map[string]struct{}{
	actionSubmit: {}, actionNewline: {}, actionCancel: {}, actionClearPrompt: {},
	actionEditBack: {}, actionPaste: {}, actionSelectAll: {}, actionCopySelection: {},
	actionQuit: {}, actionQuitD: {}, actionSuspend: {}, actionAllow: {},
	actionAllowAlways: {}, actionDeny: {}, actionScrollU: {}, actionScrollD: {},
	actionScrollTop: {}, actionScrollBottom: {}, actionModeSwitch: {}, actionMCPPanel: {},
	actionResources: {}, actionPrompts: {}, actionUp: {}, actionDown: {},
	actionChoose: {}, actionClose: {}, actionRefresh: {}, actionTasks: {},
	actionFindings: {}, actionJumpTop: {}, actionJumpEnd: {}, actionAgents: {},
	actionToolcalls: {}, actionExpandConversation: {}, actionNextTab: {},
	actionCancelChild: {}, actionHelp: {}, actionEffort: {}, actionSetGlobalDefault: {},
	actionRawArgs: {},
}

// scope membership per action.
var (
	globalOpen = map[string]struct{}{
		actionSubmit: {}, actionNewline: {}, actionCancel: {}, actionClearPrompt: {},
		actionEditBack: {}, actionPaste: {}, actionSelectAll: {}, actionCopySelection: {},
		actionQuit: {}, actionQuitD: {}, actionSuspend: {}, actionScrollU: {},
		actionScrollD: {}, actionScrollTop: {}, actionScrollBottom: {}, actionModeSwitch: {},
		actionMCPPanel: {}, actionResources: {}, actionPrompts: {}, actionAgents: {},
		actionToolcalls: {}, actionExpandConversation: {}, actionHelp: {}, actionEffort: {},
	}
	overlayInternal = map[string]struct{}{
		actionUp: {}, actionDown: {}, actionChoose: {}, actionClose: {}, actionRefresh: {},
		actionTasks: {}, actionFindings: {}, actionJumpTop: {}, actionJumpEnd: {},
		actionNextTab: {}, actionCancelChild: {}, actionRawArgs: {},
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
	// Toolcalls is also live in the approval modal. Validate the modal's
	// first-consumed detail and focus keys before generic global rules so their
	// collision diagnostics identify the shadowed approval action.
	if err := validateToolcallsApprovalKeys(res); err != nil {
		return err
	}
	// 1) Reject bare printable runes on globalOpen actions.
	if err := validateGlobalBarePrintableRunes(res); err != nil {
		return err
	}
	// 2) Reject collisions within overlayInternal scope.
	if err := rejectScopeCollisions(res, overlayInternal, "overlay-internal"); err != nil {
		return err
	}
	// 3) Reject collisions within globalOpen scope.
	if err := rejectScopeCollisions(res, globalOpen, "global"); err != nil {
		return err
	}
	// 3d) Toolcalls is also live in the approval modal; report verdict
	// collisions before general global collisions for a useful diagnostic.
	if err := validateToolcallsVerdicts(res); err != nil {
		return err
	}
	if err := validateConversationDefaultGlobalCollisions(res); err != nil {
		return err
	}
	// 3b) RawArgs and Refresh share default chord r in disjoint surfaces; an
	// explicit rebind of either must keep them disjoint. With both at their
	// defaults the two surfaces never coexist (an overlay never owns the keyboard
	// while the permission modal's full-screen args view is open), so the
	// overlay-internal scope check above deliberately does not fire on the shared
	// default — only an operator rebind that re-overlaps the pair is rejected.
	_, rawRebound := res.ByAction[actionRawArgs]
	_, refreshRebound := res.ByAction[actionRefresh]
	if rawRebound || refreshRebound {
		raw := res.ByAction[actionRawArgs]
		if len(raw) == 0 {
			raw = []string{"r"}
		}
		refresh := res.ByAction[actionRefresh]
		if len(refresh) == 0 {
			refresh = []string{"r"}
		}
		if err := rejectPairOverlap(raw, refresh, actionRawArgs, actionRefresh); err != nil {
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
		for _, verdict := range []string{actionAllow, actionAllowAlways, actionDeny} {
			if err := rejectPairOverlap(res.ByAction[actionRawArgs], res.ByAction[verdict], actionRawArgs, verdict); err != nil {
				return err
			}
		}
	}
	// 4) Approval consistency: Deny must not collide with Allow/AllowAlways/Submit/Cancel.
	deny := res.ByAction[actionDeny]
	for _, action := range []string{actionAllow, actionAllowAlways, actionSubmit, actionCancel} {
		if err := rejectPairOverlap(deny, res.ByAction[action], actionDeny, action); err != nil {
			return err
		}
	}
	// 5) Submit vs Newline distinct.
	if err := rejectPairOverlap(res.ByAction[actionSubmit], res.ByAction[actionNewline], actionSubmit, actionNewline); err != nil {
		return err
	}
	// 6) Quit vs QuitD distinct: both are independent double-press guards, so sharing
	// a chord would arm one and confirm the other (an armed ctrl+c confirmed by
	// ctrl+d) — the two quit keys must never share a chord.
	return rejectPairOverlap(res.ByAction[actionQuit], res.ByAction[actionQuitD], actionQuit, actionQuitD)
}

func validateGlobalBarePrintableRunes(res Resolved) error {
	for action, chords := range res.ByAction {
		if _, isGlobal := globalOpen[action]; !isGlobal {
			continue
		}
		for _, chord := range chords {
			if isBarePrintableRune(chord) {
				return fmt.Errorf("bare printable chord %q not allowed for global action %q", chord, action)
			}
		}
	}
	return nil
}

func validateConversationDefaultGlobalCollisions(res Resolved) error {
	defaults := map[string][]string{
		actionSubmit: {"enter"}, actionNewline: {"shift+enter", "ctrl+j", "ctrl+enter", "alt+enter"},
		actionCancel: {"esc"}, actionClearPrompt: {"ctrl+u"}, actionEditBack: {"up"},
		actionPaste: {"ctrl+v"}, actionSelectAll: {"ctrl+g"}, actionCopySelection: {"ctrl+y"},
		actionQuit: {"ctrl+c"}, actionQuitD: {"ctrl+d"}, actionSuspend: {"ctrl+z"},
		actionScrollU: {"pgup"}, actionScrollD: {"pgdown"}, actionScrollTop: {"home"}, actionScrollBottom: {"end"},
		actionModeSwitch: {"shift+tab"}, actionMCPPanel: {"ctrl+o"}, actionResources: {"ctrl+r"},
		actionPrompts: {"f8"}, actionAgents: {"f6"}, actionEffort: {"f7"},
		actionToolcalls: {"ctrl+t"}, actionExpandConversation: {"f9"}, actionHelp: {"?"},
	}
	for _, action := range []string{actionToolcalls, actionExpandConversation} {
		chords := res.ByAction[action]
		if len(chords) == 0 {
			chords = defaults[action]
		}
		for other, fallback := range defaults {
			if other == action {
				continue
			}
			effective := res.ByAction[other]
			if len(effective) == 0 {
				effective = fallback
			}
			if err := rejectPairOverlap(chords, effective, action, other); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateToolcallsApprovalKeys(res Resolved) error {
	toolcalls := res.ByAction[actionToolcalls]
	if len(toolcalls) == 0 {
		toolcalls = []string{"ctrl+t"}
	}
	rawArgs := res.ByAction[actionRawArgs]
	if len(rawArgs) == 0 {
		rawArgs = []string{"r"}
	}
	if err := rejectPairOverlap(toolcalls, rawArgs, actionToolcalls, actionRawArgs); err != nil {
		return err
	}
	for _, key := range []struct {
		action string
		chord  string
	}{
		{"Tab", "tab"},
		{"Left", "left"},
		{"Right", "right"},
		{actionDown, "down"},
	} {
		if err := rejectPairOverlap(toolcalls, []string{key.chord}, actionToolcalls, key.action); err != nil {
			return err
		}
	}
	return nil
}

func validateToolcallsVerdicts(res Resolved) error {
	toolcalls := res.ByAction[actionToolcalls]
	if len(toolcalls) == 0 {
		toolcalls = []string{"ctrl+t"}
	}
	for verdict, defaults := range map[string][]string{
		actionAllow:       {"a", "y", "enter"},
		actionAllowAlways: {"w"},
		actionDeny:        {"d", "n", "esc"},
	} {
		chords := res.ByAction[verdict]
		if len(chords) == 0 {
			chords = defaults
		}
		if err := rejectPairOverlap(toolcalls, chords, actionToolcalls, verdict); err != nil {
			return err
		}
	}
	return nil
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
