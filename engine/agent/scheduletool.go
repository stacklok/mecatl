package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// scheduletool.go implements the model-facing Schedule tool (ADR 0073, the
// scheduled-tasks capability): the in-chat affordance the model calls to manage
// scheduled tasks. The tool is a THIN, validated adapter over the consumer-local
// port.ScheduleManager seam composition injects (satisfied by the server
// Service's schedule methods) — the SAME validated create-seam
// (validateScheduleSpec + applyScheduleDefaults) the REST/gRPC handlers call,
// never a second path. No adapter/server/proto type crosses into engine/agent
// (the WithSubagentStore injection precedent).

// ScheduleToolName is the catalog name of the scheduled-task management tool.
// Exported: the composition root references it for the permission floor Allow
// (defaultRules) and the catalog registration.
const ScheduleToolName = "Schedule"

// scheduleArgs is the model-supplied argument payload. Verb selects the
// operation; the remaining fields feed the verb that needs them (create takes a
// full spec; the name-addressed verbs take name; fire takes name). The JSON
// keys are snake_case (the repo's tool-arg convention).
type scheduleArgs struct {
	// Verb is the operation: create | list | inspect | pause | resume | delete | fire.
	Verb string `json:"verb"`
	// Name addresses one schedule (inspect/pause/resume/delete/fire; required
	// there, ignored by create/list).
	Name string `json:"name,omitempty"`
	// Prompt is the fire's prompt (create; required there).
	Prompt string `json:"prompt,omitempty"`
	// Cron is a cron trigger expression (create; mutually exclusive with OneShot).
	Cron string `json:"cron,omitempty"`
	// OneShot is a single future fire instant, RFC 3339 (create; mutually
	// exclusive with Cron).
	OneShot string `json:"one_shot,omitempty"`
	// Timezone is the IANA timezone the cron fires in (create; empty = UTC).
	Timezone string `json:"timezone,omitempty"`
	// Workspace is the session cwd the fire runs in (create; required on a
	// default-profile schedule).
	Workspace string `json:"workspace,omitempty"`
	// Profile is the session tool-surface profile (create; "" default, "no-fs").
	Profile string `json:"profile,omitempty"`
	// Mutating is the explicit write opt-in (create; default false = read-leaning,
	// which the create-seam requires to run in plan mode).
	Mutating bool `json:"mutating,omitempty"`
	// MaxFires bounds a cron's total fires (create; 0 = forever).
	MaxFires int `json:"max_fires,omitempty"`
	// MaxTurns bounds each fire's turns (create; 0 = the create-seam default).
	MaxTurns int `json:"max_turns,omitempty"`
	// MaxToolCalls bounds each fire's tool calls (create; 0 = the default).
	MaxToolCalls int `json:"max_tool_calls,omitempty"`
	// OneShotRetry opts a one-shot into at-least-once crash retry (create;
	// one-shot-only — the create-seam rejects it on a cron trigger fail-closed).
	OneShotRetry bool `json:"one_shot_retry,omitempty"`
	// OneShotMaxRetries bounds the one-shot retry budget (create; 0 with
	// OneShotRetry = the create-seam default of 3).
	OneShotMaxRetries int `json:"one_shot_max_retries,omitempty"`
}

// scheduleSchema is the JSON schema the model sees for the tool's arguments.
var scheduleSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "verb": {
      "type": "string",
      "enum": ["create", "list", "inspect", "pause", "resume", "delete", "fire"],
      "description": "The operation. create: register a new schedule. list: every schedule (name, trigger, next fire, enabled, last-fire stop). inspect: one schedule plus its fires. pause/resume: disable/enable without deleting. delete: remove. fire: trigger an immediate run, returning the fire id + session id."
    },
    "name": {"type": "string", "description": "The schedule name (required for inspect/pause/resume/delete/fire; create's new name)."},
    "prompt": {"type": "string", "description": "create only: the prompt each fire runs with."},
    "cron": {"type": "string", "description": "create only: a cron expression or @-macro (e.g. '0 9 * * *', '@every 1h'). Mutually exclusive with one_shot."},
    "one_shot": {"type": "string", "description": "create only: a single future fire instant, RFC 3339. Mutually exclusive with cron."},
    "timezone": {"type": "string", "description": "create only: the IANA timezone the cron fires in (e.g. 'America/New_York'). Empty = UTC."},
    "workspace": {"type": "string", "description": "create only: the session cwd the fire runs in. Required on a default-profile schedule; must be empty on a no-fs schedule."},
    "profile": {"type": "string", "description": "create only: the session tool-surface profile ('' default, 'no-fs' file-less)."},
    "mutating": {"type": "boolean", "description": "create only: the explicit write opt-in. Default false (read-leaning — the fire runs in plan mode)."},
    "max_fires": {"type": "integer", "description": "create only: bound a cron's total fires (0 = forever). Ignored on a one-shot."},
    "max_turns": {"type": "integer", "description": "create only: bound each fire's turns (0 = the create-seam default)."},
    "max_tool_calls": {"type": "integer", "description": "create only: bound each fire's tool calls (0 = the default)."},
    "one_shot_retry": {"type": "boolean", "description": "create only: opt a one-shot into at-least-once crash retry. One-shot only — rejected on a cron trigger (a cron self-heals via misfire)."},
    "one_shot_max_retries": {"type": "integer", "description": "create only: bound the one-shot retry budget (0 with one_shot_retry = the create-seam default of 3)."}
  },
  "required": ["verb"]
}`)

// ScheduleTool is the model-facing scheduled-task management tool. It consumes
// the consumer-local port.ScheduleManager seam composition injects (the server
// Service's schedule methods) — the SAME validated create-seam the REST/gRPC
// handlers ride, never a second path, so a schedule created in-chat is
// indistinguishable from an API-created one (one store, one truth).
//
// READONLY PARTITION (the dispatch invariant — AGENTS.md): a single tool carries
// BOTH read-only verbs (list/inspect) and mutating verbs (create/pause/resume/
// delete/fire), but ReadOnly() takes no args. The conservative, honest shape is
// chosen here: the tool reports ReadOnly()==false, so EVERY Schedule call
// serialises on the mutate path — a mutating verb can NEVER run concurrently
// with a sibling read (the AC the partition protects). The cost (list/inspect
// calls also serialise) is accepted over the unsound alternative (a true
// ReadOnly() would let a mutating verb fan out into the read-parallel batch).
// The verb-level read-only/mutating split is documented in the Spec description
// and pinned by TestScheduleTool_ReadOnlyPartition.
type ScheduleTool struct {
	mgr port.ScheduleManager
}

// NewScheduleTool constructs the Schedule tool over the injected
// port.ScheduleManager. mgr must be non-nil; NewScheduleTool panics otherwise
// (a composition-root programming error — the tool has nothing to drive without
// the seam). Composition registers the tool ONLY when the session's store backs
// a ScheduleStore (the scheduleStore() != nil gate), so a nil manager never
// reaches the catalog.
func NewScheduleTool(mgr port.ScheduleManager) tool.Tool {
	if mgr == nil {
		panic("agent: NewScheduleTool requires a non-nil ScheduleManager")
	}
	return &ScheduleTool{mgr: mgr}
}

// Spec returns the model-facing specification for the Schedule tool.
func (*ScheduleTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: ScheduleToolName,
		Description: "Manage scheduled tasks (recurring or one-shot prompts that run unattended). " +
			"Verbs: create registers a schedule (name + prompt + cron or one_shot + workspace); " +
			"list shows every schedule (name, trigger, next fire, enabled, last-fire stop reason); " +
			"inspect shows one schedule plus its fires; pause/resume disable/enable without deleting; " +
			"delete removes it; fire triggers an immediate run and returns the fire id + session id. " +
			"list and inspect are read-only; create, pause, resume, delete, and fire mutate the " +
			"schedule registry. Use list to discover existing schedules before creating a duplicate.",
		Schema: scheduleSchema,
	}
}

// ReadOnly reports false — the conservative, honest shape for a tool carrying
// both read-only (list/inspect) and mutating (create/pause/resume/delete/fire)
// verbs: ReadOnly() takes no args, so a single-verb tool cannot distinguish per
// call. Reporting false serialises EVERY Schedule call on the mutate path, so a
// mutating verb NEVER overlaps a sibling read (the AC the partition protects);
// the alternative (true) would let a mutating verb into the read-parallel
// batch. See the type doc.
func (*ScheduleTool) ReadOnly() bool { return false }

// NewPlanAwareScheduleTool wraps the Schedule tool for a PLAN-MODE session's
// catalog (ADR 0073 decision 4, the AC4.3 gate). The default tool reports
// ReadOnly()==false, so the plan-mode catalog projection (engine/tool/catalog.go
// Available(ModePlan)) would hide the WHOLE tool — including the read-leaning
// verbs plan mode must keep (a schedule CREATE does not itself mutate the
// workspace; the FIRE's posture is pinned at create-time by the Mutating/Mode
// invariant). The plan-aware variant reports ReadOnly()==true (so the plan-mode
// projection advertises it) and hard-denies a mutating: true create per call
// with the plan-mode deny reason BEFORE the base tool runs — the read-leaning
// verbs (list/inspect, and create with mutating false) drive through unchanged.
//
// The wrapper's ReadOnly()==true is sound because the ONLY call shapes it lets
// through are the read-leaning ones: none mutate the workspace (a CREATE writes
// the schedule REGISTRY, not the tree), so no admitted plan-mode call can
// mutate. The non-plan engine keeps the DEFAULT tool (the read/mutate
// serialization contract is unchanged).
func NewPlanAwareScheduleTool(base tool.Tool) tool.Tool {
	return &planAwareScheduleTool{base: base}
}

// planAwareScheduleTool is the plan-mode view NewPlanAwareScheduleTool wraps
// (see its doc).
type planAwareScheduleTool struct {
	base tool.Tool
}

// Spec is the base spec verbatim — the model sees the same verbs; the plan-mode
// gate is a per-call deny, not a hidden verb (removing the mutating create from
// the SCHEMA would make the model guess why its create was refused).
func (t *planAwareScheduleTool) Spec() tool.ToolSpec { return t.base.Spec() }

// ReadOnly reports true so the plan-mode catalog projection advertises the tool
// (see NewPlanAwareScheduleTool for the soundness argument).
func (*planAwareScheduleTool) ReadOnly() bool { return true }

// Execute hard-denies a mutating: true create (the plan-mode mutation veto) and
// passes every other call through to the base tool.
func (t *planAwareScheduleTool) Execute(ctx context.Context, call session.ToolCall, ws tool.Workspace) (session.ToolResult, error) {
	if reason := schedulePlanModeDeny(call); reason != "" {
		return session.NewToolError(call.ID, reason), nil
	}
	return t.base.Execute(ctx, call, ws)
}

// schedulePlanModeDeny returns the plan-mode hard-deny reason for a
// mutating: true create, else "". The reason mirrors the governance evaluator's
// plan-mode deny for mutating tools (present a plan and exit plan mode first)
// so the model gets the same guidance the loop's own deny carries — plus the
// read-leaning alternative it may use instead.
func schedulePlanModeDeny(call session.ToolCall) string {
	var args scheduleArgs
	if _, ok := session.ParseArgs(call, &args); !ok {
		return "" // a parse miss is the base tool's model-addressable error, not the gate's
	}
	if strings.EqualFold(strings.TrimSpace(args.Verb), "create") && args.Mutating {
		return "plan mode is active: a mutating Schedule create is not permitted; present a plan and exit plan mode first " +
			"(a read-leaning create with mutating:false IS permitted — the fire then runs in plan mode)"
	}
	return ""
}

// Compile-time assertion: the plan-aware wrapper is a Tool.
var _ tool.Tool = (*planAwareScheduleTool)(nil)

// Execute dispatches on the verb, mapping the call args onto the injected
// ScheduleManager and rendering the result as model-readable text. A
// verb-level error (an unknown schedule, a rejected create, a fire overlap) is
// a MODEL-ADDRESSABLE ToolResult (IsError), not a harness-level error — the
// loop records it and lets the model react.
func (t *ScheduleTool) Execute(ctx context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
	var args scheduleArgs
	if msg, ok := session.ParseArgs(call, &args); !ok {
		return session.NewToolError(call.ID, "Schedule: "+msg), nil
	}
	switch strings.ToLower(strings.TrimSpace(args.Verb)) {
	case "create":
		return t.create(ctx, call, args), nil
	case "list":
		return t.list(ctx, call), nil
	case "inspect":
		return t.inspect(ctx, call, args), nil
	case "pause":
		return t.setEnabled(ctx, call, args, false), nil
	case "resume":
		return t.setEnabled(ctx, call, args, true), nil
	case "delete":
		return t.delete(ctx, call, args), nil
	case "fire":
		return t.fire(ctx, call, args), nil
	default:
		return session.NewToolError(call.ID, fmt.Sprintf(
			"Schedule: unknown verb %q (supported: create, list, inspect, pause, resume, delete, fire)", args.Verb)), nil
	}
}

// create maps the create args onto a ScheduleSpec and drives the manager's
// create-seam. It performs ONLY the structural args→spec mapping (trigger XOR,
// the RFC 3339 one-shot parse); ALL validation (cron grammar, the Mutating/Mode
// invariant, the profile-aware workspace check, the cadence floor) lives in the
// create-seam — the tool never re-implements it (the one-path discipline).
func (t *ScheduleTool) create(ctx context.Context, call session.ToolCall, args scheduleArgs) session.ToolResult {
	if strings.TrimSpace(args.Name) == "" {
		return session.NewToolError(call.ID, "Schedule create: 'name' is required")
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return session.NewToolError(call.ID, "Schedule create: 'prompt' is required")
	}
	trigger, err := scheduleTrigger(args)
	if err != nil {
		return session.NewToolError(call.ID, "Schedule create: "+err.Error())
	}
	mode := session.ModeDefault
	if !args.Mutating {
		mode = session.ModePlan
	}
	spec := port.ScheduleSpec{
		Name:      strings.TrimSpace(args.Name),
		Prompt:    args.Prompt,
		Trigger:   trigger,
		Profile:   args.Profile,
		Workspace: args.Workspace,
		Mode:      mode,
		Mutating:  args.Mutating,
		MaxFires:  args.MaxFires,
		Timezone:  args.Timezone,
		// The Phase-2 one-shot retry fields map through VERBATIM: their rule
		// enforcement (one-shot-only rejection, the >= 0 bound, the default of
		// 3) lives ENTIRELY in the create-seam — the tool must never drop them
		// (a subset-of-the-seam wiring) or re-check them (a second path).
		OneShotRetry:      args.OneShotRetry,
		OneShotMaxRetries: args.OneShotMaxRetries,
		Limits: session.Limits{
			MaxTurns:     args.MaxTurns,
			MaxToolCalls: args.MaxToolCalls,
		},
	}
	sched, err := t.mgr.CreateSchedule(ctx, spec)
	if err != nil {
		return session.NewToolError(call.ID, "Schedule create: "+err.Error())
	}
	return session.NewToolResult(call.ID, renderScheduleCreated(sched))
}

// list renders every schedule (name, trigger, next fire, enabled, last-fire
// stop reason), sorted by name for a stable, model-diffable output.
func (t *ScheduleTool) list(ctx context.Context, call session.ToolCall) session.ToolResult {
	scheds, err := t.mgr.ListSchedules(ctx)
	if err != nil {
		return session.NewToolError(call.ID, "Schedule list: "+err.Error())
	}
	return session.NewToolResult(call.ID, renderScheduleList(scheds))
}

// inspect renders one schedule plus its fires (the terminal stop reasons).
func (t *ScheduleTool) inspect(ctx context.Context, call session.ToolCall, args scheduleArgs) session.ToolResult {
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return scheduleNameRequired(call.ID, "inspect")
	}
	sched, err := t.mgr.GetSchedule(ctx, name)
	if err != nil {
		return scheduleNameError(call.ID, "inspect", name, err)
	}
	fires, err := t.mgr.ListFires(ctx, name)
	if err != nil {
		return scheduleNameError(call.ID, "inspect", name, err)
	}
	return session.NewToolResult(call.ID, renderScheduleInspect(sched, fires))
}

// setEnabled drives pause (enabled=false) / resume (enabled=true).
func (t *ScheduleTool) setEnabled(ctx context.Context, call session.ToolCall, args scheduleArgs, enabled bool) session.ToolResult {
	verb := "pause"
	if enabled {
		verb = "resume"
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return scheduleNameRequired(call.ID, verb)
	}
	var err error
	if enabled {
		err = t.mgr.ResumeSchedule(ctx, name)
	} else {
		err = t.mgr.PauseSchedule(ctx, name)
	}
	if err != nil {
		return scheduleNameError(call.ID, verb, name, err)
	}
	state := "paused"
	if enabled {
		state = "resumed"
	}
	return session.NewToolResult(call.ID, fmt.Sprintf("Schedule %q %s.", name, state))
}

// delete removes a schedule (idempotent).
func (t *ScheduleTool) delete(ctx context.Context, call session.ToolCall, args scheduleArgs) session.ToolResult {
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return scheduleNameRequired(call.ID, "delete")
	}
	if err := t.mgr.DeleteSchedule(ctx, name); err != nil {
		return scheduleNameError(call.ID, "delete", name, err)
	}
	return session.NewToolResult(call.ID, fmt.Sprintf("Schedule %q deleted.", name))
}

// fire drives the synchronous-to-terminal FireNow seam and renders the minted
// fire's id + session id (the sched-- session) and terminal stop reason.
func (t *ScheduleTool) fire(ctx context.Context, call session.ToolCall, args scheduleArgs) session.ToolResult {
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return scheduleNameRequired(call.ID, "fire")
	}
	fireRec, err := t.mgr.FireNow(ctx, name)
	if err != nil {
		return scheduleNameError(call.ID, "fire", name, err)
	}
	return session.NewToolResult(call.ID, renderScheduleFired(fireRec))
}

// scheduleTrigger maps the cron/one_shot create args onto a TriggerSpec,
// enforcing the structural XOR (exactly one set) and the RFC 3339 one-shot
// parse — the tool-side structural mapping. The cron grammar + the one-shot
// future invariant stay in the create-seam.
func scheduleTrigger(args scheduleArgs) (port.TriggerSpec, error) {
	cron := strings.TrimSpace(args.Cron)
	oneShot := strings.TrimSpace(args.OneShot)
	switch {
	case cron != "" && oneShot != "":
		return port.TriggerSpec{}, fmt.Errorf("set exactly one of 'cron' or 'one_shot', not both")
	case cron == "" && oneShot == "":
		return port.TriggerSpec{}, fmt.Errorf("one of 'cron' or 'one_shot' is required")
	case cron != "":
		return port.TriggerSpec{Cron: cron}, nil
	default:
		at, err := time.Parse(time.RFC3339, oneShot)
		if err != nil {
			return port.TriggerSpec{}, fmt.Errorf("invalid 'one_shot' %q (want RFC 3339, e.g. 2026-07-25T09:00:00Z): %v", oneShot, err)
		}
		return port.TriggerSpec{OneShot: at}, nil
	}
}

// scheduleToolNameRequired is the model-addressable miss for a name-addressed
// verb called without a name.
func scheduleNameRequired(callID session.ToolCallID, verb string) session.ToolResult {
	return session.NewToolError(callID, fmt.Sprintf("Schedule %s: 'name' is required", verb))
}

// scheduleNameError renders a name-addressed verb's manager error. The not-found
// case (the port-level sentinel) names the schedule honestly; every other error
// (a create-seam rejection, an infra failure, a fire overlap) surfaces verbatim —
// already model-readable, never re-wrapped into a lie.
func scheduleNameError(callID session.ToolCallID, verb, name string, err error) session.ToolResult {
	return session.NewToolError(callID, fmt.Sprintf("Schedule %s %q: %v", verb, name, err))
}

// renderScheduleCreated renders a freshly-created schedule (the create-seam's
// computed first fire + defaults are echoed back so the model sees what landed).
func renderScheduleCreated(sched port.Schedule) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Schedule %q created.\n%s", sched.Spec.Name, renderScheduleSummary(sched))
	return b.String()
}

// renderScheduleList renders the list verb's one-line-per-schedule summary,
// sorted by name for a stable, model-diffable output.
func renderScheduleList(scheds []port.Schedule) string {
	if len(scheds) == 0 {
		return "No schedules."
	}
	sorted := append([]port.Schedule(nil), scheds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Spec.Name < sorted[j].Spec.Name })
	var b strings.Builder
	fmt.Fprintf(&b, "Schedules (%d):", len(sorted))
	for _, s := range sorted {
		fmt.Fprintf(&b, "\n- %s", renderScheduleSummary(s))
	}
	return b.String()
}

// renderScheduleSummary renders the one-line name/trigger/next-fire/enabled/
// last-fire-stop summary shared by create + list.
func renderScheduleSummary(s port.Schedule) string {
	enabled := "disabled"
	if s.State.Enabled {
		enabled = "enabled"
	}
	next := "none"
	if !s.State.NextFireAt.IsZero() {
		next = s.State.NextFireAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("%s [%s] next=%s %s", s.Spec.Name, renderTrigger(s.Spec.Trigger), next, enabled)
}

// renderTrigger renders the trigger as a compact model-readable label.
func renderTrigger(tr port.TriggerSpec) string {
	switch tr.Kind() {
	case port.TriggerCron:
		return "cron " + tr.Cron
	case port.TriggerOneShot:
		return "once " + tr.OneShot.UTC().Format(time.RFC3339)
	default:
		return "no trigger"
	}
}

// renderScheduleInspect renders one schedule's full summary plus its fires
// (id, fired-at, terminal stop reason, error), most-recent-first.
func renderScheduleInspect(sched port.Schedule, fires []port.ScheduleFire) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Schedule %q\n  %s\n  fire count: %d", sched.Spec.Name, renderScheduleSummary(sched), sched.State.FireCount)
	if len(fires) == 0 {
		b.WriteString("\n  fires: none")
		return b.String()
	}
	sorted := append([]port.ScheduleFire(nil), fires...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].FiredAt.After(sorted[j].FiredAt) })
	b.WriteString("\n  fires (most recent first):")
	for _, f := range sorted {
		stop := string(f.Stop)
		if stop == "" {
			stop = "running"
		}
		fmt.Fprintf(&b, "\n  - fire %s (session %s) at %s: %s", f.ID, f.SessionID, f.FiredAt.UTC().Format(time.RFC3339), stop)
		if f.Err != "" {
			fmt.Fprintf(&b, " — %s", f.Err)
		}
	}
	return b.String()
}

// renderScheduleFired renders the fire verb's result: the minted fire's id +
// session id (the sched-- session) and its terminal stop reason.
func renderScheduleFired(f port.ScheduleFire) string {
	stop := string(f.Stop)
	if stop == "" {
		stop = "unknown"
	}
	out := fmt.Sprintf("Fired schedule %q.\nfire id: %s\nsession id: %s\nstop: %s", f.ScheduleName, f.ID, f.SessionID, stop)
	if f.Err != "" {
		out += "\nerror: " + f.Err
	}
	return out
}

// Compile-time assertion: ScheduleTool is a Tool.
var _ tool.Tool = (*ScheduleTool)(nil)
