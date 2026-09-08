package k8s_e2e_test

import (
	"encoding/json"
	"testing"
)

const learningInitialCompletion = "The restart-safe procedure has been retained for learning."

type learningMockScriptDocument struct {
	Turns []learningMockTurn `json:"turns"`
}

type learningMockTurn struct {
	DelayMS   int64                  `json:"delay_ms,omitempty"`
	Text      *string                `json:"text,omitempty"`
	ToolCalls []learningMockToolCall `json:"tool_calls,omitempty"`
}

type learningMockToolCall struct {
	ID   string            `json:"id"`
	Name string            `json:"name"`
	Args map[string]string `json:"args"`
}

func buildInitialLearningMockScript(reflection, skillName, terminal string) ([]byte, error) {
	return marshalLearningMockScript(
		learningTextTurn(learningInitialCompletion, 0),
		learningTextTurn(reflection, 30_000),
		learningSkillTurn(skillName),
		learningTextTurn(terminal, 0),
	)
}

func buildRecoveryLearningMockScript(reflection, skillName, terminal string) ([]byte, error) {
	return marshalLearningMockScript(
		learningTextTurn(reflection, 0),
		learningSkillTurn(skillName),
		learningTextTurn(terminal, 0),
	)
}

func learningTextTurn(text string, delayMS int64) learningMockTurn {
	return learningMockTurn{DelayMS: delayMS, Text: &text}
}

func learningSkillTurn(skillName string) learningMockTurn {
	return learningMockTurn{ToolCalls: []learningMockToolCall{{
		ID: "call-learned-skill", Name: "Skill", Args: map[string]string{"name": skillName},
	}}}
}

func marshalLearningMockScript(turns ...learningMockTurn) ([]byte, error) {
	return json.Marshal(learningMockScriptDocument{Turns: turns})
}

func TestLearningMockScriptSequences(t *testing.T) {
	const (
		reflection = `{"kind":"proposed"}`
		skill      = "restart-safe-workflow"
		terminal   = "E2E_SKILL_USED"
	)

	initial, err := buildInitialLearningMockScript(reflection, skill, terminal)
	if err != nil {
		t.Fatalf("build initial script: %v", err)
	}
	initialTurns := decodeLearningMockTurns(t, initial)
	if len(initialTurns) != 4 {
		t.Fatalf("initial turns = %d, want 4", len(initialTurns))
	}
	assertLearningTextTurn(t, initialTurns[0], learningInitialCompletion, 0)
	assertLearningTextTurn(t, initialTurns[1], reflection, 30_000)
	assertLearningSkillTurn(t, initialTurns[2], skill)
	assertLearningTextTurn(t, initialTurns[3], terminal, 0)

	recovery, err := buildRecoveryLearningMockScript(reflection, skill, terminal)
	if err != nil {
		t.Fatalf("build recovery script: %v", err)
	}
	recoveryTurns := decodeLearningMockTurns(t, recovery)
	if len(recoveryTurns) != 3 {
		t.Fatalf("recovery turns = %d, want 3", len(recoveryTurns))
	}
	assertLearningTextTurn(t, recoveryTurns[0], reflection, 0)
	assertLearningSkillTurn(t, recoveryTurns[1], skill)
	assertLearningTextTurn(t, recoveryTurns[2], terminal, 0)
}

func decodeLearningMockTurns(t *testing.T, script []byte) []learningMockTurn {
	t.Helper()
	var document learningMockScriptDocument
	if err := json.Unmarshal(script, &document); err != nil {
		t.Fatalf("decode script: %v", err)
	}
	return document.Turns
}

func assertLearningTextTurn(t *testing.T, turn learningMockTurn, text string, delayMS int64) {
	t.Helper()
	if turn.Text == nil || *turn.Text != text || turn.DelayMS != delayMS || len(turn.ToolCalls) != 0 {
		t.Fatalf("text turn shape mismatch: text present=%t delay_ms=%d tool_calls=%d", turn.Text != nil, turn.DelayMS, len(turn.ToolCalls))
	}
}

func assertLearningSkillTurn(t *testing.T, turn learningMockTurn, skill string) {
	t.Helper()
	if turn.Text != nil || turn.DelayMS != 0 || len(turn.ToolCalls) != 1 {
		t.Fatalf("Skill turn shape mismatch: text present=%t delay_ms=%d tool_calls=%d", turn.Text != nil, turn.DelayMS, len(turn.ToolCalls))
	}
	call := turn.ToolCalls[0]
	if call.ID != "call-learned-skill" || call.Name != "Skill" || len(call.Args) != 1 || call.Args["name"] != skill {
		t.Fatalf("Skill call shape mismatch: id=%q name=%q args=%d expected_name=%t", call.ID, call.Name, len(call.Args), call.Args["name"] == skill)
	}
}
