package scrollback

import "reflect"

// TeamStart identifies a team and its initial member lanes.
type TeamStart struct {
	TeamID string
	Lanes  []TeamLane
}

// TeamUpdate is the current or terminal state of a team. Done seals the update:
// only an identical replay is accepted afterwards.
type TeamUpdate struct {
	TeamID   string
	Lanes    []TeamLane
	Tasks    []Task
	Findings []Finding
	Rounds   int
	Stop     string
	Usage    Usage
	Done     bool
}

// TeamLane is a bounded projection of one Team member, in roster order.
type TeamLane struct {
	Name, SessionID, Role                             string
	Mutating, Lead                                    bool
	RoutedCategory, RoutedModel, RoutingReason, Model string
	Routing                                           RoutingDecision
	Current                                           string
	ToolCount                                         int
	Usage                                             Usage
	Trace                                             []TraceEntry
	Idle, Stopped                                     bool
	StopReason, Cause                                 string
	ErrorRounds                                       int
	ContextUsed, ContextWindow                        int64
}

// Task is a team task and its assignment, state, and prerequisite task IDs.
type Task struct {
	ID, Description, State, Assignee string
	Dependencies                     []string
}

// Finding is a result reported by a named team member.
type Finding struct{ Member, Body string }

// TeamCardSnapshot is the detached payload of a specialized Team tool card.
type TeamCardSnapshot struct {
	Call     ToolCall
	Resolved bool
	Result   ToolResult
	Update   TeamUpdate
}

// Kind returns KindTeam.
func (TeamCardSnapshot) Kind() Kind       { return KindTeam }
func (TeamCardSnapshot) payloadSnapshot() {}

// TeamCards transitions tool cards for teams.
type TeamCards struct{ conversation *Conversation }

// Teams returns the facade for team lifecycle transitions.
func (c *Conversation) Teams() TeamCards { return TeamCards{conversation: c} }

// Start specializes the indexed pending Team tool card. It returns false for a
// missing, wrong-kind, or non-Team call; an identical replay succeeds as a no-op.
func (t TeamCards) Start(callID string, start TeamStart) bool {
	c := t.conversation
	i, ok := c.call(callID)
	if !ok {
		return false
	}
	start.Lanes = cloneTeamLanes(start.Lanes)
	switch payload := c.cards[i].payload.(type) {
	case ToolCardSnapshot:
		if payload.Call.Name != "Team" {
			return false
		}
		return c.replace(i, TeamCardSnapshot{
			Call: payload.Call, Resolved: payload.Resolved, Result: payload.Result,
			Update: TeamUpdate{TeamID: start.TeamID, Lanes: start.Lanes},
		})
	case TeamCardSnapshot:
		return payload.Update.TeamID == start.TeamID && reflect.DeepEqual(payload.Update.Lanes, start.Lanes)
	default:
		return false
	}
}

// Update records a team update. It returns false for a missing or wrong-kind card
// and for a conflicting update after Done; identical updates succeed as no-ops.
func (t TeamCards) Update(callID string, update TeamUpdate) bool {
	c := t.conversation
	i, ok := c.call(callID)
	if !ok {
		return false
	}
	payload, ok := c.cards[i].payload.(TeamCardSnapshot)
	if !ok {
		return false
	}
	update = cloneTeamUpdate(update)
	if payload.Update.Done {
		return reflect.DeepEqual(payload.Update, update)
	}
	if reflect.DeepEqual(payload.Update, update) {
		return true
	}
	payload.Update = update
	return c.replace(i, payload)
}
