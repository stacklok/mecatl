package scrollback

import "testing"

func TestTeamTransitionsOwnNestedData(t *testing.T) {
	var c Conversation
	c.Tools().Add(ToolCall{ID: "team", Name: "Team"})
	if !c.Teams().Start("team", TeamStart{TeamID: "team-1"}) {
		t.Fatal("start team")
	}
	team := TeamUpdate{Lanes: []TeamLane{{Name: "worker", Trace: []TraceEntry{{Text: "trace"}}}}, Tasks: []Task{{ID: "task", Dependencies: []string{"dep"}}}, Findings: []Finding{{Member: "worker", Body: "finding"}}}
	if !c.Teams().Update("team", team) {
		t.Fatal("update team")
	}
	team.Lanes[0].Trace[0].Text = "mutated input"
	team.Tasks[0].Dependencies[0] = "mutated input"
	out := c.SnapshotAt(0).Payload.(TeamCardSnapshot)
	out.Update.Lanes[0].Trace[0].Text = "mutated snapshot"
	out.Update.Tasks[0].Dependencies[0] = "mutated snapshot"
	stored := c.SnapshotAt(0).Payload.(TeamCardSnapshot)
	if stored.Update.Lanes[0].Trace[0].Text != "trace" || stored.Update.Tasks[0].Dependencies[0] != "dep" {
		t.Fatalf("team snapshot leaked mutation: %#v", stored.Update)
	}
}

func TestTeamTerminalTaskAndFindingState(t *testing.T) {
	var c Conversation
	c.Tools().Add(ToolCall{ID: "team", Name: "Team"})
	if !c.Teams().Start("team", TeamStart{TeamID: "team-1"}) {
		t.Fatal("start team")
	}
	terminal := TeamUpdate{
		TeamID:   "team-1",
		Done:     true,
		Stop:     "complete",
		Tasks:    []Task{{ID: "design", State: "done", Assignee: "lead", Dependencies: []string{"research"}}},
		Findings: []Finding{{Member: "lead", Body: "implementation complete"}},
	}
	if !c.Teams().Update("team", terminal) {
		t.Fatal("store terminal team state")
	}
	got := c.SnapshotAt(0).Payload.(TeamCardSnapshot).Update
	if !got.Done || got.Stop != "complete" || got.Tasks[0].State != "done" || got.Tasks[0].Assignee != "lead" || got.Findings[0] != (Finding{Member: "lead", Body: "implementation complete"}) {
		t.Fatalf("terminal team update = %#v", got)
	}
	if !c.Teams().Update("team", terminal) || c.Teams().Update("team", TeamUpdate{Done: true, Tasks: []Task{{ID: "design", State: "open"}}}) {
		t.Fatal("terminal team update did not preserve its task and finding state")
	}
}
