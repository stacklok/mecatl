// Package team is the DOMAIN coordination substrate for headless agent teams
// (see docs/adr/0014-agent-teams.md). It holds the pure, in-memory state two
// or more concurrently-running agent sessions share to coordinate: a roster of
// members with lifecycle states, a dependency-aware task list members claim and
// complete, and a mailbox members use to message one another.
//
// It is a domain leaf: it imports only engine/session (for SessionID) and the
// standard library, and nothing in session imports team, so there is no cycle.
// It performs NO I/O, spawns NO goroutines, and knows nothing about the Engine or
// the LLM — the supervisor (engine/agent) drives the running sessions and shares
// a *Team by reference. Because mecatl teammates are goroutines in one process
// rather than separate OS processes, this shared-memory aggregate (guarded by a
// single mutex) replaces the on-disk, file-locked task files a multi-process
// harness needs.
//
// All mutators are safe for concurrent use; queries return value copies so callers
// can never mutate aggregate state without going through a method.
package team

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/stacklok/mecatl/engine/session"
)

// OperatorSender is the reserved sender identity for messages injected by the
// out-of-band operator (the human/client on the wire, not a roster member). It is
// the only non-member `from` Send accepts, and no member may be named it: this
// keeps the message-provenance space partitioned into exactly "a real teammate"
// and "the operator", so a teammate cannot impersonate the operator and the wire
// path cannot impersonate a teammate. See Send and AddMember.
const OperatorSender = "operator"

// Aggregate resource caps. They bound a single team's coordination state so a
// runaway (or adversarial) member cannot exhaust process memory by creating
// unbounded tasks, queuing unbounded messages, or enrolling unbounded members.
// The model sees a breach as a tool-result error (ErrTooMany*), not a crash.
const (
	// MaxTasks caps the total number of tasks one team may ever create.
	MaxTasks = 512
	// MaxInboxMessages caps the number of queued (undelivered) messages a single
	// member's inbox may hold at once. Draining frees the budget again.
	MaxInboxMessages = 256
	// MaxMembers caps the roster size of one team.
	MaxMembers = 32
	// MaxFindings caps the findings ledger so a runaway member cannot exhaust memory.
	MaxFindings = 512
)

// Errors returned by the Team aggregate.
var (
	// ErrMemberExists is returned by AddMember when name is already taken.
	ErrMemberExists = errors.New("team: member already exists")
	// ErrUnknownMember is returned when a named member is not on the roster.
	ErrUnknownMember = errors.New("team: unknown member")
	// ErrUnknownTask is returned when a task id is not in the task list.
	ErrUnknownTask = errors.New("team: unknown task")
	// ErrTaskNotClaimable is returned by ClaimTask when the task is not pending,
	// is already assigned, or has unmet dependencies.
	ErrTaskNotClaimable = errors.New("team: task not claimable")
	// ErrTaskState is returned by CompleteTask when the task is not in progress
	// or is completed by a member that does not own it.
	ErrTaskState = errors.New("team: illegal task transition")
	// ErrReservedName is returned by AddMember when name is the reserved operator
	// identity (OperatorSender): a member must not be able to be named the operator
	// and thereby impersonate out-of-band operator messages.
	ErrReservedName = errors.New("team: name is reserved")
	// ErrUnknownSender is returned by Send when from is neither a current roster
	// member nor the reserved operator identity: a message's provenance must be a
	// real teammate or the operator, never an arbitrary forged label.
	ErrUnknownSender = errors.New("team: unknown sender")
	// ErrTooManyTasks is returned by CreateTask when the team is at MaxTasks.
	ErrTooManyTasks = errors.New("team: task limit reached")
	// ErrTooManyMessages is returned by Send when the recipient's inbox is at
	// MaxInboxMessages.
	ErrTooManyMessages = errors.New("team: inbox limit reached")
	// ErrTooManyMembers is returned by AddMember when the roster is at MaxMembers.
	ErrTooManyMembers = errors.New("team: member limit reached")
	// ErrTooManyFindings is returned by AppendFinding when the ledger is at MaxFindings.
	ErrTooManyFindings = errors.New("team: findings ledger limit reached")
)

// MemberState is the lifecycle state of a teammate (or the lead).
type MemberState string

const (
	// MemberSpawning is the initial state before the member's session is wired.
	MemberSpawning MemberState = "spawning"
	// MemberWorking means the member is running a turn or holds a claimed task.
	MemberWorking MemberState = "working"
	// MemberIdle means the member has no work and is parked awaiting a message or
	// a newly-unblocked task.
	MemberIdle MemberState = "idle"
	// MemberStopped is terminal: the member has shut down.
	MemberStopped MemberState = "stopped"
)

// Member is one participant in a team. The lead is just a Member like any other;
// the supervisor knows which name is the lead.
type Member struct {
	// Name is the unique, human-meaningful handle peers address messages to.
	Name string
	// AgentType is the optional agent-definition name this member adopts (its
	// scoped tools / model / prompt); empty for a generic member.
	AgentType string
	// Session is the stable id reserved for the runtime session backing this
	// member. It may be advertised before that runtime is materialized.
	Session session.SessionID
	// State is the member's lifecycle state.
	State MemberState
}

// TaskID identifies a task within a team.
type TaskID string

// TaskState is the lifecycle state of a task.
type TaskState string

const (
	// TaskPending is unclaimed work (possibly still blocked by dependencies).
	TaskPending TaskState = "pending"
	// TaskInProgress is claimed by a member and being worked.
	TaskInProgress TaskState = "in_progress"
	// TaskCompleted is finished; it unblocks any task that depends on it.
	TaskCompleted TaskState = "completed"
)

// Task is one unit of work on the shared list. Dependencies are other tasks that
// must be Completed before this one becomes claimable.
type Task struct {
	// ID is the stable identifier.
	ID TaskID
	// Description is the work to do (the prompt seed the assignee runs against).
	Description string
	// Deps lists task ids that must complete before this task can be claimed.
	Deps []TaskID
	// Assignee is the member name that claimed the task, or empty if unclaimed.
	Assignee string
	// State is the task lifecycle state.
	State TaskState
}

// Message is one mailbox entry from one member to another.
type Message struct {
	// Seq is a team-global monotonic sequence number (delivery/order witness).
	Seq int
	// From is the sender's member name (may be the lead).
	From string
	// To is the recipient's member name.
	To string
	// Body is the message text.
	Body string
}

// Finding is one member-authored finding recorded to the shared ledger. Member is
// the recording member's name (authenticated against the roster by AppendFinding);
// Body is the finding text (UNTRUSTED — member-authored, fenced before it reaches
// the lead). The ledger is the PRIMARY channel through which the lead's synthesis
// turn consolidates the team's work into the final report.
type Finding struct {
	// Seq is a team-global monotonic sequence number that witnesses append order.
	Seq int
	// Member is the recording member's name (a current roster member).
	Member string
	// Body is the finding text.
	Body string
}

// Team is the aggregate root for one agent team's coordination state. Construct it
// with New. All methods are safe for concurrent use.
type Team struct {
	mu sync.Mutex

	name string

	members   map[string]*Member
	memOrder  []string // join order, for deterministic listing
	tasks     map[TaskID]*Task
	taskOrder []TaskID // creation order, for deterministic claiming
	inbox     map[string][]Message
	findings  []Finding // append-order ledger; the primary synthesis channel

	nextTaskN int
	nextMsg   int
	nextFind  int // monotonic seq for findings, mirroring nextMsg
}

// New constructs an empty team identified by name.
func New(name string) *Team {
	return &Team{
		name:    name,
		members: make(map[string]*Member),
		tasks:   make(map[TaskID]*Task),
		inbox:   make(map[string][]Message),
	}
}

// Name returns the team's identifier.
func (t *Team) Name() string { return t.name }

// AddMember enrols a new member in MemberSpawning state. It returns ErrMemberExists
// if name is already taken, ErrReservedName if name is the reserved operator
// identity (so a member cannot impersonate the operator), and ErrTooManyMembers
// if the roster is already at MaxMembers.
func (t *Team) AddMember(name, agentType string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if name == OperatorSender {
		return fmt.Errorf("%w: %q", ErrReservedName, name)
	}
	if _, ok := t.members[name]; ok {
		return fmt.Errorf("%w: %q", ErrMemberExists, name)
	}
	if len(t.members) >= MaxMembers {
		return fmt.Errorf("%w: %d", ErrTooManyMembers, MaxMembers)
	}
	t.members[name] = &Member{Name: name, AgentType: agentType, State: MemberSpawning}
	t.memOrder = append(t.memOrder, name)
	return nil
}

// RemoveMember drops a member from the roster, clearing its mailbox. It is used to
// roll back a failed enrolment (e.g. the supervisor rejecting a member after
// AddMember but before the member is wired). It is a no-op for an unknown member.
// It does NOT reassign or release tasks the member may hold; a member rolled back
// during enrolment holds none.
func (t *Team) RemoveMember(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.members[name]; !ok {
		return
	}
	delete(t.members, name)
	delete(t.inbox, name)
	for i, n := range t.memOrder {
		if n == name {
			t.memOrder = append(t.memOrder[:i], t.memOrder[i+1:]...)
			break
		}
	}
}

// SetMemberSession records the running session id backing a member.
func (t *Team) SetMemberSession(name string, sid session.SessionID) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.members[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownMember, name)
	}
	m.Session = sid
	return nil
}

// SetMemberState transitions a member to state.
func (t *Team) SetMemberState(name string, state MemberState) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.members[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownMember, name)
	}
	m.State = state
	return nil
}

// Members returns a copy of the roster in join order.
func (t *Team) Members() []Member {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Member, 0, len(t.memOrder))
	for _, name := range t.memOrder {
		out = append(out, *t.members[name])
	}
	return out
}

// CreateTask appends a task with the given description and dependencies. Every dep
// must already exist (ErrUnknownTask otherwise). It returns ErrTooManyTasks when
// the team is already at MaxTasks. It returns the new task id.
func (t *Team) CreateTask(description string, deps ...TaskID) (TaskID, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.tasks) >= MaxTasks {
		return "", fmt.Errorf("%w: %d", ErrTooManyTasks, MaxTasks)
	}
	for _, d := range deps {
		if _, ok := t.tasks[d]; !ok {
			return "", fmt.Errorf("%w: dependency %q", ErrUnknownTask, d)
		}
	}
	t.nextTaskN++
	id := TaskID(fmt.Sprintf("task-%d", t.nextTaskN))
	t.tasks[id] = &Task{ID: id, Description: description, Deps: slices.Clone(deps), State: TaskPending}
	t.taskOrder = append(t.taskOrder, id)
	return id, nil
}

// claimableLocked reports whether task is pending, unassigned, and has all deps
// completed. The caller must hold t.mu.
func (t *Team) claimableLocked(task *Task) bool {
	if task.State != TaskPending || task.Assignee != "" {
		return false
	}
	for _, d := range task.Deps {
		dep, ok := t.tasks[d]
		if !ok || dep.State != TaskCompleted {
			return false
		}
	}
	return true
}

// ClaimNext atomically claims the first claimable task (in creation order) for
// member and returns it. It returns ok=false when nothing is currently claimable
// (all done, all blocked, or all assigned). The member must exist.
func (t *Team) ClaimNext(member string) (Task, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.members[member]; !ok {
		return Task{}, false, fmt.Errorf("%w: %q", ErrUnknownMember, member)
	}
	for _, id := range t.taskOrder {
		task := t.tasks[id]
		if t.claimableLocked(task) {
			task.State = TaskInProgress
			task.Assignee = member
			// Return a copy whose Deps slice does not alias the aggregate's backing
			// array, so a caller cannot mutate team state outside the lock (Tasks()
			// makes the same guarantee for its snapshots).
			out := *task
			out.Deps = slices.Clone(task.Deps)
			return out, true, nil
		}
	}
	return Task{}, false, nil
}

// ClaimTask atomically claims a specific task for member. It returns
// ErrTaskNotClaimable if the task is not pending, already assigned, or blocked by
// an incomplete dependency, and ErrUnknownTask / ErrUnknownMember as appropriate.
func (t *Team) ClaimTask(id TaskID, member string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.members[member]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownMember, member)
	}
	task, ok := t.tasks[id]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownTask, id)
	}
	if !t.claimableLocked(task) {
		return fmt.Errorf("%w: %q", ErrTaskNotClaimable, id)
	}
	task.State = TaskInProgress
	task.Assignee = member
	return nil
}

// CompleteTask marks an in-progress task completed. It returns ErrTaskState if the
// task is not in progress, or if member is not the task's assignee. Completing a
// task may unblock tasks that depend on it (they become claimable).
func (t *Team) CompleteTask(id TaskID, member string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	task, ok := t.tasks[id]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownTask, id)
	}
	if task.State != TaskInProgress || task.Assignee != member {
		return fmt.Errorf("%w: %q by %q (state %s, assignee %q)",
			ErrTaskState, id, member, task.State, task.Assignee)
	}
	task.State = TaskCompleted
	return nil
}

// ReleaseTasks returns every in-progress task assigned to member back to pending
// and clears its assignee. It is how a stopped member's unfinished work is freed:
// an in-progress task owned by a member that will never run again would otherwise
// stay in_progress forever — blocking its dependents and preventing Quiescent from
// ever holding (the team would dead-spin to its round cap). Completed and pending
// tasks are untouched; it is a no-op for an unknown member or one holding none.
func (t *Team) ReleaseTasks(member string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, task := range t.tasks {
		if task.State == TaskInProgress && task.Assignee == member {
			task.State = TaskPending
			task.Assignee = ""
		}
	}
}

// InProgressFor reports whether member currently holds at least one in-progress
// task. The supervisor uses it to avoid auto-claiming a second task for a member
// that is already working one — bounding a member to a single in-flight claim so a
// single member cannot drain the whole task list into itself across rounds.
func (t *Team) InProgressFor(member string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, task := range t.tasks {
		if task.State == TaskInProgress && task.Assignee == member {
			return true
		}
	}
	return false
}

// Tasks returns a copy of the task list in creation order.
func (t *Team) Tasks() []Task {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Task, 0, len(t.taskOrder))
	for _, id := range t.taskOrder {
		task := *t.tasks[id]
		task.Deps = slices.Clone(task.Deps)
		out = append(out, task)
	}
	return out
}

// Send posts a message from one member to another. The recipient must exist
// (ErrUnknownMember otherwise). The sender's identity is authenticated: from must
// be either a current roster member or the reserved OperatorSender, else
// ErrUnknownSender — this prevents a caller from forging a `from` (e.g.
// impersonating the lead) on the wire path or in a coordination tool. The
// recipient's inbox is bounded: a queued (undelivered) backlog at MaxInboxMessages
// returns ErrTooManyMessages. Messages are delivered to the recipient via Drain.
func (t *Team) Send(from, to, body string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if from != OperatorSender {
		if _, ok := t.members[from]; !ok {
			return fmt.Errorf("%w: %q", ErrUnknownSender, from)
		}
	}
	if _, ok := t.members[to]; !ok {
		return fmt.Errorf("%w: recipient %q", ErrUnknownMember, to)
	}
	if len(t.inbox[to]) >= MaxInboxMessages {
		return fmt.Errorf("%w: %d for %q", ErrTooManyMessages, MaxInboxMessages, to)
	}
	t.nextMsg++
	t.inbox[to] = append(t.inbox[to], Message{Seq: t.nextMsg, From: from, To: to, Body: body})
	return nil
}

// Drain returns and clears the pending messages for member, in arrival order
// (at-most-once delivery). It returns ErrUnknownMember if member is not enrolled.
func (t *Team) Drain(member string) ([]Message, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.members[member]; !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownMember, member)
	}
	msgs := t.inbox[member]
	delete(t.inbox, member)
	return msgs, nil
}

// AppendFinding records a member-authored finding to the shared ledger. The
// recording member is authenticated against the roster (ErrUnknownMember
// otherwise) exactly as Send authenticates a sender — but unlike Send there is no
// OperatorSender case: only a real roster member records a finding (the operator
// does not). The ledger is bounded: at MaxFindings the next append returns
// ErrTooManyFindings, so a runaway member cannot exhaust memory. The Body is
// UNTRUSTED member content; the supervisor fences it before it reaches the lead.
func (t *Team) AppendFinding(member, body string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.members[member]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownMember, member)
	}
	if len(t.findings) >= MaxFindings {
		return fmt.Errorf("%w: %d", ErrTooManyFindings, MaxFindings)
	}
	t.nextFind++
	t.findings = append(t.findings, Finding{Seq: t.nextFind, Member: member, Body: body})
	return nil
}

// Findings returns a copy of the findings ledger in APPEND ORDER (witnessed by each
// Finding's Seq), matching the copy-on-read discipline of Tasks/Members. Append
// order is the consistent idiom for "things that happened over time" (like the
// mailbox Drain's arrival order); the synthesis turn groups by member for
// readability but does not depend on enrolment order. Finding has no slice fields,
// so a shallow clone is a deep copy.
func (t *Team) Findings() []Finding {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.findings)
}

// Quiescent reports whether the team has reached a terminal-or-deadlocked resting
// point: no task is pending or in progress, every mailbox is empty, and no member
// is still working or spawning. It is the supervisor's "team done / nobody can
// make progress" signal. A team with members all Idle but tasks still blocked by
// an unsatisfiable dependency is NOT quiescent by this definition (a task remains
// pending), so the supervisor can distinguish genuine completion from a stuck
// dependency.
func (t *Team) Quiescent() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, task := range t.tasks {
		if task.State != TaskCompleted {
			return false
		}
	}
	for _, msgs := range t.inbox {
		if len(msgs) > 0 {
			return false
		}
	}
	for _, m := range t.members {
		if m.State == MemberWorking || m.State == MemberSpawning {
			return false
		}
	}
	return true
}
