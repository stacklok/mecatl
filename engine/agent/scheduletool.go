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

// scheduletool.go implements the model-facing Schedule tools (ADR 0073, the
// scheduled-tasks capability): the in-chat affordance the model calls to manage
// scheduled tasks. The surface is TWO catalog entries over the ONE injected
// port.ScheduleManager seam — a read-only ScheduleQueryTool (list/inspect,
// ReadOnly()==true) and a mutating ScheduleTool (create/pause/resume/delete/
// fire, ReadOnly()==false) — the AC1.4 read-parallel/mutate-serial partition.
// Both are THIN, validated adapters over the consumer-local port.ScheduleManager
// seam composition injects (satisfied by the server Service's schedule methods)
// — the SAME validated create-seam (validateScheduleSpec + applyScheduleDefaults)
// the REST/gRPC handlers call, never a second path. No adapter/server/proto
// type crosses into engine/agent (the WithSubagentStore injection precedent).

// ScheduleToolName is the catalog name of the MUTATING scheduled-task
// management tool (create/pause/resume/delete/fire). Exported: the composition
// root references it for the permission floor Allow (defaultRules) and the
// catalog registration.
const ScheduleToolName = "Schedule"

// ScheduleQueryToolName is the catalog name of the READ-ONLY scheduled-task
// query tool (list/inspect). Exported: the composition root references it for
// the permission floor Allow (defaultRules) and the catalog registration.
const ScheduleQueryToolName = "ScheduleQuery"

// scheduleArgs is the model-supplied argument payload. Verb selects the
// operation; the remaining fields feed the verb that needs them (create takes a
// full spec; the name-addressed verbs take name; fire takes name). The JSON
// keys are snake_case (the repo's tool-arg convention). The SAME payload is
// shared by the read-only query tool (list/inspect) and the mutating tool
// (create/pause/resume/delete/fire) — each tool's Execute accepts only its own
// verbs.
type scheduleArgs struct {
	// Verb is the operation. The mutating tool accepts create | pause | resume |
	// delete | fire; the read-only query tool accepts list | inspect.
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

// scheduleSchema is the JSON schema the model sees for the MUTATING tool's
// arguments (create/pause/resume/delete/fire).
var scheduleSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "verb": {
      "type": "string",
      "enum": ["create", "pause", "resume", "delete", "fire"],
      "description": "The operation. create: register a new schedule. pause/resume: disable/enable without deleting. delete: remove. fire: trigger an immediate run, returning the fire id + session id."
    },
    "name": {"type": "string", "description": "The schedule name (required for pause/resume/delete/fire; create's new name)."},
    "prompt": {"type": "string", "description": "create only: the prompt each fire runs with."},
    "cron": {"type": "string", "description": "create only: a cron expression or @-macro (e.g. '0 9 * * *', '@every 1h'). Mutually exclusive with one_shot."},
    "one_shot": {"type": "string", "description": "create only: a single future fire instant, RFC 3339. Mutually exclusive with cron."},
    "timezone": {"type": "string", "description": "create only: the IANA timezone the cron fires in (e.g. 'America/New_York'). Empty = UTC."},
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

// scheduleQuerySchema is the JSON schema the model sees for the READ-ONLY query
// tool's arguments (list/inspect).
var scheduleQuerySchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "verb": {
      "type": "string",
      "enum": ["list", "inspect"],
      "description": "The read-only operation. list: every schedule (name, trigger, next fire, enabled, last-fire stop). inspect: one schedule plus its fires."
    },
    "name": {"type": "string", "description": "The schedule name (required for inspect; ignored by list)."}
  },
  "required": ["verb"]
}`)

// ScheduleTool is the model-facing MUTATING scheduled-task management tool
// (create/pause/resume/delete/fire). It consumes the consumer-local
// port.ScheduleManager seam composition injects (the server Service's schedule
// methods) — the SAME validated create-seam the REST/gRPC handlers ride, never
// a second path, so a schedule created in-chat is indistinguishable from an
// API-created one (one store, one truth).
//
// READONLY PARTITION (the dispatch invariant — AGENTS.md): the read-only verbs
// (list/inspect) live on the SEPARATE ScheduleQueryTool (ReadOnly()==true,
// read-parallel); this tool carries ONLY the mutating verbs and reports
// ReadOnly()==false, so every mutating Schedule call serialises on the mutate
// path and NEVER runs concurrently with a sibling read. The per-verb split is
// realised as two catalog entries over the one ScheduleManager because a single
// tool's ReadOnly() takes no args — pinned by TestScheduleTool_ReadOnlyPartition.
type ScheduleTool struct {
	mgr port.ScheduleManager
}

// NewScheduleTool constructs the mutating Schedule tool over the injected
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

// Spec returns the model-facing specification for the mutating Schedule tool.
func (*ScheduleTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: ScheduleToolName,
		Description: "Manage scheduled tasks (recurring or one-shot prompts that run unattended) — the MUTATING half. " +
			"Verbs: create registers a schedule (name + prompt + cron or one_shot) in this session's exact placement; " +
			"pause/resume disable/enable without deleting; delete removes it; " +
			"fire triggers an immediate run and returns the fire id + session id. " +
			"A schedule created here reports its fire's result back into THIS conversation when it fires — " +
			"tell the user to expect the outcome to arrive in this chat, not a separate session. " +
			"The read-only list/inspect verbs live on the ScheduleQuery tool; use ScheduleQuery list " +
			"to discover existing schedules before creating a duplicate.",
		Schema: scheduleSchema,
	}
}

// ReadOnly reports false — this tool carries ONLY the mutating verbs
// (create/pause/resume/delete/fire), so every call serialises on the mutate
// path and never overlaps a sibling read (the dispatch invariant). The read-only
// list/inspect verbs live on the ScheduleQueryTool (ReadOnly()==true). See the
// type doc.
func (*ScheduleTool) ReadOnly() bool { return false }

// ScheduleQueryTool is the model-facing READ-ONLY scheduled-task query tool
// (list/inspect). It shares the injected port.ScheduleManager with the mutating
// ScheduleTool but reports ReadOnly()==true, so its calls join the read-parallel
// batch — the AC1.4 partition (a mutating verb can never fan out from here
// because this tool carries none).
type ScheduleQueryTool struct {
	mgr port.ScheduleManager
}

// NewScheduleQueryTool constructs the read-only Schedule query tool over the
// injected port.ScheduleManager. mgr must be non-nil; NewScheduleQueryTool
// panics otherwise (the same composition-root programming-error contract as
// NewScheduleTool). Composition registers it alongside the mutating Schedule
// tool when the session's store backs a ScheduleStore.
func NewScheduleQueryTool(mgr port.ScheduleManager) tool.Tool {
	if mgr == nil {
		panic("agent: NewScheduleQueryTool requires a non-nil ScheduleManager")
	}
	return &ScheduleQueryTool{mgr: mgr}
}

// Spec returns the model-facing specification for the read-only Schedule query
// tool.
func (*ScheduleQueryTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: ScheduleQueryToolName,
		Description: "Read-only scheduled-task queries (recurring or one-shot prompts that run unattended). " +
			"Verbs: list shows every schedule (name, trigger, next fire, enabled, last-fire stop reason); " +
			"inspect shows one schedule plus its fires. These verbs are read-only; the mutating " +
			"create/pause/resume/delete/fire verbs live on the Schedule tool.",
		Schema: scheduleQuerySchema,
	}
}

// ReadOnly reports true — list/inspect never mutate the workspace or the
// schedule registry, so they are parallel-safe (the read-half of the AC1.4
// partition).
func (*ScheduleQueryTool) ReadOnly() bool { return true }

// Execute dispatches on the read-only verb (list/inspect). A mutating verb is a
// model-addressable unknown-verb error (this tool carries none).
func (t *ScheduleQueryTool) Execute(ctx context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	var args scheduleArgs
	if msg, ok := session.ParseArgs(call, &args); !ok {
		return session.NewToolError(call.ID, "ScheduleQuery: "+msg), nil
	}
	st := &ScheduleTool{mgr: t.mgr}
	switch strings.ToLower(strings.TrimSpace(args.Verb)) {
	case "list":
		return st.list(ctx, call), nil
	case "inspect":
		return st.inspect(ctx, call, args), nil
	default:
		return session.NewToolError(call.ID, fmt.Sprintf(
			"ScheduleQuery: unknown verb %q (supported: list, inspect — the mutating create/pause/resume/delete/fire verbs live on the Schedule tool)", args.Verb)), nil
	}
}

// Compile-time assertion: ScheduleQueryTool is a Tool.
var _ tool.Tool = (*ScheduleQueryTool)(nil)

// NewPlanAwareScheduleTool wraps the MUTATING Schedule tool for a PLAN-MODE
// session's catalog (ADR 0073 decision 4, the AC4.3 gate). The default mutating
// tool reports ReadOnly()==false, so the plan-mode catalog projection
// (engine/tool/catalog.go Available(ModePlan)) would hide the WHOLE tool —
// including the read-leaning create plan mode must keep (a schedule CREATE does
// not itself mutate the workspace; the FIRE's posture is pinned at create-time
// by the Mutating/Mode invariant). The plan-aware variant reports
// ReadOnly()==true (so the plan-mode projection advertises it) and hard-denies
// the mutating shapes per call with the plan-mode deny reason BEFORE the base
// tool runs — the read-leaning create (mutating:false) drives through
// unchanged. The read-only ScheduleQueryTool needs no wrapper: it is
// ReadOnly()==true, so the plan-mode projection advertises it as-is.
//
// The wrapper's ReadOnly()==true is sound because the ONLY call shapes it lets
// through are the read-leaning ones: none mutate the workspace (a CREATE writes
// the schedule REGISTRY, not the tree; a read-leaning schedule's FIRE runs in
// plan mode), so no admitted plan-mode call can mutate. mgr is the SAME
// ScheduleManager the base drives — the fire gate reads the schedule's pinned
// Mutating posture from it (the fire verb carries no mutating flag of its own).
// The non-plan engine keeps the DEFAULT tool (the read/mutate serialization
// contract is unchanged).
func NewPlanAwareScheduleTool(base tool.Tool, mgr port.ScheduleManager) tool.Tool {
	return &planAwareScheduleTool{base: base, mgr: mgr}
}

// planAwareScheduleTool is the plan-mode view NewPlanAwareScheduleTool wraps
// (see its doc).
type planAwareScheduleTool struct {
	base tool.Tool
	mgr  port.ScheduleManager
}

// Spec is the base spec verbatim — the model sees the same verbs; the plan-mode
// gate is a per-call deny, not a hidden verb (removing the mutating create from
// the SCHEMA would make the model guess why its create was refused).
func (t *planAwareScheduleTool) Spec() tool.ToolSpec { return t.base.Spec() }

// ReadOnly reports true so the plan-mode catalog projection advertises the tool
// (see NewPlanAwareScheduleTool for the soundness argument).
func (*planAwareScheduleTool) ReadOnly() bool { return true }

// Execute hard-denies the mutating shapes (a mutating: true create, and the
// fire of a mutating schedule — the plan-mode mutation veto) and passes every
// other call through to the base tool.
func (t *planAwareScheduleTool) Execute(ctx context.Context, call session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	if reason := t.schedulePlanModeDeny(ctx, call); reason != "" {
		return session.NewToolError(call.ID, reason), nil
	}
	return t.base.Execute(ctx, call, env)
}

// schedulePlanModeDeny returns the plan-mode hard-deny reason for a mutating
// shape, else "". Two shapes are denied: a mutating: true create, and the fire
// of a schedule whose create-time Mutating opt-in is set (the fire's own
// mutations are workspace writes — the plan-mode hard-deny on mutations). The
// reason mirrors the governance evaluator's plan-mode deny for mutating tools
// (present a plan and exit plan mode first) so the model gets the same guidance
// the loop's own deny carries — plus the read-leaning alternative it may use
// instead. A fire of a READ-LEANING schedule drives through (the fire runs in
// plan mode, pinned at create-time); an unresolvable schedule name lets the
// base tool surface its own honest not-found error (the gate never lies about a
// schedule that does not exist).
func (t *planAwareScheduleTool) schedulePlanModeDeny(ctx context.Context, call session.ToolCall) string {
	var args scheduleArgs
	if _, ok := session.ParseArgs(call, &args); !ok {
		return "" // a parse miss is the base tool's model-addressable error, not the gate's
	}
	switch strings.ToLower(strings.TrimSpace(args.Verb)) {
	case "create":
		if args.Mutating {
			return "plan mode is active: a mutating Schedule create is not permitted; present a plan and exit plan mode first " +
				"(a read-leaning create with mutating:false IS permitted — the fire then runs in plan mode)"
		}
	case "fire":
		name := strings.TrimSpace(args.Name)
		if name == "" {
			return "" // a missing name is the base tool's model-addressable error
		}
		sched, err := t.mgr.GetSchedule(ctx, name)
		if err != nil {
			return "" // unknown/unresolvable — the base tool's not-found error is the honest surface
		}
		if sched.Spec.Mutating {
			return "plan mode is active: firing the mutating schedule " + name + " is not permitted; present a plan and exit plan mode first " +
				"(the schedule's create-time mutating:true opt-in means its fire writes the workspace)"
		}
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
func (t *ScheduleTool) Execute(ctx context.Context, call session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	var args scheduleArgs
	if msg, ok := session.ParseArgs(call, &args); !ok {
		return session.NewToolError(call.ID, "Schedule: "+msg), nil
	}
	switch strings.ToLower(strings.TrimSpace(args.Verb)) {
	case "create":
		return t.create(ctx, call, args, env.Ref()), nil
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
			"Schedule: unknown verb %q (supported: create, pause, resume, delete, fire — the read-only list/inspect verbs live on the ScheduleQuery tool)", args.Verb)), nil
	}
}

// create maps the create args onto a ScheduleSpec and drives the manager's
// create-seam. It performs ONLY the structural args→spec mapping (trigger XOR,
// the RFC 3339 one-shot parse); ALL validation (cron grammar, the Mutating/Mode
// invariant, the profile-aware workspace check, the cadence floor) lives in the
// create-seam — the tool never re-implements it (the one-path discipline).
func (t *ScheduleTool) create(ctx context.Context, call session.ToolCall, args scheduleArgs, environmentRef session.EnvironmentRef) session.ToolResult {
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
	origin := sessionOriginFromContext(ctx)
	spec := port.ScheduleSpec{
		Name:     strings.TrimSpace(args.Name),
		Prompt:   args.Prompt,
		Trigger:  trigger,
		Profile:  args.Profile,
		Mode:     mode,
		Mutating: args.Mutating,
		MaxFires: args.MaxFires,
		Timezone: args.Timezone,
		// The origin comes from the RUN CONTEXT and from nowhere else, and this
		// literal is the only place it is ever set (ADR 0209). scheduleArgs has
		// no origin field, so a model-supplied one cannot reach it — do NOT add
		// an `args.Origin ?: ctx` fallback, which is exactly the forgery this
		// closes. An unbound context yields the empty id, which means NO
		// delivery (the delivery path early-returns), never delivery to an
		// arbitrary session; that is the fail-safe direction and it is what an
		// out-of-band create gets by design (ADR 0075 decision #1).
		OriginSessionID: origin,
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
	if origin != "" {
		spec.EnvironmentRef = environmentRef
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
// last-fire-stop summary shared by create + list. When the spec carries a
// per-fire wall-clock FireTimeout (issue #386), it is appended so a create/list
// surfaces the bound the in-flight fire runs under.
func renderScheduleSummary(s port.Schedule) string {
	state := "disabled"
	if s.State.DeletionID != "" {
		state = "deletion_pending=true (retry delete)"
	} else if s.State.Enabled {
		state = "enabled"
	}
	next := "none"
	if !s.State.NextFireAt.IsZero() {
		next = s.State.NextFireAt.UTC().Format(time.RFC3339)
	}
	out := fmt.Sprintf("%s [%s] next=%s %s", s.Spec.Name, renderTrigger(s.Spec.Trigger), next, state)
	if s.Spec.FireTimeout > 0 {
		out += " fire_timeout=" + s.Spec.FireTimeout.String()
	}
	return out
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
// (id, fired-at, terminal stop reason, error), most-recent-first. An IN-FLIGHT
// fire (issue #386) is rendered with an explicit "in-flight" marker plus its
// started/last-progress/deadline instants — it is NEVER silently rendered as
// "none". A CLAIMED fire (the post-Claim, pre-RecordFireStart state where
// State.LastFireSessionID is the "pending" sentinel) is rendered as an explicit
// "in-flight: claimed (session pending)" line, so a claimed-but-not-yet-run
// fire never renders as "fires: none" (the genuinely-never-fired case).
func renderScheduleInspect(sched port.Schedule, fires []port.ScheduleFire) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Schedule %q\n  %s\n  fire count: %d", sched.Spec.Name, renderScheduleSummary(sched), sched.State.FireCount)

	// A CLAIMED fire: Claim happened (LastFireSessionID == "pending") but the
	// run has not yet started (LastFireStartedAt zero AND no in-flight fire
	// record). Render it explicitly instead of "fires: none" so a claimed fire
	// is never mistaken for a schedule that has never fired.
	claimedPending := string(sched.State.LastFireSessionID) == string(port.PendingFireSessionID) &&
		sched.State.LastFireStartedAt.IsZero()
	if claimedPending && len(fires) == 0 {
		b.WriteString("\n  in-flight: claimed (session pending)")
	}

	if len(fires) == 0 {
		if !claimedPending {
			b.WriteString("\n  fires: none")
		}
		return b.String()
	}
	sorted := append([]port.ScheduleFire(nil), fires...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].FiredAt.After(sorted[j].FiredAt) })

	// A schedule-level in-flight summary when the state shows a live fire (the
	// state's LastFireStartedAt is set, or any fire record is in-flight — Stop
	// empty). Rendered once before the per-fire lines so the model sees the
	// active fire's liveness window at a glance.
	inFlight := !sched.State.LastFireStartedAt.IsZero()
	if !inFlight {
		for _, f := range sorted {
			if f.Stop == "" {
				inFlight = true
				break
			}
		}
	}
	if inFlight {
		b.WriteString("\n  in-flight:")
		if !sched.State.LastFireStartedAt.IsZero() {
			fmt.Fprintf(&b, " started %s", sched.State.LastFireStartedAt.UTC().Format(time.RFC3339))
		}
		if !sched.State.LastFireProgressAt.IsZero() {
			fmt.Fprintf(&b, " last-progress %s", sched.State.LastFireProgressAt.UTC().Format(time.RFC3339))
		}
		if !sched.State.FireDeadline.IsZero() {
			fmt.Fprintf(&b, " deadline %s", sched.State.FireDeadline.UTC().Format(time.RFC3339))
		}
	}

	b.WriteString("\n  fires (most recent first):")
	for _, f := range sorted {
		if f.Stop == "" {
			// In-flight fire: render with the in-flight marker + the per-fire
			// started/last-progress/deadline instants (issue #386). It is
			// never rendered as a terminal stop reason.
			fmt.Fprintf(&b, "\n  - fire %s (session %s) at %s: in-flight", f.ID, f.SessionID, f.FiredAt.UTC().Format(time.RFC3339))
			if !f.StartedAt.IsZero() {
				fmt.Fprintf(&b, " started %s", f.StartedAt.UTC().Format(time.RFC3339))
			}
			if !f.ProgressAt.IsZero() {
				fmt.Fprintf(&b, " last-progress %s", f.ProgressAt.UTC().Format(time.RFC3339))
			}
			if !f.Deadline.IsZero() {
				fmt.Fprintf(&b, " deadline %s", f.Deadline.UTC().Format(time.RFC3339))
			}
			continue
		}
		stop := string(f.Stop)
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
