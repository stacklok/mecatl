/**
 * A fixture mecated for e2e: a tiny HTTP server speaking just enough of the
 * daemon's wire (stdlib-JSON responses, SSE prompt relay) for Studio's
 * surfaces to render real content through the real proxy tier. This is a test
 * double behind the API seam — not a UI-level demo fallback, which Studio
 * forbids.
 *
 * Studio drives every request through the TypeScript SDK
 * (`@stacklok-oss/mecatl-sdk`), so the fixture speaks the routes the SDK's
 * rpc-catalog resolves (sdk/typescript/src/rpc-catalog.ts): the SDK first
 * GETs /v1/compatibility and gates features on it, the prompt SSE frames are
 * proto-JSON events carrying `run_id` and ending with a `result`, and the
 * durable watch is an SSE of `{event, cursor, phase}` envelopes. Shapes are
 * the minimum the SDK accepts (sdk/typescript/test/scripted-state.ts); the
 * fixture is deliberately permissive — it rejects unknown fields nowhere.
 */
import http from "node:http";

const port = Number(process.env.FIXTURE_DAEMON_PORT || 8099);

const sessionID = "session-fixture-1";
// The successor `/clear` mints (an empty-history session the UI opens).
const clearedSessionID = "session-fixture-clear";
const runID = "run-fixture-1";
// The MCP browser-authorization phase: a prompt whose text asks for
// "authorization" parks its tool call on this authorization and the stream
// ENDS without a result (the daemon's parked shape); the recheck/cancel
// controls stream the resolution plus the continuation run.
const authorizationID = "auth-fixture-1";
const continuationRunID = "run-fixture-2";
const placement = {
  kind: "local",
  label: "fixture",
  branch: "main",
  revision: "fixture",
};

const session = {
  session_id: sessionID,
  title: "Fix the flaky scheduler test",
  // The kind taxonomy + the activity-state projection the sidebar's kind
  // tabs read (the latter rides the `session_activity_inventory` feature).
  kind: "main",
  activity_state: "active",
  state: "idle",
  mode: "default",
  turns: 2,
  model_id: "fixture-model",
  placement,
  created_at_unix: 1_755_000_000,
  modified_at_unix: 1_755_003_600,
  capabilities: {
    rename: true,
    delete: true,
    view_transcript: true,
    // The `/session` dialog's Copy button is gated on this (omitted = denied).
    copy_id: true,
    // The successor offer: gates the header menu's Clear conversation (and
    // the fork-shaped model/effort switch). An idle main chat has it.
    fork: true,
    reasons: {},
  },
};

// The inspect-only inventory the sidebar's Runs / Scheduled tabs list: a
// child of the fixture chat and a scheduler fire. Both are stamped
// `inspect_only_kind` (not a public chat) and offer their transcript.
const subagentSessionID = "subagent-fixture-stored";
const subagentSession = {
  session_id: subagentSessionID,
  title: "Scan the scheduler tests",
  kind: "subagent",
  activity_state: "active",
  state: "completed",
  turns: 3,
  model_id: "fixture-model",
  placement,
  relationship: { parent_session_id: sessionID, call_id: "call-fixture-1" },
  created_at_unix: 1_755_001_000,
  modified_at_unix: 1_755_002_000,
  capabilities: {
    inspect: true,
    view_transcript: true,
    copy_id: true,
    reasons: { public_chat: "inspect_only_kind" },
  },
};
const scheduledSessionID = "sched-fixture-stored";
const scheduledSession = {
  session_id: scheduledSessionID,
  title: "",
  kind: "scheduled",
  activity_state: "active",
  state: "completed",
  turns: 1,
  model_id: "fixture-model",
  placement,
  relationship: { schedule_name: "nightly-fixture-digest" },
  created_at_unix: 1_755_000_500,
  modified_at_unix: 1_755_000_900,
  capabilities: {
    inspect: true,
    view_transcript: true,
    copy_id: true,
    reasons: { public_chat: "inspect_only_kind" },
  },
};

// The deployment capabilities Studio's surfaces gate on (proto
// ServerCapabilities, snake_case on the wire): every inventory the fixture
// serves is advertised, so the pages render rather than hide themselves.
const capabilities = {
  mcp: true,
  slash_commands: true,
  memory: true,
  skills: true,
  teams: true,
  bash: true,
  image: false,
  audio: false,
  agents: true,
  soul: true,
  user_model: true,
  model_selection: true,
  posture: "trusted",
  worktrees: true,
  scheduling: true,
  storage_health: true,
  storage_migration: true,
  storage_cleanup: true,
  steer: true,
  manual_compaction: true,
  // Workspace-services enrollment (the TUI's /tools-connect): the notice above
  // the composer and its connect/retry/cancel controls. Connector inspection
  // lets Studio skip the notice on an already-connected chat.
  workspace_enrollment: true,
  mcp_connector_status: true,
  // "Debug with AI" (ADR 0254): the sidebar row's menu item, its consent
  // dialog, and the attachable reporting servers the dialog lists from
  // GET /v1/mcp/sources (the fixture's `fixture-mcp`).
  session_debug: true,
  debug_mcp: true,
  // The learning review queue (ADR 0109): Settings → Learning's proposal
  // list/detail/decision/undo routes below, and the sibling Reflect card's
  // POST /v1/sessions/{id}/reflect.
  learning_proposals: true,
  reflection: true,
};

const snapshot = {
  session_id: sessionID,
  mode: "default",
  state: "idle",
  // The `/session` details dialog reads kind, title (+ provenance), creation
  // time, turn/tool-call counts and limits off this same snapshot. The WIRE
  // shape is the proto's `Session.title_metadata` (SessionTitle{title,
  // provenance}); the SDK projects it to `{value, provenance}` — sending the
  // projected shape here fails protobuf-es fromJson and poisons the snapshot.
  kind: "main",
  title_metadata: {
    title: "Fix the flaky scheduler test",
    provenance: "first-prompt",
  },
  created_at_unix: 1_755_000_000,
  turns: 2,
  tool_calls: 3,
  limits: { max_turns: 50, max_tool_calls: 200, max_consecutive_failures: 3 },
  placement,
  // The proto field is `model_id` (SessionResolvedModel), and the context
  // meter needs the window to draw its bar.
  resolved_model: {
    model_id: "fixture-model",
    provider_id: "fixture",
    context_window: 128000,
    // The EFFECTIVE reasoning-effort tier: the live picker checkmarks it and
    // the composer trigger reads "fixture-model · Medium".
    reasoning_effort: "medium",
  },
  // The durable session-cumulative usage (engine/session/usage.go "main").
  token_usage: {
    main: { total: { input_tokens: 120, output_tokens: 40 }, models: {} },
  },
  capabilities,
};

// Learning review queue (proto LearningProposal, stdlib-JSON: snake_case,
// `{seconds}` timestamps, int64 event_seq as a number): one staged fact
// whose evidence still resolves, and one deferred procedure whose evidence
// is gone — the Pending/Deferred pills, the Approve gate, and the Details
// provenance rows all have something to show. `GET /v1/learning/proposals`
// filters these by the `status` query in the server below.
const learningProposals = [
  {
    id: "prop-1",
    version: "2",
    status: "staged",
    kind: "fact",
    key: "scheduler/flaky-test",
    value: "The scheduler test races the claim sentinel; run it with -count=3.",
    description: "How the flaky scheduler test was diagnosed",
    triggers: ["scheduler", "flaky"],
    evidence: [
      {
        session_id: sessionID,
        locator: "tool",
        ordinal: 1,
        event_seq: 42,
        tool_call_id: "call-fixture-1",
        digest: "sha256:0123456789abcdef",
        available: true,
        availability: "",
        preview: "$ go test ./internal/scheduler -run TestClaim -count=3\nok",
      },
    ],
    decisions: [],
    created_at: { seconds: 1_755_000_000 },
    updated_at: { seconds: 1_755_003_600 },
    project_scoped: false,
    promotion_available: true,
    promotion_unavailable_reason: "",
    learned_skill_id: "",
  },
  {
    id: "prop-2",
    version: "1",
    status: "deferred_unsupported",
    kind: "procedure",
    key: "release/tag",
    title: "Tag a release",
    body: "1. Bump the version\n2. Tag\n3. Push the tag",
    triggers: ["release"],
    evidence: [
      {
        session_id: "session-fixture-gone",
        locator: "tool",
        ordinal: 4,
        event_seq: 88,
        tool_call_id: "",
        digest: "sha256:feedfacecafebeef",
        available: false,
        availability: "source session deleted",
        preview: "",
      },
    ],
    decisions: [
      {
        kind: "defer",
        actor: "daemon",
        reason: "skills were unsupported",
        at: { seconds: 1_755_000_100 },
      },
    ],
    created_at: { seconds: 1_755_000_000 },
    updated_at: { seconds: 1_755_000_100 },
    project_scoped: false,
    promotion_available: true,
    promotion_unavailable_reason: "",
    learned_skill_id: "",
  },
];

const routes = {
  "GET /v1/compatibility": {
    api_major: 1,
    features: [
      "http_steer",
      "watch_session_events",
      "server_info",
      // Rows carry `activity_state`, so the sidebar offers a Drafts tab.
      "session_activity_inventory",
    ],
    capabilities,
  },
  "GET /v1/info": {
    build_id: "fixture",
    server_implementation: "fixture-daemon",
  },
  // The resolved persona (proto GetSoulResponse → SoulInfo) behind the
  // `soul: true` capability above: Settings → Agent's Persona card and the
  // chat's /soul built-in render it. Stdlib-JSON shape: snake_case fields,
  // the provenance enum as its ordinal (1 = SOUL_PROVENANCE_USER).
  "GET /v1/soul": {
    soul: {
      content: "You are the fixture persona: concise and direct.",
      size_bytes: 49,
      sha256: "a".repeat(64),
      present: true,
      provenance: 1,
      trusted: true,
      drifted: false,
    },
  },
  // The resolved MCP source inventory the "Debug with AI" dialog lists its
  // attachable debugger servers from (proto ListMcpSourcesResponse), and
  // Settings → MCP tools / the chat's MCP tools panel render by source. The
  // ToolHive source is DISABLED (its server never reaches the debug dialog's
  // enabled-only list) and carries one skip reason, so the panel's
  // disabled badge and diagnostics line both have something to show.
  "GET /v1/mcp/sources": {
    sources: [
      {
        name: "static",
        kind: "static",
        enabled: true,
        servers: [
          {
            name: "fixture-mcp",
            url: "http://127.0.0.1:1/mcp",
            transport: "streamable-http",
          },
        ],
      },
      {
        name: "toolhive(default)",
        kind: "toolhive",
        enabled: false,
        group: "default",
        servers: [],
        diagnostics: ["fixture-stdio: skipped, unsupported transport stdio"],
      },
    ],
  },
  // The distinct ToolHive groups (proto ListToolHiveGroupsResponse), listed
  // under the sources as the TUI's "ToolHive groups:" line.
  "GET /v1/mcp/toolhive/groups": { groups: ["default", "research"] },
  // The composer's "Insert from MCP" pickers (the TUI's f8 prompts / ctrl+r
  // resources). ONE prompt with ONE required argument (proto
  // ListMcpPromptsResponse), rendered by POST /v1/mcp/prompts/get into two
  // role-tagged messages (GetMcpPromptResponse — the fixture ignores the
  // posted arguments); ONE text resource (ListMcpResourcesResponse) read by
  // GET /v1/mcp/resources/read as a single text chunk (ReadMcpResourceResponse).
  "GET /v1/mcp/prompts": {
    prompts: [
      {
        server: "fixture-mcp",
        name: "summarize",
        title: "Summarize a file",
        description: "Summarizes one workspace file.",
        arguments: [
          {
            name: "path",
            title: "File path",
            description: "Workspace-relative path of the file.",
            required: true,
          },
        ],
      },
    ],
  },
  "POST /v1/mcp/prompts/get": {
    description: "Summarizes one workspace file.",
    messages: [
      { role: "user", text: "Summarize the file at README.md." },
      { role: "assistant", text: "I will read it first." },
    ],
  },
  "GET /v1/mcp/resources": {
    resources: [
      {
        server: "fixture-mcp",
        uri: "file:///fixture/notes.txt",
        name: "notes.txt",
        title: "Fixture notes",
        description: "Release notes kept by the fixture.",
        mime_type: "text/plain",
        size: 44,
      },
    ],
  },
  "GET /v1/mcp/resources/read": {
    contents: [
      {
        uri: "file:///fixture/notes.txt",
        mime_type: "text/plain",
        text: "Fixture notes: the scheduler test is flaky.",
      },
    ],
  },
  // Proto-JSON GetStorageHealthResponse: a healthy store (no banner) whose
  // effective retention policy, sweep timestamps and family counts the
  // Storage page's Retention card renders. int64s ride as JSON numbers.
  "GET /v1/storage/health": {
    available: true,
    session_count: 3,
    v2_count: 3,
    main_count: 1,
    child_count: 1,
    scheduled_count: 1,
    file_count: 7,
    current_bytes: 20_480,
    current_bytes_available: true,
    reclaimable_bytes: 0,
    reclaimable_bytes_available: true,
    policy: {
      main_max_age_seconds: 0,
      main_max_count: 0,
      child_max_age_seconds: 604_800,
      child_max_count: 500,
      scheduled_max_age_seconds: 604_800,
      scheduled_max_count: 0,
      sweep_cadence_seconds: 3600,
    },
    last_sweep_unix: 1_755_003_000,
    last_sweep_available: true,
    next_sweep_unix: 1_755_006_600,
    next_sweep_available: true,
  },
  // Storage maintenance (advertised by storage_migration / storage_cleanup,
  // so every route the Storage page can reach must answer). Proto-JSON,
  // snake_case, static: the estimate finds two legacy families, apply
  // answers an already-completed job; the clean-up plan has one eligible
  // child run and a token, apply reports it deleted.
  "POST /v1/storage/migrations/plan": {
    plan_id: "plan-fixture-1",
    available: true,
    v1_families: 2,
    v2_families: 3,
    invalid_families: 0,
    skipped_families: 0,
    current_bytes: 20_480,
    reclaimable_bytes: 4_096,
    temporary_bytes: 8_192,
  },
  "POST /v1/storage/migrations/apply": {
    job_id: "migration-fixture-1",
    state: "completed",
    v1_families: 2,
    v2_families: 3,
    processed: 2,
    migrated: 2,
    failed: 0,
    errors: [],
  },
  "GET /v1/storage/migrations/migration-fixture-1": {
    job_id: "migration-fixture-1",
    state: "completed",
    v1_families: 2,
    v2_families: 3,
    processed: 2,
    migrated: 2,
    failed: 0,
    errors: [],
  },
  "POST /v1/storage/migrations/migration-fixture-1/cancel": {
    job_id: "migration-fixture-1",
    state: "cancelled",
    v1_families: 2,
    processed: 1,
    migrated: 1,
    failed: 0,
    errors: [],
  },
  "POST /v1/storage/migrations/migration-fixture-1/resume": {
    job_id: "migration-fixture-1",
    state: "completed",
    v1_families: 2,
    processed: 2,
    migrated: 2,
    failed: 0,
    errors: [],
  },
  "POST /v1/storage/cleanup:plan": {
    confirmation_token: "cleanup-token-fixture",
    available: true,
    generation: "fixture-generation-1",
    policy_version: "fixture-policy-1",
    eligible: [
      {
        session_id: "subagent-fixture-old",
        kind: "subagent",
        state: "completed",
        reason: "age",
        modified_at_unix: 1_754_000_000,
        estimated_bytes: 2_048,
      },
    ],
    protected: {
      total: 2,
      by_kind: { main: 1, scheduled: 1 },
      by_state: { idle: 2 },
      by_reason: { live: 1, active_state: 1 },
    },
    eligible_counts: {
      total: 1,
      by_kind: { subagent: 1 },
      by_state: { completed: 1 },
      by_reason: { age: 1 },
    },
    estimated_bytes: 2_048,
    planned_job_id: "cleanup-fixture-1",
  },
  "POST /v1/storage/cleanup:apply": {
    job_id: "cleanup-fixture-1",
    state: "completed",
    processed: 1,
    deleted: 1,
    skipped: 0,
    stale: 0,
    failed: 0,
    errors: [],
  },
  "GET /v1/storage/cleanup/jobs/cleanup-fixture-1": {
    job_id: "cleanup-fixture-1",
    state: "completed",
    processed: 1,
    deleted: 1,
    skipped: 0,
    stale: 0,
    failed: 0,
    errors: [],
  },
  "POST /v1/storage/cleanup/jobs/cleanup-fixture-1/cancel": {
    job_id: "cleanup-fixture-1",
    state: "cancelled",
    processed: 0,
    deleted: 0,
    skipped: 0,
    stale: 0,
    failed: 0,
    errors: [],
  },
  // The cleared successor renders as an empty chat: its snapshot (the chat
  // hook's handle GET) and an empty, complete transcript.
  [`GET /v1/sessions/${clearedSessionID}`]: {
    ...snapshot,
    session_id: clearedSessionID,
    token_usage: {},
  },
  [`GET /v1/sessions/${clearedSessionID}/transcript`]: {
    session_id: clearedSessionID,
    complete: true,
    messages: [],
  },
  // The fork-as-is copy ("Fork chat" in the row / header menu) renders as the
  // source's conversation carried over: a fork seeds from the source's
  // history, so its transcript is the fixture chat's opening exchange.
  "GET /v1/sessions/session-fixture-fork": {
    ...snapshot,
    session_id: "session-fixture-fork",
  },
  "GET /v1/sessions/session-fixture-fork/transcript": {
    session_id: "session-fixture-fork",
    complete: true,
    messages: [
      { role: "user", text: "Why does the scheduler test flake?" },
      {
        role: "assistant",
        text: "The fixture transcript renders: the test races the claim sentinel.",
      },
    ],
  },
  "GET /v1/models": {
    models: [{ id: "fixture-model", provider_id: "fixture" }],
    // One healthy provider_status row (issue #262): the provider page's
    // read-only daemon status list renders the row with NO hint line for
    // state=ok (a hint accompanies only unreachable/unauthorized/empty).
    provider_status: [
      { provider_id: "fixture", state: "ok", hint: "", model_count: 1 },
    ],
  },
  "GET /v1/sessions": {
    sessions: [session, subagentSession, scheduledSession],
    next_cursor: "",
  },
  // The read-only inspect transcripts the Runs / Scheduled tabs open. The
  // transcript loader (`fetchSessionTranscriptMessages`) takes the SDK's
  // session handle first — `sessions.get` is the snapshot GET — so each
  // stored row serves its snapshot too, agreeing with its inventory row.
  [`GET /v1/sessions/${subagentSessionID}`]: {
    ...snapshot,
    session_id: subagentSessionID,
    kind: "subagent",
    state: "completed",
    title_metadata: {
      title: subagentSession.title,
      provenance: "first-prompt",
    },
    created_at_unix: subagentSession.created_at_unix,
    turns: subagentSession.turns,
    tool_calls: 2,
    token_usage: {},
    capabilities: subagentSession.capabilities,
  },
  [`GET /v1/sessions/${scheduledSessionID}`]: {
    ...snapshot,
    session_id: scheduledSessionID,
    kind: "scheduled",
    state: "completed",
    // A scheduler fire has no title: no title_metadata on the wire.
    title_metadata: undefined,
    created_at_unix: scheduledSession.created_at_unix,
    turns: scheduledSession.turns,
    tool_calls: 0,
    token_usage: {},
    capabilities: scheduledSession.capabilities,
  },
  [`GET /v1/sessions/${subagentSessionID}/transcript`]: {
    session_id: subagentSessionID,
    kind: "subagent",
    complete: true,
    messages: [
      { role: "user", text: "Scan the scheduler tests for flakes." },
      {
        role: "assistant",
        text: "The stored subagent transcript renders: two tests race the claim sentinel.",
      },
    ],
  },
  [`GET /v1/sessions/${scheduledSessionID}/transcript`]: {
    session_id: scheduledSessionID,
    kind: "scheduled",
    complete: true,
    messages: [
      {
        role: "assistant",
        text: "The stored scheduled-fire transcript renders: digest sent.",
      },
    ],
  },
  [`GET /v1/sessions/${sessionID}`]: snapshot,
  [`GET /v1/sessions/${sessionID}/transcript`]: {
    session_id: sessionID,
    complete: true,
    messages: [
      { role: "user", text: "Why does the scheduler test flake?" },
      {
        role: "assistant",
        text: "The fixture transcript renders: the test races the claim sentinel.",
      },
      // A scheduled task's delivery note exactly as the daemon records it
      // (fenced, with the provenance header): Studio renders it as a card.
      {
        role: "user",
        text: "<<<UNTRUSTED\n[scheduled task nightly-fixture-digest (fire fire-1) completed with stop reason: end_turn]\nDigest: 3 PRs merged, 0 failures.\n<<<UNTRUSTED\n",
      },
    ],
  },
  "GET /v1/schedules": {
    schedules: [
      {
        spec: {
          name: "nightly-fixture-digest",
          prompt: "Summarise the day",
          trigger: { cron: "0 9 * * *" },
          timezone: "UTC",
          mode: 2,
          mutating: false,
        },
        // stdlib-JSON Timestamp shape (as the fires fixture below uses); the
        // list's Last run / Runs columns read these.
        state: {
          enabled: true,
          fire_count: 3,
          last_fire_at: { seconds: 1_755_000_000 },
          last_fire_session_id: "sched--nightly-fixture-digest-1",
        },
      },
    ],
  },
  "GET /v1/schedules/nightly-fixture-digest/fires": {
    fires: [
      {
        id: "fire-1",
        schedule_name: "nightly-fixture-digest",
        session_id: "sched--nightly-fixture-digest-1",
        fired_at: { seconds: 1_755_000_000 },
        stop: "end_turn",
      },
    ],
  },
  "GET /v1/skills": {
    skills: [
      {
        name: "code-review-fixture",
        description: "Review a diff for correctness.",
      },
    ],
  },
  "GET /v1/usermodel": {
    entries: [
      {
        key: "prefers-tabs",
        description: "The operator prefers tabs over spaces.",
      },
    ],
    size_bytes: 64,
    sha256: "f".repeat(64),
  },
  "GET /v1/agents": {
    agents: [{ name: "reviewer", description: "Reviews changes." }],
  },
  // Slash-command discovery is keyed by `?session_id=` (placement is
  // server-owned, ADR 0291); the query is accepted and ignored here.
  "GET /v1/commands": { commands: [] },
  // The worktree picker (`/worktrees`): eligible sibling worktrees in `git
  // worktree list` order, each with an OPAQUE selector the clear/fork routes
  // below accept as `worktree_selector` (ADR 0291 — never a path).
  "GET /v1/worktrees": {
    worktrees: [
      {
        selector: "wt-main",
        kind: "git-worktree",
        label: "main",
        branch: "main",
        revision: "abcdef0123456789",
        bare: false,
      },
      {
        selector: "wt-feature",
        kind: "git-worktree",
        label: "feature-x",
        branch: "feature/x",
        revision: "0123456789abcdef",
        bare: false,
      },
    ],
  },
  "POST /v1/sessions": { session_id: sessionID, placement },
  [`POST /v1/sessions/${sessionID}/fork`]: {
    session_id: "session-fixture-fork",
    placement,
  },
  [`POST /v1/sessions/${sessionID}/approve`]: {},
  [`POST /v1/sessions/${sessionID}/cancel`]: {},
  // Per-child cancel (subagent / parallel branch / team member): the real
  // daemon answers 204; the SDK reads the fixture's empty JSON the same way.
  [`POST /v1/sessions/${sessionID}/cancel-child`]: {},
  [`POST /v1/sessions/${sessionID}/steer`]: {
    outcome: "accepted",
    message_id: "steer-fixture-1",
  },
  [`POST /v1/sessions/${sessionID}/cancel-steer`]: { outcome: "retracted" },
  [`POST /v1/sessions/${sessionID}/rename`]: {},
  [`POST /v1/sessions/${sessionID}/delete`]: {},
  [`DELETE /v1/sessions/${sessionID}`]: {},
  // Every route the advertised capabilities make reachable from the UI must
  // answer, or a flow under test dies on a 404 problem instead of its logic:
  // manual compaction, the mode picker, and the six mutating schedule actions.
  [`POST /v1/sessions/${sessionID}/compact`]: { compacted: false },
  // The `/clear` built-in's ClearSession successor (ADR 0291): a distinct
  // empty-history session the UI navigates to; the source stays intact.
  [`POST /v1/sessions/${sessionID}/clear`]: {
    session_id: clearedSessionID,
    placement,
  },
  // The live sign-in URL of the parked MCP authorization (fetched when the
  // operator opens or copies it — it never rides an event).
  [`GET /v1/sessions/${sessionID}/mcp-authorizations/${authorizationID}/presentation`]:
    { url: "https://example.test/authorize?state=fixture" },
  [`POST /v1/sessions/${sessionID}/mode`]: snapshot,
  // The owner-scoped connector inventory (proto ListSessionMcpConnectorsResponse;
  // vocabulary in internal/mcpbroker/connector.go): enrollment required but
  // not begun, so the notice shows on open. The one connector's catalogue is
  // still hidden (the chat's MCP tools panel reads "Awaiting discovery").
  [`GET /v1/sessions/${sessionID}/mcp/connectors`]: {
    availability: "available",
    enrollment_state: "not_started",
    connectors: [
      { name: "fixture-connector", catalogue_state: "hidden", tool_count: 0 },
    ],
    total_connectors: 1,
    truncated: false,
  },
  // Workspace-services enrollment controls (bodyless POSTs; proto
  // WorkspaceEnrollment). The fixture connects straight away — no consent
  // window to drive — so `presentation_url` stays empty, as the daemon sends
  // it on every non-begin response.
  [`POST /v1/sessions/${sessionID}/workspace-enrollment/connect`]: {
    enrollment_id: "enroll-fixture-1",
    status: "connected",
    required_services: 1,
    presentation_url: "",
  },
  [`POST /v1/sessions/${sessionID}/workspace-enrollment/enroll-fixture-1/retry`]:
    {
      enrollment_id: "enroll-fixture-1",
      status: "connected",
      required_services: 1,
      presentation_url: "",
    },
  [`POST /v1/sessions/${sessionID}/workspace-enrollment/enroll-fixture-1/cancel`]:
    {
      enrollment_id: "enroll-fixture-1",
      status: "cancelled",
      required_services: 1,
      presentation_url: "",
    },
  // Learning review queue: the per-proposal read the Details disclosure
  // performs, the approve/reject decision and the undo (each answers the
  // proposal at its next version), and the Reflect card's explicit pass.
  "GET /v1/learning/proposals/prop-1": { proposal: learningProposals[0] },
  "GET /v1/learning/proposals/prop-2": { proposal: learningProposals[1] },
  "POST /v1/learning/proposals/prop-1/decision": {
    proposal: {
      ...learningProposals[0],
      version: "3",
      status: "promoted",
      promotion: {
        memory_key: "scheduler/flaky-test",
        previous_exists: false,
        previous_version: "",
        result_version: "1",
      },
    },
  },
  "POST /v1/learning/proposals/prop-2/decision": {
    proposal: {
      ...learningProposals[1],
      version: "2",
      status: "skill_materialized",
      learned_skill_id: "learned-fixture-1",
    },
  },
  "POST /v1/learning/proposals/prop-1/undo": {
    proposal: { ...learningProposals[0], version: "4", status: "undone" },
  },
  "POST /v1/learning/proposals/prop-2/undo": {
    proposal: { ...learningProposals[1], version: "3", status: "undone" },
  },
  [`POST /v1/sessions/${sessionID}/reflect`]: {
    receipt: {
      reflection_id: "reflection-fixture-1",
      disposition: "completed",
      queued: 0,
      abstained: false,
      staged: 1,
      promoted: 0,
      conflicted: 0,
    },
  },
  "POST /v1/schedules": {},
  "PUT /v1/schedules/nightly-fixture-digest": {},
  "POST /v1/schedules/nightly-fixture-digest/pause": {},
  "POST /v1/schedules/nightly-fixture-digest/resume": {},
  "POST /v1/schedules/nightly-fixture-digest/fire": {},
  "DELETE /v1/schedules/nightly-fixture-digest": {},
};

// The prompt relay: proto-JSON events, every frame stamped with the run id
// the SDK binds the Run to, ending on the terminal `result` frame.
const promptFrames = [
  { type: "turn.start", run_id: runID, seq: "1", turn: 1 },
  {
    type: "message.delta",
    run_id: runID,
    seq: "2",
    turn: 1,
    text: "Streaming ",
  },
  {
    type: "message.delta",
    run_id: runID,
    seq: "3",
    turn: 1,
    text: "from the fixture.",
  },
  // One foreground subagent's lifecycle inside the turn (proto `Subagent`,
  // snake_case protojson; int64 duration_ms as a string): start → one tool
  // → end. It feeds the inline delegation card, the fleet chip beside the
  // context meter, and the Agents panel.
  {
    type: "subagent.start",
    run_id: runID,
    seq: "4",
    turn: 1,
    subagent: {
      parent_call_id: "call-fixture-1",
      child_id: "subagent-fixture-1",
      goal: "Scan the scheduler tests",
      background: false,
    },
  },
  {
    type: "subagent.tool",
    run_id: runID,
    seq: "5",
    turn: 1,
    subagent: {
      parent_call_id: "call-fixture-1",
      child_id: "subagent-fixture-1",
      tool_name: "Grep",
      tool_count: 1,
      inner_kind: "tool.call",
    },
  },
  {
    type: "subagent.end",
    run_id: runID,
    seq: "6",
    turn: 1,
    subagent: {
      parent_call_id: "call-fixture-1",
      child_id: "subagent-fixture-1",
      stop: "end_turn",
      tool_count: 1,
      duration_ms: "850",
      usage: { input_tokens: 4, output_tokens: 2 },
    },
  },
  // The per-turn stat frame (tokens + elapsed model-call time).
  {
    type: "turn.end",
    run_id: runID,
    seq: "7",
    turn: 1,
    turn_end: {
      duration_ms: "4100",
      usage: { input_tokens: 10, output_tokens: 5, cache_read_tokens: 4 },
    },
  },
  {
    type: "result",
    run_id: runID,
    seq: "8",
    turn: 1,
    text: "Streaming from the fixture.",
    result: {
      stop: "end_turn",
      text: "Streaming from the fixture.",
      usage: { input_tokens: 10, output_tokens: 5 },
    },
  },
];

// A prompt parked on a browser sign-in: the daemon emits authorization.required
// and closes the stream WITHOUT a result. Nothing here is a truncation.
const authorizationPromptFrames = [
  { type: "turn.start", run_id: runID, seq: "1", turn: 1 },
  {
    type: "message.delta",
    run_id: runID,
    seq: "2",
    turn: 1,
    text: "Signing in to Fixture MCP… ",
  },
  {
    type: "authorization.required",
    run_id: runID,
    seq: "3",
    turn: 1,
    authorization: {
      authorization_id: authorizationID,
      call_id: "call-fixture-1",
      status: "pending",
      display_name: "Fixture MCP",
      expires_at: new Date(Date.now() + 10 * 60_000).toISOString(),
    },
  },
];

// The recheck/cancel control stream: the terminal status, then the
// continuation run through its result (the parked call resumes on granted;
// on cancelled it records the cancellation and the model carries on).
function authorizationControlFrames(status) {
  const text =
    status === "granted"
      ? "Authorized: continuing from the fixture."
      : "Sign-in cancelled: continuing without the tool.";
  return [
    {
      type: "authorization.resolved",
      run_id: continuationRunID,
      seq: "1",
      authorization: {
        authorization_id: authorizationID,
        call_id: "call-fixture-1",
        status,
        display_name: "Fixture MCP",
      },
    },
    { type: "message.delta", run_id: continuationRunID, seq: "2", text },
    {
      type: "result",
      run_id: continuationRunID,
      seq: "3",
      text,
      result: {
        stop: "end_turn",
        text,
        usage: { input_tokens: 4, output_tokens: 6 },
      },
    },
  ];
}

const promptAsksForAuthorization = (raw) => {
  try {
    return /authoriz/i.test(String(JSON.parse(raw).text ?? ""));
  } catch {
    return false;
  }
};

function readBody(request) {
  return new Promise((resolve) => {
    const chunks = [];
    request.on("data", (chunk) => chunks.push(chunk));
    request.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
  });
}

function writeProblem(response, status, code, detail) {
  response.writeHead(status, { "Content-Type": "application/problem+json" });
  response.end(
    JSON.stringify({
      type: `https://mecatl.stacklok.com/problems/${code}`,
      title: detail,
      status,
      code,
      detail,
    }),
  );
}

// The key-scoped user-model read (`GET /v1/usermodel?key=prefers-tabs`)
// adds `detail` — the fact's value plus its bounded revision history — with
// the daemon's snake_case keys and `{seconds}` timestamps
// (GetUserModelResponse.detail is populated only on an exact key match).
const userModelDetail = {
  current: {
    key: "prefers-tabs",
    value: "Tabs, width 4",
    description: "The operator prefers tabs over spaces.",
    version: "3",
    status: "active",
    writer: "agent",
    origin: "reflection",
    source_session_id: "session-fixture-1",
    updated_at: { seconds: 1_755_000_000 },
  },
  history: [
    {
      version: "2",
      status: "superseded",
      updated_at: { seconds: 1_754_000_000 },
    },
  ],
  history_available: true,
};

function writeSSE(response, frame) {
  response.write(`data: ${JSON.stringify(frame)}\n\n`);
}

http
  .createServer((request, response) => {
    // Studio's proxy percent-encodes each path segment (server-proxy.ts), so a
    // custom-method route such as `cleanup:plan` arrives as `cleanup%3Aplan`.
    // The daemon's Go ServeMux unescapes per SEGMENT before matching its
    // literal pattern; mirror that here (never the whole path — an encoded
    // slash inside a segment must stay part of that segment, as it does live).
    const path = new URL(request.url, "http://fixture").pathname
      .split("/")
      .map(decodeURIComponent)
      .join("/");
    const key = `${request.method} ${path}`;
    // Retry re-drives the recorded failed step and streams exactly like a
    // prompt (ADR 0239), so both share the relay.
    if (
      key === `POST /v1/sessions/${sessionID}/prompt` ||
      key === `POST /v1/sessions/${sessionID}/retry`
    ) {
      void readBody(request).then((raw) => {
        const frames =
          key.endsWith("/prompt") && promptAsksForAuthorization(raw)
            ? authorizationPromptFrames
            : promptFrames;
        response.writeHead(200, { "Content-Type": "text/event-stream" });
        for (const frame of frames) writeSSE(response, frame);
        response.end();
      });
      return;
    }
    const controlPrefix = `POST /v1/sessions/${sessionID}/mcp-authorizations/${authorizationID}/`;
    if (key === `${controlPrefix}recheck` || key === `${controlPrefix}cancel`) {
      // Correlation-only, like the daemon's controlRequestBodyEmpty: ONE body
      // byte is a 400, so a client that posts `{}` fails here as it would live.
      void readBody(request).then((raw) => {
        if (raw.length > 0) {
          writeProblem(
            response,
            400,
            "invalid_argument",
            "MCP authorization controls do not accept a request body",
          );
          return;
        }
        response.writeHead(200, { "Content-Type": "text/event-stream" });
        const status = key.endsWith("/cancel") ? "cancelled" : "granted";
        for (const frame of authorizationControlFrames(status)) {
          writeSSE(response, frame);
        }
        response.end();
      });
      return;
    }
    if (key === `GET /v1/sessions/${sessionID}/watch`) {
      // A durable watch never ends on its own: send the replay→live boundary
      // and hold the connection open until the client detaches.
      response.writeHead(200, { "Content-Type": "text/event-stream" });
      writeSSE(response, { cursor: "c2RrY3VyLzEtZml4dHVyZQ", phase: "live" });
      const keepalive = setInterval(() => response.write(": ping\n\n"), 15_000);
      request.on("close", () => clearInterval(keepalive));
      return;
    }
    if (key === "GET /v1/learning/proposals") {
      // The queue's status pills filter server-side (`?status=`); no page
      // cursor is ever handed out — two proposals fit one page.
      const status =
        new URL(request.url, "http://fixture").searchParams.get("status") ?? "";
      response.writeHead(200, { "Content-Type": "application/json" });
      response.end(
        JSON.stringify({
          proposals: learningProposals.filter(
            (proposal) => status === "" || proposal.status === status,
          ),
          next_cursor: "",
        }),
      );
      return;
    }
    if (key === "GET /v1/usermodel") {
      // The index read ignores the query; an exact `?key=` match adds `detail`.
      const wanted = new URL(request.url, "http://fixture").searchParams.get(
        "key",
      );
      const index = routes[key];
      response.writeHead(200, { "Content-Type": "application/json" });
      response.end(
        JSON.stringify(
          wanted === "prefers-tabs"
            ? { ...index, detail: userModelDetail }
            : index,
        ),
      );
      return;
    }
    const body = routes[key];
    response.writeHead(body ? 200 : 404, {
      "Content-Type": body ? "application/json" : "application/problem+json",
    });
    response.end(
      JSON.stringify(
        body ?? {
          type: "https://mecatl.stacklok.com/problems/not_found",
          title: "Not found",
          status: 404,
          code: "not_found",
          detail: `fixture has no ${key}`,
        },
      ),
    );
  })
  .listen(port, "127.0.0.1", () => {
    console.log(`fixture daemon on 127.0.0.1:${port}`);
  });
