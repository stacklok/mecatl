package server

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/stacklok/mecatl/internal/adapter/memory"
	"github.com/stacklok/mecatl/internal/syscaller"
)

// contextType is the reflect.Type of context.Context, used by
// requireCallerOwnedContext to check a KindCallerOwned method's first
// parameter.
var contextType = reflect.TypeOf((*context.Context)(nil)).Elem()

// AccessKind is one of the four classifications ADR 0102 decision 2 requires
// for every designated application object-touching boundary.
type AccessKind int

const (
	// KindCallerOwned means the boundary itself resolves the ownership
	// decision (ownsResource/authorizeSession/authorizeSchedule, or an
	// equivalent per-kind check such as memory.CallerStore's context-derived
	// namespace) on every call, denying a foreign caller as absence.
	KindCallerOwned AccessKind = iota
	// KindDerived means the boundary carries no independent decision of its
	// own: it operates on an identifier a caller can only obtain from an
	// ALREADY-classified caller-owned boundary (e.g. the id a CreateSession
	// call just returned, or an id a Cancel/EndSession call already
	// authorized before reaching it). It resolves ownership by construction,
	// not by re-checking.
	KindDerived
	// KindSharedInfrastructure means the boundary is a classified, narrow,
	// non-caller-identified operation — an internal system-principal root
	// (ADR 0100 decision 7) or a process-wide catalog/config read that is,
	// by design, the same for every caller.
	KindSharedInfrastructure
	// KindExempt is an explicit, reviewed carve-out for a boundary that
	// structurally cannot carry caller identity — a pure composition-time
	// wiring setter/accessor, a lifecycle control, or a workspace-path-scoped
	// (not caller-scoped) read gated by the pre-existing project-trust axis.
	KindExempt
)

func (k AccessKind) String() string {
	switch k {
	case KindCallerOwned:
		return "caller-owned"
	case KindDerived:
		return "derived"
	case KindSharedInfrastructure:
		return "shared-infrastructure"
	case KindExempt:
		return "exempt"
	default:
		return fmt.Sprintf("AccessKind(%d)", int(k))
	}
}

// ClassificationEntry is one boundary's per-kind table row (ADR 0102 decision
// 2). Rationale is MANDATORY: a shared-infrastructure/exempt entry that
// cannot state a concrete, reviewable reason is rejected by validate, so an
// exemption can never become a silent caller-owned bypass (AC5.3).
type ClassificationEntry struct {
	Kind      AccessKind
	Rationale string
}

// minReviewableRationale is the floor length a shared-infrastructure/exempt
// rationale must clear to count as a reviewable reason rather than a
// rubber-stamped restatement of the kind ("infra", "n/a", "internal").
// Caller-owned/derived entries are exempt from the floor: their rationale
// documents WHICH existing decision applies (ownsResource, a parent id, …),
// which is often a short, precise pointer.
const minReviewableRationale = 24

// blanketBypassPhrases are phrases that read as a universal bypass rather
// than a narrow, reviewable exemption. A shared-infrastructure/exempt entry
// containing one of these fails validate — the rationale must name the
// SPECIFIC narrow reason, never wave the check away wholesale.
var blanketBypassPhrases = []string{
	"always allow",
	"no check needed",
	"bypass everything",
	"skip ownership",
	"not applicable",
}

// validate reports the entry's own defect, or nil if it is well-formed. It
// does not know the boundary name (the caller adds that context).
func (e ClassificationEntry) validate() error {
	r := strings.TrimSpace(e.Rationale)
	if r == "" {
		return errors.New("missing rationale")
	}
	if e.Kind != KindCallerOwned && e.Kind != KindDerived && len(r) < minReviewableRationale {
		return fmt.Errorf("rationale %q is too short to review (shared-infrastructure/exempt entries need a concrete, narrow reason, >= %d chars)", e.Rationale, minReviewableRationale)
	}
	low := strings.ToLower(r)
	for _, phrase := range blanketBypassPhrases {
		if strings.Contains(low, phrase) {
			return fmt.Errorf("rationale %q reads like a blanket bypass, not a narrow exemption", e.Rationale)
		}
	}
	return nil
}

// classificationReport is the guard's finding for one classified surface: the
// boundary names present in code but missing (or misclassified) in the table,
// and the table entries that no longer name a real boundary (stale — AC5.1).
type classificationReport struct {
	Surface       string
	Unclassified  []string
	Misclassified []string
	Stale         []string
}

func (r classificationReport) Errors() []error {
	var errs []error
	for _, name := range r.Unclassified {
		errs = append(errs, fmt.Errorf("%s: unclassified access boundary %q — add a ClassificationEntry (ADR 0102 decision 2)", r.Surface, name))
	}
	for _, name := range r.Misclassified {
		errs = append(errs, fmt.Errorf("%s: misclassified access boundary %q", r.Surface, name))
	}
	for _, name := range r.Stale {
		errs = append(errs, fmt.Errorf("%s: stale classification table entry %q — boundary no longer exists, remove it", r.Surface, name))
	}
	return errs
}

// classifyNames is the guard's core comparison: every name in boundaries must
// resolve to exactly one valid table entry; every table key not present in
// boundaries is stale. It is the single function the production guard AND
// the AC5.2 fixture test drive, so the fixture proves the SAME logic that
// gates CI, not a parallel copy.
func classifyNames(surface string, table map[string]ClassificationEntry, boundaries []string) classificationReport {
	report := classificationReport{Surface: surface}
	seen := make(map[string]bool, len(boundaries))
	for _, name := range boundaries {
		seen[name] = true
		entry, ok := table[name]
		if !ok {
			report.Unclassified = append(report.Unclassified, name)
			continue
		}
		if err := entry.validate(); err != nil {
			report.Misclassified = append(report.Misclassified, fmt.Sprintf("%s (%s)", name, err))
		}
	}
	for name := range table {
		if !seen[name] {
			report.Stale = append(report.Stale, name)
		}
	}
	sort.Strings(report.Unclassified)
	sort.Strings(report.Misclassified)
	sort.Strings(report.Stale)
	return report
}

// exportedMethodNames returns every exported method name on t. reflect.Type's
// NumMethod/Method report only EXPORTED methods for a non-interface type
// (regardless of caller package), so this is exactly the application-facade
// surface a caller — a gRPC/HTTP/ACP/mecatui client — can reach.
//
// A name ending in "ForTest" is excluded: this codebase's established
// convention (export_test.go, see AGENTS.md/docs/design test-writer
// guidance) for a test-only seam exported ONLY to cross the package boundary
// from an external test — it exists solely in test binaries, is never part
// of the real application facade, and carries no independent caller-identity
// decision of its own to classify.
func exportedMethodNames(t reflect.Type) []string {
	names := make([]string, 0, t.NumMethod())
	for i := 0; i < t.NumMethod(); i++ {
		name := t.Method(i).Name
		if strings.HasSuffix(name, "ForTest") {
			continue
		}
		names = append(names, name)
	}
	return names
}

// requireCallerOwnedContext walks every table entry classified KindCallerOwned
// and fails if the underlying method on t has no leading context.Context
// parameter (after the receiver) — such a method cannot possibly consult a
// caller's principal, so KindCallerOwned would be a contradiction (issue #368
// task 08: this is exactly the pre-fix shape of Service.Subscribe, which
// claimed no classification error yet took no ctx at all). It does not verify
// the method actually USES the ctx — that stays a human-reviewed judgment
// call.
func requireCallerOwnedContext(surface string, t reflect.Type, table map[string]ClassificationEntry) []error {
	var errs []error
	for name, entry := range table {
		if entry.Kind != KindCallerOwned {
			continue
		}
		m, ok := t.MethodByName(name)
		if !ok {
			// Not a real method on this type (e.g. a stale/unrelated table
			// entry) — classifyNames already reports that separately.
			continue
		}
		// m.Type is the method expression: In(0) is the receiver, In(1) would
		// be the first real parameter.
		if m.Type.NumIn() < 2 || m.Type.In(1) != contextType {
			errs = append(errs, fmt.Errorf("%s: %q is classified caller-owned but its first parameter is not context.Context — it cannot consult a caller's principal", surface, name))
		}
	}
	return errs
}

// serviceAccessTable classifies every exported *Service method — the
// application-facade, in-memory-registry, and event-relay boundaries ADR 0102
// decision 2 names. See AccessKind's doc comment for what each kind means.
//
// Adding an exported Service method requires an entry here or
// TestInvariant_owned_access_is_classified fails, naming the method.
var serviceAccessTable = map[string]ClassificationEntry{
	// --- caller-owned: sessions ---
	"CreateSession":             {KindCallerOwned, "binds the verified context principal as owner atomically with visibility (reserveSessionID)"},
	"CreateSessionWithProvider": {KindCallerOwned, "delegates to CreateSessionWithProfile's atomic owner bind"},
	"CreateSessionWithProfile":  {KindCallerOwned, "atomic owner bind at creation (reserveSessionID); ForkSession/carryover sources are authorized via authorizeSession before copying history"},
	"CreateSessionWithMCP":      {KindCallerOwned, "delegates to CreateSessionWithProfile's atomic owner bind"},
	"GetSession":                {KindCallerOwned, "authorizeSession: owner mismatch or absence both return ErrNotFound"},
	"LoadSession":               {KindCallerOwned, "authorizeSession before rehydration"},
	"LoadSessionWithMCP":        {KindCallerOwned, "delegates to LoadSession's authorizeSession before mounting client MCP"},
	"SetMode":                   {KindCallerOwned, "authorizes via GetSession before changing the session's permission mode"},
	"ForkSession":               {KindCallerOwned, "authorizes the SOURCE session (authorizeSession) before copying its history to a new owned session"},
	"EndSession":                {KindCallerOwned, "authorizes via GetSession before CloseSession"},
	"ListSessions":              {KindCallerOwned, "filters to the caller's own rows before any pagination/count is computed"},
	"StreamSessionEvents":       {KindCallerOwned, "event log/live stream resolves through the owning session's authorizeSession check"},
	"Subscribe":                 {KindCallerOwned, "authorizes via GetSession before registering a live subscriber (issue #368)"},

	// --- caller-owned: live run verbs ---
	"StartRun":        {KindCallerOwned, "delegates to StartRunContent's run-entry authorization"},
	"StartRunContent": {KindCallerOwned, "loadAndReopen authorizes the session before entering/resuming the run"},
	"Approve":         {KindCallerOwned, "delegates to ApproveRun's authorization"},
	"ApproveRun":      {KindCallerOwned, "same-process path authorizes via the registered run's owning session; the cross-process resumeFromAwaiting path authorizes via loadAndReopen"},
	"ApprovePlan":     {KindCallerOwned, "authorizes the session before resolving the parked plan ask"},
	"Cancel":          {KindCallerOwned, "authorizes via GetSession before signalling the in-flight run"},
	"CancelChild":     {KindCallerOwned, "authorizes the PARENT session via GetSession before reaching into its child registry"},
	"Persist":         {KindCallerOwned, "authorizes via GetSession before consulting the live run registry"},

	// --- caller-owned: schedules ---
	"CreateSchedule": {KindCallerOwned, "the schedule manager binds the verified context principal as owner atomically with visibility"},
	"GetSchedule":    {KindCallerOwned, "authorizeSchedule: owner mismatch or absence both return ErrScheduleNotFound"},
	"ListSchedules":  {KindCallerOwned, "filters to owned schedules before any list metadata is computed"},
	"UpdateSchedule": {KindCallerOwned, "authorizes via GetSchedule before delegating the write"},
	"DeleteSchedule": {KindCallerOwned, "authorizes via GetSchedule before delegating the delete"},
	"PauseSchedule":  {KindCallerOwned, "authorizes via GetSchedule before delegating the pause"},
	"ResumeSchedule": {KindCallerOwned, "authorizes via GetSchedule before delegating the resume"},
	"GetFire":        {KindCallerOwned, "the manager loads the fire's parent schedule directly by its stored physical key and compares owners (no longer via GetSchedule); denies with fireNotFoundErr on mismatch"},
	"ListFires":      {KindCallerOwned, "authorizes the parent schedule via GetSchedule before listing its fires"},
	"FireNow":        {KindCallerOwned, "authorizes via GetSchedule before manually firing"},

	// --- caller-owned: teams ---
	"CreateTeam":          {KindCallerOwned, "binds the verified context principal as the team's owner at registration"},
	"SpawnTeammate":       {KindCallerOwned, "authorizes via the shared lookupTeam (ownsResource) before enrolling a member"},
	"SendTeammateMessage": {KindCallerOwned, "authorizes via the shared lookupTeam before posting to a member's inbox"},
	"CancelTeammate":      {KindCallerOwned, "authorizes via the shared lookupTeam before cancelling a member mid-round"},
	"RunTeam":             {KindCallerOwned, "authorizes via the shared lookupTeam before claiming and driving the team"},
	"ListTeam":            {KindCallerOwned, "authorizes via the shared lookupTeam before reading the roster/tasks"},
	"CleanupTeam":         {KindCallerOwned, "authorizes via ownsResource before dropping the registry entry (issue #368 task 06 fix — CleanupTeam previously ignored its ctx)"},

	// --- derived: resolve ownership through an already-classified caller-owned call ---
	"SessionCapabilities":   {KindDerived, "reads the per-session engine registry keyed on an id the caller only holds from an authorized CreateSession*/GetSession* echo; carries no ctx to re-check"},
	"ResolvedModel":         {KindDerived, "mirrors SessionCapabilities: identity read off the per-session engine registry for an id the caller already authorized to obtain"},
	"LookupRun":             {KindDerived, "in-memory run registry read; every caller-facing entry point (Cancel, Persist, Approve*, MaybeAutoApprovePlan) authorizes the session FIRST and only then consults this"},
	"IsLive":                {KindDerived, "same in-flight registry as LookupRun; consumed by the composition-owned child-GC liveness predicate, not a caller-facing verb"},
	"FinishRun":             {KindDerived, "deregisters an id the wire adapter already finished draining from its own authorized run"},
	"PublishSessionEvent":   {KindDerived, "publishes to subscribers already registered via the (caller-owned) Subscribe for this id; PublishSessionEvent itself takes no ctx and makes no independent decision"},
	"AppendRunEvent":        {KindDerived, "durable-append passthrough to the single appendEvent chokepoint for an id the in-process caller (the scheduler fire loop) already owns via its own run"},
	"RecoverNotice":         {KindDerived, "pops a notice keyed by id that only the relay's own immediately-preceding, already-authorized StartRunContent call could have set"},
	"SetSessionEnvironment": {KindDerived, "called only with the id CreateSession* just returned to the same caller (internal/adapter/acp); renamed from SetSessionWorkspace by the execution-environments refactor"},
	"CloseSession":          {KindDerived, "internal cleanup for an id the caller (EndSession, already authorized) or the owning connection has already established as its own; takes no ctx"},
	"EmitScheduleEvent":     {KindDerived, "stamps the fire's ALREADY-established actor (the scheduler's system principal for a tick fire, or FireNow's caller) captured at fire time; makes no independent ownership decision"},
	"MaybeAutoApprovePlan":  {KindDerived, "invoked from relayEvent only for an id the SAME request's already-authorized StartRunContent/ApprovePlan call is streaming"},

	// --- shared infrastructure: process-wide catalog/config, same for every caller by design ---
	"ListMcpResources":   {KindSharedInfrastructure, "MCP servers are process-wide composition config, not a caller-owned record; every caller may list a wired server's resources"},
	"ReadMcpResource":    {KindSharedInfrastructure, "reads a resource off a process-wide MCP server registration, not a caller-owned record"},
	"ListMcpPrompts":     {KindSharedInfrastructure, "reads prompt snapshots off a process-wide MCP server registration"},
	"GetMcpPrompt":       {KindSharedInfrastructure, "expands a prompt on a process-wide MCP server registration"},
	"ListMcpSources":     {KindSharedInfrastructure, "the deployment-wide MCP source inventory, identical for every caller"},
	"ListToolHiveGroups": {KindSharedInfrastructure, "derives group names from the same deployment-wide MCP source inventory as ListMcpSources"},
	"ListAgents":         {KindSharedInfrastructure, "the deployment's configured agent-definition catalog, identical for every caller"},
	"ListSkills":         {KindSharedInfrastructure, "the deployment's configured skill catalog, identical for every caller"},
	"ListModels":         {KindSharedInfrastructure, "the deployment's model catalog/live listing, identical for every caller"},
	"GetSoul":            {KindSharedInfrastructure, "the deployment's configured soul, identical for every caller"},

	// --- derived: user-model memory delegates entirely to the caller-partitioned store ---
	"GetUserModel":       {KindDerived, "delegates to cfg.UserModel.List, the caller-partitioned memory.CallerStore already classified caller-owned"},
	"GetUserModelDetail": {KindDerived, "delegates to cfg.UserModel's optional lifecycle Inspect, preserving the same caller-partitioned memory.CallerStore boundary for current value and history"},

	// --- exempt: workspace-path-scoped (project trust), not caller-identity-scoped ---
	"ListCommands":  {KindExempt, "scoped by filesystem workspace path under the pre-existing project-trust gate, not caller identity — out of ADR 0102's per-caller kind table"},
	"ListWorktrees": {KindExempt, "scoped by filesystem workspace path under the pre-existing project-trust gate, not caller identity"},

	// --- exempt: composition-time wiring / process lifecycle, structurally caller-free ---
	"SetModels":              {KindExempt, "composition-time model-catalog setter; no per-caller identity exists at this call site"},
	"SetModelsRefresher":     {KindExempt, "composition-time wiring of the live-catalog refresh callback"},
	"SetProviderStatus":      {KindExempt, "composition-time provider-status setter, process-wide state"},
	"ProviderStatuses":       {KindExempt, "reads the process-wide provider-status set SetProviderStatus writes"},
	"ProviderCapabilities":   {KindExempt, "the deployment's default provider capability set, identical for every caller"},
	"Close":                  {KindExempt, "process shutdown; not a per-request caller-facing operation"},
	"Drain":                  {KindExempt, "process drain-gate arm; not a per-request caller-facing operation"},
	"Diagnostics":            {KindExempt, "returns the injected port.Diagnostics sink, a composition-time wiring accessor"},
	"IsDraining":             {KindExempt, "reads the process-wide drain flag Drain sets"},
	"ActiveRuns":             {KindExempt, "process-wide in-flight run count, an operator/health metric with no per-caller identity"},
	"StorageReady":           {KindExempt, "a storage-backend health probe, not a caller-owned resource read"},
	"HasScheduler":           {KindExempt, "reports whether a scheduler is wired, composition-time state"},
	"SetScheduler":           {KindExempt, "composition-time wiring of the scheduler instance"},
	"SetScheduleMinInterval": {KindExempt, "composition-time configuration of the scheduling frequency floor"},
	"ScheduleManager":        {KindExempt, "composition-time accessor for the context-free manager handle; Schedule and ScheduleQuery are separately classified caller-owned consumers"},

	// --- exempt: composition-owned session staleness sweep (issue #475), process-wide by design ---
	"SessionStale":       {KindExempt, "decides staleness for a metadata-scan candidate inside internal/app's composition-owned sweep, never a per-request caller-facing verb (mirrors IsLive)"},
	"LeaseSweepDisabled": {KindExempt, "reads the process-wide sticky sweep-disabled flag SessionStale sets, consumed only by the composition-owned sweep"},
	"SettleIfStale":      {KindExempt, "repairs a stale session found by the composition-owned sweep's own metadata scan across every session, not a caller-supplied id from a caller-owned boundary"},
}

// callerStoreAccessTable classifies memory.CallerStore's exported methods —
// the "cache/index" boundary ADR 0102 decision 2 names (the local backing
// store, search/index, and delete for the caller-partitioned user-model and
// project memory kinds). Every method derives its namespace from the
// context-carried verified principal (session.PrincipalFromContext), so all
// six are caller-owned by construction: a request with no verified principal
// is rejected (memory: verified caller is required), never silently
// namespaced to a shared/default bucket.
// callerStoreScopedRationale is shared by every CallerStore method except
// RememberEntry (which additionally notes the project workspace bind) —
// factored out so the repeated literal doesn't trip goconst.
const callerStoreScopedRationale = "scoped() derives the namespace from the context principal on every call"

var callerStoreAccessTable = map[string]ClassificationEntry{
	"RememberEntry": {KindCallerOwned, "scoped() derives the namespace from the context principal (+ workspace for project memory) on every call"},
	"Recall":        {KindCallerOwned, callerStoreScopedRationale},
	"List":          {KindCallerOwned, callerStoreScopedRationale},
	"Index":         {KindCallerOwned, callerStoreScopedRationale},
	"Search":        {KindCallerOwned, callerStoreScopedRationale},
	"Forget":        {KindCallerOwned, callerStoreScopedRationale},
}

// systemAccessTable classifies each internal/syscaller.Root's explicit
// shared-infrastructure scope (ADR 0102 decision 5): the NARROW operation set
// that root's system principal may perform. A system principal is NEVER a
// universal ownership bypass — every other caller-owned boundary (GetSession,
// GetSchedule, …) denies it exactly like any other non-matching identity
// (TestCallerSeparation_Scenario4_SystemPrincipalIsNotUniversalBypass).
//
// Adding a new internal/syscaller.Root requires an entry here or
// TestInvariant_owned_access_is_classified fails, naming the root.
var systemAccessTable = map[syscaller.Root]ClassificationEntry{
	syscaller.RootChildGC: {
		KindSharedInfrastructure,
		"session retention sweep: deletes only aged, already-terminal snapshots by age/freshness, never reads or exposes a caller's live content",
	},
	syscaller.RootMemoryConsolidation: {
		KindSharedInfrastructure,
		"project-memory dream consolidation: never starts when ownership is enforced; otherwise operates on the raw single-tenant store, so one namespace exists and no cross-caller boundary can be crossed",
	},
	syscaller.RootUserModelConsolidation: {
		KindSharedInfrastructure,
		"user-model dream consolidation: never starts when ownership is enforced; otherwise operates on the raw single-tenant store's user/ namespace, so no cross-caller boundary exists",
	},
	syscaller.RootScheduler: {
		KindSharedInfrastructure,
		"tick/fire/delivery/reconcile loop: keeps scheduler context for claims, bookkeeping, diagnostics, and event attribution; only run-entry calls for an already-captured schedule owner receive that owner context, so no general system bypass or impersonation path exists",
	},
	syscaller.RootJWKSRefresh: {
		KindSharedInfrastructure,
		"background JWKS key refresh for the token validator; touches no session/schedule/team/memory record at all",
	},
	syscaller.RootModelCatalogRefresh: {
		KindSharedInfrastructure,
		"one-shot startup live-model-catalog fetch and swap; reads provider APIs and publishes a process-wide model registry snapshot, identical for every caller, touching no session/schedule/team/memory record at all",
	},
}

// modelToolAccessTable classifies the model-facing tool families that reach a
// caller-owned or shared-infrastructure boundary directly rather than only
// through *Service — ADR 0102 decision 2's "model-tool access boundary" and
// Scenario 3's non-store coverage. Names are the literal tool.ToolSpec.Name
// values (engine/agent keeps its own name constants unexported; these
// literals are the single source the guard and the tests share — see
// ADR 0102 and docs/architecture.md's caller-ownership section).
var modelToolAccessTable = map[string]ClassificationEntry{
	// Project memory tools (Remember/Recall/SearchMemory): backed by
	// memory.CallerStore(project=true), classified caller-owned above.
	// Forget is a CallerStore capability (classified in callerStoreAccessTable)
	// with no registered model-facing tool today — there is nothing to list
	// here until one is added.
	"Remember":     {KindCallerOwned, "backed by memory.CallerStore(project=true); scoped by verified caller + workspace"},
	"Recall":       {KindCallerOwned, "backed by memory.CallerStore(project=true); scoped by verified caller + workspace"},
	"SearchMemory": {KindCallerOwned, "backed by memory.CallerStore(project=true); scoped by verified caller + workspace"},
	// User-model memory (RememberUser/RecallUser/SearchUserModel): backed by
	// memory.CallerStore(project=false), a DISTINCT kind (decision 2) from
	// project memory even where logical keys match.
	"RememberUser":    {KindCallerOwned, "backed by memory.CallerStore(project=false); scoped by verified caller only, a distinct kind from project memory"},
	"RecallUser":      {KindCallerOwned, "backed by memory.CallerStore(project=false); scoped by verified caller only"},
	"SearchUserModel": {KindCallerOwned, "backed by memory.CallerStore(project=false); scoped by verified caller only"},
	// Child/team observability and delegation: these act on a caller-supplied
	// handle (agentId / teamID) inside the SAME run, so the decision is the
	// resuming child/team session's own owner check (engine/agent/teaminspect.go,
	// subagentinspect.go, subagentstatus.go), which SameIdentity-matches the
	// run's caller.
	"InspectSubagent": {KindCallerOwned, "reads a persisted child session's transcript; teaminspect/subagentinspect authorize via sess.Owner.SameIdentity(caller) before returning it"},
	"InspectMember":   {KindCallerOwned, "reads a persisted team-member session's transcript; authorizes via sess.Owner.SameIdentity(caller) before returning it"},
	"SubagentStatus":  {KindCallerOwned, "reads the run-scoped background-child registry (parentCaps.children), which only ever holds children this SAME run's caller spawned"},
	"Subagent":        {KindCallerOwned, "a resume: <agentId> load authorizes the persisted child via sess.Owner.SameIdentity(caller) before recovering or re-persisting it (issue #368 task 09)"},
	"Team":            {KindCallerOwned, "the in-loop Team tool inherits the run's OWN authorized session; RunTeam's gRPC entry point is separately classified above via lookupTeam"},
	"Schedule":        {KindCallerOwned, "every verb (create/list/inspect/pause/resume/delete/fire-now) uses the context-bound owner-namespaced schedule manager, which derives/enforces ownership per verb; GetFire's parent-owner check is a separate gRPC/REST-only boundary this tool never reaches"},
	"ScheduleQuery":   {KindCallerOwned, "the context-bound schedule manager filters list results by owner before rendering model-visible metadata"},
}

// ModelToolBoundaries is the single registry of model-facing tool names ADR
// 0102's "model-tool access boundary" covers — the tools that reach a
// caller-owned or shared-infrastructure decision directly rather than only
// through *Service. It mirrors internal/syscaller.Roots' registry discipline:
// a new tool in this family is added HERE (and to modelToolAccessTable) or
// TestInvariant_owned_access_is_classified fails, naming the gap. Tools
// unrelated to caller ownership (Read, Bash, WebSearch, …) are deliberately
// NOT enumerated — this is a narrow, reviewable boundary list, not the whole
// catalog.
var ModelToolBoundaries = []string{
	"Remember", "Recall", "SearchMemory",
	"RememberUser", "RecallUser", "SearchUserModel",
	"InspectSubagent", "InspectMember", "SubagentStatus", "Subagent", "Team",
	"Schedule", "ScheduleQuery",
}

// ClassifyServiceBoundaries walks every exported *Service method (the
// application-facade, in-memory-registry, and event-relay boundary) and
// reports one error per unclassified, misclassified, or stale entry.
func ClassifyServiceBoundaries() []error {
	t := reflect.TypeOf(&Service{})
	names := exportedMethodNames(t)
	errs := classifyNames("server.Service", serviceAccessTable, names).Errors()
	errs = append(errs, requireCallerOwnedContext("server.Service", t, serviceAccessTable)...)
	return errs
}

// ClassifyCallerStoreBoundaries walks every exported memory.CallerStore
// method (the cache/index boundary for caller-partitioned memory).
func ClassifyCallerStoreBoundaries() []error {
	t := reflect.TypeOf(&memory.CallerStore{})
	names := exportedMethodNames(t)
	errs := classifyNames("memory.CallerStore", callerStoreAccessTable, names).Errors()
	errs = append(errs, requireCallerOwnedContext("memory.CallerStore", t, callerStoreAccessTable)...)
	return errs
}

// ClassifySystemBoundaries walks every registered internal/syscaller.Root
// (the explicit shared-infrastructure system-principal scopes, ADR 0102
// decision 5).
func ClassifySystemBoundaries() []error {
	table := make(map[string]ClassificationEntry, len(systemAccessTable))
	for root, entry := range systemAccessTable {
		table[string(root)] = entry
	}
	names := make([]string, 0, len(syscaller.Roots))
	for _, root := range syscaller.Roots {
		names = append(names, string(root))
	}
	return classifyNames("syscaller.Root", table, names).Errors()
}

// ClassifyModelToolBoundaries walks ModelToolBoundaries (the model-tool
// access boundary registry).
func ClassifyModelToolBoundaries() []error {
	return classifyNames("model-tool", modelToolAccessTable, ModelToolBoundaries).Errors()
}

// ClassifyAllBoundaries runs every classified surface's guard and returns the
// concatenated findings — the single entry point
// TestInvariant_owned_access_is_classified drives (AC5.1).
func ClassifyAllBoundaries() []error {
	var errs []error
	errs = append(errs, ClassifyServiceBoundaries()...)
	errs = append(errs, ClassifyCallerStoreBoundaries()...)
	errs = append(errs, ClassifySystemBoundaries()...)
	errs = append(errs, ClassifyModelToolBoundaries()...)
	return errs
}
