package scrollback

import "reflect"

// TeamStart is part of the internal typed scrollback contract.
type TeamStart struct {
	TeamID string
	Lanes  []TeamLane
}

// TeamUpdate is part of the internal typed scrollback contract.
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

// Task is part of the internal typed scrollback contract.
type Task struct {
	ID, Description, State, Assignee string
	Dependencies                     []string
}

// Finding is part of the internal typed scrollback contract.
type Finding struct{ Member, Body string }

// TeamCardSnapshot is part of the internal typed scrollback contract.
type TeamCardSnapshot struct {
	Call     ToolCall
	Resolved bool
	Result   ToolResult
	Update   TeamUpdate
}

// Kind is part of the internal typed scrollback contract.
func (TeamCardSnapshot) Kind() Kind       { return KindTeam }
func (TeamCardSnapshot) payloadSnapshot() {}

// TeamCards is part of the internal typed scrollback contract.
type TeamCards struct{ conversation *Conversation }

// Teams is part of the internal typed scrollback contract.
func (c *Conversation) Teams() TeamCards { return TeamCards{conversation: c} }

// Start is part of the internal typed scrollback contract.
func (t TeamCards) Start(callID string, start TeamStart) bool {
	c := t.conversation
	i, ok := c.call(callID)
	if !ok {
		return false
	}
	payload, ok := c.cards[i].payload.(ToolCardSnapshot)
	if !ok {
		return false
	}
	return c.replace(i, TeamCardSnapshot{
		Call: payload.Call, Resolved: payload.Resolved, Result: payload.Result,
		Update: TeamUpdate{TeamID: start.TeamID, Lanes: start.Lanes},
	})
}

// Update is part of the internal typed scrollback contract.
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
	if reflect.DeepEqual(payload.Update, update) {
		return true
	}
	payload.Update = update
	return c.replace(i, payload)
}

// StartTeam is part of the internal typed scrollback contract.
func (c *Conversation) StartTeam(callID string, start TeamStart) bool {
	return c.Teams().Start(callID, start)
}

// UpdateTeam is part of the internal typed scrollback contract.
func (c *Conversation) UpdateTeam(callID string, update TeamUpdate) bool {
	return c.Teams().Update(callID, update)
}
