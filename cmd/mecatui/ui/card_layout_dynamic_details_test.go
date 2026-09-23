package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// TestMecatuiCardLayout_Scenario3_DynamicDetailsFitWidth verifies AC3.2: every
// server-derived detail row is terminal-sanitized and wrapped before its style
// and the centred card frame are applied.
func TestMecatuiCardLayout_Scenario3_DynamicDetailsFitWidth(t *testing.T) {
	const width = 32
	const maxCardWidth = 106 // cardTextWidth's 100-column cap plus askCard chrome
	long := strings.Repeat("dynamic-detail-value-", 8) + "\x1b[2J"
	th, hk := aztec(), defaultHelpKeys()
	assertFits := func(t *testing.T, name, out string) {
		t.Helper()
		plain := stripANSIstr(out)
		if strings.ContainsRune(plain, '\x1b') {
			t.Fatalf("%s retained a terminal escape: %q", name, plain)
		}
		if !strings.Contains(plain, "dynamic-detail") {
			t.Fatalf("%s omitted dynamic detail: %q", name, plain)
		}
		for row, line := range strings.Split(plain, "\n") {
			if strings.Contains(line, "dynamic-detail") && maxLineWidth(line) > maxCardWidth {
				t.Errorf("%s row %d width = %d, want <= %d: %q", name, row, maxLineWidth(line), maxCardWidth, line)
			}
		}
	}

	t.Run("skills and learned details", func(t *testing.T) {
		assertFits(t, "skills", renderSkillsPanel(th, skillsState{skills: []client.Skill{{Name: long, Description: long}}, filtered: []client.Skill{{Name: long, Description: long}}, learned: []client.LearnedSkill{{Name: long, State: long, OwnerAgent: long}}}, client.Capabilities{Skills: true}, hk, width))
		assertFits(t, "learned skill", centerCard(th, renderLearnedSkillDetail(th, client.LearnedSkill{Name: long, OwnerAgent: long, State: long, Version: long, Revision: long, Description: long, Body: long, Evaluations: []client.SkillEvaluation{{Verdict: long, FixtureIDs: []string{long}}}, Receipts: []client.SkillChange{{Operation: long, FromState: long, ToState: long}}}, long, width), width, 40))
	})
	t.Run("agent definitions", func(t *testing.T) {
		assertFits(t, "agents", renderAgentsInvPanel(th, agentsInvState{agents: []client.Agent{{Name: long, Description: long, Model: long, PermissionMode: long, Tools: []string{long}}}}, client.Capabilities{Agents: true}, hk, width))
	})
	t.Run("connect and session details", func(t *testing.T) {
		m := Model{width: width, connect: connectState{open: true, targets: []ConnectTarget{{Target: long, Issuer: long, ClientID: long, Audience: long}}}}
		assertFits(t, "connect", m.renderConnectOverlay(th))
		assertFits(t, "session details", renderSessionDetails(th, sessionDetailsView{ID: long, Title: long, State: long, Connection: long, Placement: client.Placement{Label: long}, ProviderID: long, ModelID: long}, hk, width, 40))
	})
	t.Run("dream reflections soul and user model", func(t *testing.T) {
		plan := &client.DreamPlan{Operations: []client.DreamOperation{{Kind: long, Survivor: client.DreamParticipant{Key: long, Value: long, Description: long}, Sources: []client.DreamParticipant{{Key: long, Value: long, Description: long}}, Replacement: client.DreamReplacement{Value: long, Description: long}, Reason: long}}}
		assertFits(t, "dream", renderDreamOverlay(th, dreamState{view: dreamReview, plan: plan}, client.Capabilities{}, hk, width, 60))
		proposal := &client.LearningProposal{ID: long, Status: long, Version: long, Kind: long, Key: long, Value: long, Description: long, Body: long, Triggers: []string{long}, Evidence: []client.LearningEvidence{{Locator: long, SessionID: long, Digest: long, Preview: long}}}
		assertFits(t, "reflections", renderReflectionsOverlay(th, reflectionsState{view: reflectionsDetail, detail: proposal}, client.Capabilities{LearningProposals: true}, hk, width, 60))
		assertFits(t, "soul", centerCard(th, renderSoulPanel(th, soulState{soul: client.Soul{Present: true, Content: long, SHA256: long}}, client.Capabilities{Soul: true}, hk, width), width, 40))
		assertFits(t, "user model inventory", renderUserModelOverlay(th, userModelState{view: userModelPanel, model: client.UserModel{Entries: []client.UserModelEntry{{Key: long, Description: long}}}}, client.Capabilities{UserModel: true}, hk, width, 40))
		assertFits(t, "user model", renderUserModelOverlay(th, userModelState{view: userModelDetail, detail: &client.UserModelDetail{Current: client.UserModelRevision{Key: long, Status: long, Version: long, Writer: long, Origin: long, SourceSessionID: long, SourceProposalID: long, Description: long, Value: long}}}, client.Capabilities{UserModel: true}, hk, width, 40))
	})
}
