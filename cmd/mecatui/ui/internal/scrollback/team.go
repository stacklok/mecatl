package scrollback

import "reflect"

type TeamStart struct {
	TeamID string
	Lanes  []TeamLane
}

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

type Task struct {
	ID, Description, State, Assignee string
	Dependencies                     []string
}

type Finding struct{ Member, Body string }

type TeamCardSnapshot struct {
	Call     ToolCall
	Resolved bool
	Result   ToolResult
	Update   TeamUpdate
}

func (TeamCardSnapshot) Kind() Kind       { return KindTeam }
func (TeamCardSnapshot) payloadSnapshot() {}

type TeamCards struct{ conversation *Conversation }

func (c *Conversation) Teams() TeamCards { return TeamCards{conversation: c} }

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

func (c *Conversation) StartTeam(callID string, start TeamStart) bool {
	return c.Teams().Start(callID, start)
}
func (c *Conversation) UpdateTeam(callID string, update TeamUpdate) bool {
	return c.Teams().Update(callID, update)
}
