package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// emptyMCPState is an MCP overlay state with a finished (non-loading) empty
// fetch — the "nothing to show" condition the caps-aware copy disambiguates.
func emptyMCPState(v mcpView) mcpState { return mcpState{view: v} }

// TestMCPEmptyStateCapsAware is the Option-C payoff: the SAME empty inventory
// reads "not enabled" when caps.MCP is false and "configured but empty" when
// caps.MCP is true, across the panel/resources/prompts views.
func TestMCPEmptyStateCapsAware(t *testing.T) {
	th := aztec()
	off := client.Capabilities{}         // MCP off
	on := client.Capabilities{MCP: true} // MCP on, but inventory empty

	cases := []struct {
		name    string
		view    mcpView
		offWant string
		onWant  string
	}{
		{"panel", mcpPanel, "MCP is not enabled on this server", "No MCP sources configured on this server"},
		{"resources", mcpResources, "MCP is not enabled on this server", "No resources advertised"},
		{"prompts", mcpPrompts, "MCP is not enabled on this server", "No prompts advertised"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			offOut := stripANSIstr(renderMCPOverlay(th, emptyMCPState(tc.view), off, defaultHelpKeys(), 100, 24))
			if !strings.Contains(offOut, tc.offWant) {
				t.Errorf("MCP off: want %q in:\n%s", tc.offWant, offOut)
			}
			// The off copy carries the remedy.
			if !strings.Contains(offOut, "Run a full mecated") {
				t.Errorf("MCP off copy should carry the remedy:\n%s", offOut)
			}
			onOut := stripANSIstr(renderMCPOverlay(th, emptyMCPState(tc.view), on, defaultHelpKeys(), 100, 24))
			if !strings.Contains(onOut, tc.onWant) {
				t.Errorf("MCP on-but-empty: want %q in:\n%s", tc.onWant, onOut)
			}
			if strings.Contains(onOut, "not enabled") {
				t.Errorf("MCP on-but-empty must NOT say 'not enabled':\n%s", onOut)
			}
		})
	}
}

// TestSkillsEmptyStateCapsAware mirrors the MCP empty-state payoff for the
// /skills panel: the SAME empty inventory reads "not enabled" (with a remedy)
// when caps.Skills is false and "none configured" when caps.Skills is true.
func TestSkillsEmptyStateCapsAware(t *testing.T) {
	th := aztec()
	off := client.Capabilities{}            // skills off
	on := client.Capabilities{Skills: true} // skills on, but inventory empty
	emptyPanel := skillsState{view: skillsPanel}

	offOut := stripANSIstr(renderSkillsOverlay(th, emptyPanel, off, defaultHelpKeys(), 100, 24))
	if !strings.Contains(offOut, "Skills are not enabled on this server") {
		t.Errorf("skills off: want 'not enabled' copy in:\n%s", offOut)
	}
	if !strings.Contains(offOut, "Run a mecated") {
		t.Errorf("skills off copy should carry the remedy:\n%s", offOut)
	}

	onOut := stripANSIstr(renderSkillsOverlay(th, emptyPanel, on, defaultHelpKeys(), 100, 24))
	if !strings.Contains(onOut, "No skills configured on this server") {
		t.Errorf("skills on-but-empty: want 'none configured' copy in:\n%s", onOut)
	}
	if strings.Contains(onOut, "not enabled") {
		t.Errorf("skills on-but-empty must NOT say 'not enabled':\n%s", onOut)
	}
}

// TestPaletteEmptyNoteNeutral asserts the "/" palette note is a single neutral
// "no matching command" (built-ins always exist, so the old caps-based "not
// enabled"/"none found" distinction is gone), and is empty for a non-command
// input or while dismissed. The note is independent of caps.
func TestPaletteEmptyNoteNeutral(t *testing.T) {
	th := aztec()
	var st paletteState // closed, no rows

	// caps OFF, a "/" command line with no matching rows → neutral note.
	off := stripANSIstr(renderPalette(th, st, client.Capabilities{}, "/", 100))
	if !strings.Contains(off, "no matching command") {
		t.Errorf("palette note should be neutral 'no matching command' (caps off):\n%s", off)
	}
	if strings.Contains(off, "not enabled") {
		t.Errorf("palette note must NOT carry the old caps-based 'not enabled' copy:\n%s", off)
	}
	// caps ON, an unmatched prefix → same neutral note.
	on := stripANSIstr(renderPalette(th, st, client.Capabilities{SlashCommands: true}, "/foo", 100))
	if !strings.Contains(on, "no matching command") {
		t.Errorf("palette note should be neutral 'no matching command' (caps on):\n%s", on)
	}
	// Non-command input: no note.
	if renderPalette(th, st, client.Capabilities{}, "hello", 100) != "" {
		t.Errorf("palette should render nothing for a non-command input")
	}
	// Dismissed: no note even on a command line.
	dismissed := paletteState{dismissed: true}
	if renderPalette(th, dismissed, client.Capabilities{}, "/", 100) != "" {
		t.Errorf("palette should render nothing while dismissed")
	}
}
