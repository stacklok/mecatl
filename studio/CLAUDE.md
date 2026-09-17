# CLAUDE.md — Mecatl Studio

The web client for the mecatl harness: a Next.js app (App Router) serving the
Atrium workspace — Chats · Scheduled · Skills · Memory · Settings — against a
`mecated` daemon. See ADR 0347 (module + posture) and ADR 0348 (server-backed
chats).

## Commands

Run through the root Taskfile, not bare npm:

```sh
task build          # repo root first — studio's managed mode spawns ../bin/mecated against the configured root (default: the repo; the controller's `POST /workspace` — no settings page since Sept 2026)
task studio:dev     # start Studio + its mecated supervisor (background; logs in studio/dev.log)
task studio:stop    # stop web server + controller + the mecated it supervises
task studio:test    # vitest run + the hermetic server-tier suite (builds first)
task studio:lint    # biome
task studio:typecheck
```

`npm run dev` = managed mode via `scripts/dev-local.mjs` (controller on :8788,
web on :3000). `npm run dev:web` = bare `next dev` (external mode or against an
already-running controller). Setting `MECATL_BASE_URL` selects external mode.
The hermetic suite (`npm run test:server`) builds Next first — a stale build is
the usual reason it fails mysteriously.

**Non-technical audience (September 2026).** Studio is written for office
users. Removed from the UI, with the underlying controller documents and
defaults kept: the first-run welcome card, mascot and capability hints; the
sidebar's Chats / Runs / Scheduled / Drafts tabs (chats only list now; runs
and fires stay reachable through the read-only transcript dialog); the chat
header's placement badge and status strip (facts moved to ⋯ → Session
details); the top-nav connection and posture chips; the composer's Memory
pill; and the Settings pages Status line, Tools, Persona, Keyboard
(rebinding), Model router and Workspace, plus the Preferences-file card, the
Custom-palettes card, the performance/pprof card, the posture card, the
store-location and retention cards and the configuration reference. The
status-line templates still render their DEFAULTS (`src/lib/statusline`).
Every settings card uses plain language ("the agent", one restart sentence:
"Changes restart the agent. Anything running will stop."); the Mode menu
carries the "Safety level" (operator posture) and Tools is its own pill.

Studio's own build stamp (Settings → Provider → About's `Studio` row, the
`/diagnostics` report's `client build:` line; `src/lib/studio-build.ts`) is
inlined at `next build` by `next.config.ts` (`NEXT_PUBLIC_STUDIO_BUILD` =
`MECATL_STUDIO_BUILD` when the build environment sets it, else
`<package.json version>+<short git sha>`, else `+dev` without a `.git`).
`next start` reports the stamp of the build it serves, not the running
checkout — a bug report wants the build.

Settings → About (`settings/help/page.tsx`) is the web `--version`: Studio's
plain version (inlined as `NEXT_PUBLIC_STUDIO_VERSION`,
`src/lib/studio-version.ts`), the docs and support links, and a short
"About the agent" card. The configuration reference is no longer rendered,
but `src/lib/studio-config-reference.ts` stays the DATA (every env var the
server tier, the controller or the build reads; its test scans the source
for `process.env.X` reads, so a new knob must get a row), and
`GET /api/studio/about` (same-origin, `requestIsTrusted`) answers which
names are set as booleans — never a value. The ⌘K search lists the
help pages (`src/lib/workspace-pages.ts`) under a Pages group.

## Module shape

- `@stacklok-oss/mecatl-sdk` (source `../sdk/typescript`, a `file:` dependency
  — `task studio:install` and CI build it before `npm ci`) — the ONLY reader
  of the daemon wire. Studio consumes the daemon exclusively through the SDK's
  HTTP transport pointed at the same-origin `/api/mecatl` proxy; the SDK owns
  generated proto bindings, SSE run/watch decoding, and unknown-event forward
  compatibility. `src/lib/harness/` adapts SDK values to Studio's view models
  (`sdk.ts` builds the client). Controller calls are NOT in the SDK.
- `src/lib/server-proxy.ts` + `src/app/api/mecatl{,-control}/[...path]` — the
  server tier: origin trust, header allowlists, bearer injection (auth ONLY —
  daemon bodies and queries are forwarded verbatim), external-mode 409 policy.
- `scripts/local-controller.mjs` — managed-mode sidecar (supervises `mecated`,
  owns model-router/MCP-gateway config + OAuth, the permissions document
  — operator posture / project trust / shell-less mode — as mecated spawn
  flags via `GET|POST /permissions`, the session-store location via
  `POST /storage`, the retention policy via `POST /retention`, and the
  daemon defaults — default/subagent model per provider, reasoning effort,
  context window, prompt caching, base-URL overrides, ToolHive LLM gateway,
  aliases/slots, credentials-file path, plus the durable active-provider
  choice — as spawn flags via `GET|PUT /daemon-defaults`, and the runtime
  settings — learning mode/sensitivity as a second CLI-tier
  `--permission-config` file carrying the FULL merged `learning:` block,
  the steer opt-out and the soul flags (`--no-steer`, `--no-soul`,
  `--soul-strict`, `--soul-file`, the one-shot `--approve-soul`) as spawn
  flags — via `GET|PUT /runtime-settings` + `POST /soul/approve`); policy
  helpers in `src/lib/controller-security.mjs`, the flag grammars in
  `src/lib/controller-permissions.mjs`, `src/lib/daemon-defaults.mjs` and
  `src/lib/runtime-settings.mjs`; the controller's OWN project-trust
  registry probes — authority + identity anchor mirroring the daemon's
  `workspacetrust` — in the node-only `src/lib/controller-trust.mjs`
  (`/status.trust`, `POST /permissions/trust`, `POST /permissions/trust-once`).
- `src/features/agent/` — runtime-status provider + the daemon-backed hooks;
  `src/app/workspace/**` — the five surfaces.

## Rules that have teeth

Each rule is backed by a test; break the rule and its test names you.

1. **Daemon-only: an unreachable daemon renders offline, never demo data.**
   There are no fixtures to fall back to — do not add any. The one sanctioned
   exception is the explicit, default-off, clearly-labeled Labs mock content
   (Settings → Labs → "Show demo chat", `src/features/agent/mock-tour.ts`)
   — an opt-in demo the user turns on, never a fallback for an unreachable
   daemon. Its sibling is Labs → "Developer tools" (mecatui's client debug
   mode): the `/debug-ask` built-in / "Inject fake approval" menu item park
   a clearly-labeled SYNTHETIC permission ask (`src/features/agent/debug-ask.ts`,
   `ApprovalRequest.synthetic`) that never reaches the daemon — its verdict
   is short-circuited, and a genuine ask displaces it — and the queue strip
   shows the steer correlation trace (`src/features/agent/steer-trace.ts`).
   (`tests/rendered-html.test.mjs`: unreachable daemon → friendly 503;
   `use-agent-chat.debug-ask.test.ts`: no request on a synthetic verdict.)
2. **Session placement is server-owned; Studio never sends a workspace.**
   The daemon binds every session to its deployment's environment (ADR 0291)
   and decodes `POST /v1/sessions` with unknown fields disallowed, so the
   proxy forwards create bodies verbatim — no `workspace` injection into
   session/team/schedule creation, no `?workspace=` on `/v1/commands` (it is
   keyed by `session_id`). `MECATL_WORKSPACE` (external) and the controller's
   `/status` `workspace` are display-only labels in the browser: the latter
   is the spawn root the MANAGED controller owns (the TUI's `--workspace`),
   changeable ONLY through the controller's header-gated `POST /workspace`
   (the Settings → Workspace page was removed; a path is operator
   configuration) — validated on the controller's OWN filesystem
   (`src/lib/workspace-config.mjs`: absolute, exists, a directory, not the
   state dir), persisted in `studio-workspace.json`, a restart; a non-default
   root gets its OWN session store and memory dir keyed by a path hash UNDER
   the default root's `.scratch` (chats belong to their root and reappear
   when you switch back; a foreign repo gains only `.mecatl/skills`), and a
   change withdraws the remembered/once project-trust grants. `/status` also
   carries `defaultWorkspace`; the About card shows a `Workspace` row in
   both modes. It is never sent to the daemon as a placement.
   (`workspace-config.test.ts`, `client-status.test.ts`,
   `about-daemon-card.test.tsx`, hermetic 409 + header-gate rows.) The one placement CHOICE
   Studio offers — "Switch worktree…" in the chat menu
   (`worktree-picker-dialog.tsx`) — travels as the daemon's OPAQUE
   `worktree_selector` from `GET /v1/worktrees?session_id=`, on ClearSession
   ("Start fresh there") or ForkSession ("Bring this conversation"), never a
   path; a stale selector (412 `placement_selector_*`) relists instead of
   erroring. Gated on `serverCapabilities.worktrees` AND the row's `fork`
   verdict; never offered on the draft, the mock tour or an AI-debug chat.
   (hermetic: session creation is forwarded verbatim; `worktrees.test.ts`,
   `create-body.test.ts`, `worktree-picker-dialog.test.tsx`, the Playwright
   worktree test.) The chat header SHOWS the placement the daemon bound —
   the snapshot's `placement` display metadata (label · branch, kind and
   short revision on hover; "No filesystem" for a no-fs session) as
   `placement-badge.tsx`, the TUI's startup placement line; never a path,
   never a selector, hidden when an older daemon omits it
   (`placement-badge.test.tsx`, `client-actions.test.ts`).
3. **Credentials never cross the browser/controller boundary.** No key-paste
   UI anywhere; `mecated` reads `~/.config/mecatl/auth.yaml`. The proxy's
   header allowlist excludes `authorization` from the browser.
   (hermetic: bearer injected server-side.)
4. **Controller mutations are server-only.** They require the server-set
   `x-mecatl-studio-request` header, a loopback Host, and an allowlisted
   Origin. (hermetic: CSRF/DNS-rebinding truth table.)
5. **External mode owns nothing locally.** Every control write answers 409.
   (hermetic: control writes 409.)
6. **A failed turn renders as failed.** `result.stop === "error"` with no text
   must never become a quiet success — the SDK's terminal `result` event
   always reaches the UI.
7. **Unknown event kinds are surfaced, never dropped.** The SDK decodes a new
   daemon event kind as a typed unknown; Studio shows it as "not rendered
   yet".
8. **The memory panel is read-only.** A value typed into the UI would land in
   turn-0 context bypassing injection scanning; the daemon has no write API by
   design. Do not add an editor.
9. **Session rows obey the store.** Eligibility comes from row capabilities
   (omitted = denied); a row is removed only by a complete inventory walk or a
   404; renames adopt the daemon's clamped echo.
   (`src/lib/protocol/sessions.test.ts` + `use-agent-sessions`.)
10. **Schedule PUT replaces the whole spec.** Fields the form cannot edit ride
    the row's `carried` spec and are re-encoded, or they are silently deleted.
    (`src/lib/protocol/schedules.test.ts`: carried round-trip.)
11. **Requests are protojson; responses are stdlib JSON.** Never echo a decoded
    response back as a request body. (`schedules.test.ts`: the asymmetry test.)
12. **Skills are project-scoped by default, and the controller owns the
    directory.** The controller passes ONE `--skills-dir` — the workspace's
    `.mecatl/skills` unless the daemon-options document relocates it (rule
    24: a trust decision, confined to the workspace / default root / mecatl
    config dir) — and never `--skills-conventional`. With the Skill tool
    switched off it passes NO `--skills-dir` (that is how mecated drops the
    tool); the Skills page still edits and parks skills in the owned
    directory, and shows the daemon's `skills: false` as a banner.
13. **Posture, project trust and shell-less mode are spawn FLAGS, never a
    settings.yaml key.** Settings → Permissions saves a Studio-owned
    `permissions.json`; the controller turns it into `--posture <tier>`
    (ALWAYS passed, strict included, so Studio's tier out-ranks an imported
    operator settings file's `posture:` in both directions),
    `--trust-project` and `--no-shell`, and never `--headless` (mecated
    raises the trust floor for trusted+ on interactive roots — the page's
    "implied by the posture" claim depends on it). The daemon-wide posture is
    NOT the composer's per-session Mode (the Mode menu's "Safety level"
    rows write the SAME permissions document — one writer, two entry
    points); the EFFECTIVE tier is
    `serverCapabilities.posture` (capability-gated: absent on an older
    daemon). External mode: `permissions: null`, every `/permissions` verb
    409. The four tiers are the SDK's `ServerPosture` ladder and nothing
    else — every Studio copy (`POSTURES`, `POSTURE_TIERS`, the picker's
    `POSTURE_OPTIONS`) is pinned to it, in order.
    (`src/lib/controller-permissions.test.ts`,
    `permissions-section.test.tsx`, `posture-vocabulary.test.ts`, hermetic
    409 + CSRF rows, the Playwright external-mode Permissions test.) Project
    trust itself is the controller's OWN registry, never the daemon's:
    mecated never prompts, and Studio cannot reach the Go composition layer
    mecatui's pre-TUI prompt uses, so `src/lib/controller-trust.mjs`
    mirrors the daemon's two probes in JavaScript — `hasProjectAuthority`
    (authority.go: soul / any file under the six anchor dirs / a non-empty
    `permissions.allow` in `.mecatl/settings{,.local}.yaml` or
    `.claude/settings{,.local}.json`) and `trustAnchor` (anchor.go's
    transcript byte-for-byte; permission rules deliberately NOT folded) —
    re-read ONCE PER SPAWN in `startMecatl`, never per `/status` poll. A
    saved grant carries the anchor it was granted at; a spawn whose live
    anchor differs is DRIFTED and gets NO `--trust-project` (the daemon's
    own fail-safe arm) until the bodyless `POST /permissions/trust`
    re-accepts it; `POST /permissions/trust-once` is mecatui's "trust once"
    — in-memory, dies with the controller, rides every restart in between.
    Both grants are studio-header-gated and 409 in external mode, and reach
    the UI only through `useHarnessRuntime().trustProject` /
    `.trustProjectOnce` (busy label `trust`), which re-read `/status.trust`
    so the page shows the decision the NEW spawn got. The registry never
    reads or writes `trust.yaml` / `trustedWorkspaces:` — the daemon may
    trust a workspace Studio reads as untrusted, and the UI says so.
    The PROMPT itself is mecatui's pre-TUI "trust / trust once / no" as a
    workspace banner (`workspace-trust-banner.tsx`, mounted in the layout's
    banner band): it renders only when the controller reports admittable
    authority AND the spawn got no grant (`untrusted`, or a `drifted`
    remembered grant), never in external mode, never once the daemon's own
    `GET /v1/soul` reports a TRUSTED project soul (the one wire signal that
    the daemon admitted the project from a registry Studio cannot see), and
    not after "Not now" — which stores the LIVE anchor under
    `localStorage["mecatl.trust.dismissed:<workspace>"]`, so a changed
    authority set (a new anchor) re-prompts. Both grants confirm first
    (the daemon restarts; in-flight runs end) and the once-copy says the
    grant lasts until the CONTROLLER restarts. Settings → Permissions shows
    the decision as a "Project trust" row with "Forget trust" (the switch
    off) and, on drift, "Trust again" (the explicit grant — a plain save
    with the switch already on never re-stamps the anchor).
    (`src/lib/controller-trust.test.ts`, `use-harness-runtime.test.ts`,
    `harness/trust.test.ts`, `workspace-trust-banner.test.tsx`,
    `permissions-section.test.tsx`, hermetic 409 + CSRF rows for both grant
    routes and the external `trust: null`.)
14. **The session store is a spawn FLAG too, never a settings.yaml key, and
    in-memory means NO flag.** Settings → Storage saves a Studio-owned
    `storage-settings.json` (seeded from `MECATL_STUDIO_STORE_DIR` /
    `MECATL_STUDIO_NO_STORE=1`); the controller turns a durable store into
    `--store-dir <absolute>` right after `--workspace` (relative paths resolve
    under the controller's workspace, the dir is created first) and an
    in-memory store into the ABSENCE of the flag (mecated has no `--no-store`).
    `/status.storage` reports the saved AND default location; external mode:
    `storage: null`, `POST /storage` 409. There is NO settings UI for the
    location any more (operator configuration; the route and document stay).
    (`src/lib/storage-settings.test.ts`, `store-location.test.ts`, hermetic
    409 + CSRF rows.)
15. **Retention is spawn FLAGS too, and main-chat deletion needs the
    acknowledgement — in Studio AND in mecated.** The controller's
    `POST /retention` saves a Studio-owned `retention.json` (no settings UI
    since Sept 2026 — the write side stays for operators); the controller turns
    each SET field into its mecated flag (`--main-retention`,
    `--child-retention`, `--schedule-fire-retention`, their count caps,
    `--child-gc-interval` for the sweep cadence, `--acknowledge-main-retention`)
    and a null field into NO flag (mecated's default stands). Durations are
    Go grammar — no `d`. A non-zero main age/count without
    `acknowledgeMainDeletion: true` is refused by the controller (400) before
    the restart mecated would fail; child/scheduled limits need no
    acknowledgement. No flags at all while an imported operator settings
    file is active (`managedBy: "operator-settings"`, `POST /retention` 400).
    The EFFECTIVE policy the card shows is the daemon's own
    `GET /v1/storage/health` (`policy`, `last/next_sweep_unix`), never the
    saved document. External mode: `retention: null`, `POST /retention` 409.
    (`src/lib/retention-settings.test.ts`, `harness/retention.test.ts`,
    `harness/storage.test.ts`, hermetic 409 + CSRF rows.)
16. **Daemon defaults are spawn FLAGS, keyed per provider, and the active
    provider is durable.** Settings → Model provider → "Daemon defaults" (and
    the provider page's "Make daemon default" kebab, the Add dialog's "Save
    override") save a Studio-owned `daemon-defaults.json`; the controller
    turns it into `--default-model`/`--subagent-model` (the pair saved for
    the SPAWN kind only — mecated validates them against the current default
    provider fail-fast, so a stale pair must never ride a provider switch;
    never for `--mock`), `--reasoning-effort`, `--context-window-override`,
    the two LLM stream bounds `--llm-per-attempt-timeout` /
    `--llm-stream-idle-timeout` (Go durations, `"600s"`; emitted ONLY when
    they differ from mecated's own 300 s / 180 s — 0 is a real "disabled",
    never "unset"; `LLM_TIMEOUT_DEFAULTS` in `daemon-defaults.mjs`),
    `--no-prompt-cache`, `--anthropic-cache-ttl`, `--<kind>-base-url`,
    `--toolhive-llm=false`/`--toolhive-llm-base-url`/`--toolhive-llm-mode`,
    repeated `--model-alias`/`--model-slot` (the `router` slot is reserved
    for model routing, whose settings page was removed), and `--api-key-file` (a `.yaml` INSIDE the mecatl
    config dir — the controller reads and rewrites that file for the
    provider routes, so the path is confined). A refused start rolls the
    previous document back and returns mecated's own refusal. `POST
    /providers/active` persists the choice (`MECATL_STUDIO_PROVIDER` still
    wins at boot; a saved kind that is no longer selectable is dropped, and
    `DELETE /providers/{name}?scope=all` clears it); "Switch to offline
    mock" is the explicit `--mock`. `GET /daemon-defaults` is header-free
    read-only like `/status`; the PUT is not. External mode:
    `daemonDefaults: null`, both verbs 409. (`src/lib/daemon-defaults.test.ts`,
    `controller-security.test.ts`, `harness/daemon-defaults.test.ts`,
    `use-daemon-defaults.test.ts`, `daemon-defaults-card.test.tsx`,
    `provider-section.test.tsx`, hermetic 409 rows.) Provider REMOVAL has
    the TUI's two scopes, from each row's kebab: `?scope=credential`
    (`providers logout`, "Remove key (keep provider)") cuts ONLY the
    `api_key` line from the provider's auth.yaml block — an emptied entry
    becomes `name: {}` exactly as mecated's authfile writes it, and the
    block plus a custom provider's definition stay, so it lists as
    "configured, no key"; `?scope=all` (`providers remove`, the default)
    cuts the whole auth.yaml block AND a custom provider's settings.yaml
    definition, its saved model pair and the active choice. A custom
    provider's NON-secret DEFINITION (`providers add`: id, `api_flavor`,
    HTTPS `base_url`, `default_model`, `auth.method` `api_key`|`none`) is
    the one thing Studio WRITES into the user-global settings.yaml: `POST
    /providers/custom` (Add provider → Custom gateway → "Save definition";
    a keyless provider restarts at once, an `api_key` one waits for the
    hand-pasted key — rule 3 holds: there is no key field and none is
    accepted). Both the write and `?scope=all` on a name the imported
    operator settings file defines are refused 409 BEFORE anything is
    written (Studio never edits that file) and the UI withholds the buttons
    while it is active. Every edit is a conservative line-range rewrite
    (`provider-auth.mjs`), temp-file + rename, the file's existing mode
    preserved (0600 when created). (`provider-auth.test.ts`,
    `add-provider-dialog.test.tsx`, `provider-section.test.tsx`,
    `use-provider-management.test.ts`, hermetic 409 + header-gate rows.)
17. **Diagnostics options are spawn FLAGS with ONE writer per flag, and the
    posture is not one of them.** Settings → Diagnostics reads the
    daemon-REPORTED `serverCapabilities.posture` and spells out the four
    defenses that tier switches on (`src/lib/posture.ts` mirrors mecatui's
    `/posture` sentence byte-for-byte); changing the tier stays the
    Permissions page's job (`--posture` has exactly one writer). The
    controller's DIAGNOSTICS document (`diagnostics-options.json`, `GET|POST
    /diagnostics-options`, a PARTIAL patch body, both verbs behind the studio
    header) becomes `--log-level`, an EXPLICIT `--metrics-addr` in both
    directions (`--metrics-addr=` while the admin listener is off — mecated's
    default is a fixed loopback port two managed daemons would fight over),
    `--perf-mcp` only with a real listener address, `--goroutine-warn-
    threshold`, `--product-metrics=false`/`--product-metrics-dry-run` (never
    `=true`: the controller's env opt-out — `DO_NOT_TRACK`,
    `MECATL_PRODUCT_METRICS` — stays the operator's and is reported as
    `productMetrics.source: "environment"`; `product-metrics-card.tsx` shows
    the effective verdict as "Not disabled"/"Off" — never a definitive On,
    because `telemetry.productMetrics.enabled` in settings.yaml is a third
    opt-out Studio cannot read — and disables both switches under an env
    opt-out); `quiet` only gates the
    controller's stderr mirror and restarts nothing. The controller is
    also the daemon's LOG FILE (mecatui's embedded-server log, ported):
    mecated has no log-file flag, so the process holding its stderr appends
    it to `studio/.scratch/mecated.log` (0600, ONE rotated generation
    `mecated.log.1` at mecatui's 10 MiB bound, `src/lib/controller-log.mjs`)
    and a bounded in-memory ring; `GET /logs?lines=` serves the tail + file
    facts + `startupError`, `GET /logs/download` streams the file (both
    studio-header-gated, 409 in external mode). The content is
    model-influenced, so `daemon-log-card.tsx` renders it as plain text
    only. Names are deliberately
    distinct from the (separate) daemon-options document:
    `diagnosticsOptions`, `diagnosticsOptionArgs`. External mode: both verbs
    409. (`src/lib/controller-diagnostics-options.test.ts`,
    `src/lib/posture.test.ts`, `harness/diagnostics.test.ts`,
    `use-diagnostics-options.test.ts`, `product-metrics-card.test.tsx`,
    hermetic 409 rows, the Playwright diagnostics test.) Settings →
    Diagnostics (under Support) now shows only the log tail / download, the
    metrics opt-out and Restart; the posture card (see Permissions) and the
    performance card were removed. The admin surface itself
    reaches the browser ONLY through the controller: it probes a free
    loopback port per spawn (the ready file names only `http_address`; one
    re-probe + re-spawn when the failed start says address-in-use —
    `src/lib/controller-perf.mjs`), `GET /perf` reports the LIVE origin,
    paths and knobs (`/status.perf` mirrors it), and `GET /perf/metrics` /
    `GET /perf/vars` relay the two TEXT endpoints (409 while off, 503 while
    no child runs; `/debug/pprof` and `/debug/flightrecorder` are NEVER
    relayed). All three are studio-header-gated and 409 in external mode.
    No UI renders them any more (the performance card and
    `prometheus-text.ts` were removed); the controller routes stay for
    operators. Passing `--metrics-addr=` when the switch is off means a
    managed daemon no longer opens mecated's default 127.0.0.1:9090 listener
    — that is deliberate. (`controller-perf.test.ts`, hermetic 409 +
    header-gate rows.)
18. **Storage maintenance is DAEMON-owned, capability-gated, and the
    destructive step is typed-confirmed.** Settings → Storage is ONE card
    that talks to mecated ONLY through the SDK's `client.storage` namespace
    (`src/lib/harness/storage.ts` — no controller document, no spawn flag,
    nothing to roll back): a plain health summary (`GET /v1/storage/health`,
    gated on `serverCapabilities.storage_health`) and "Clean up old runs"
    (`/v1/storage/cleanup:plan` → `cleanup:apply` →
    `/v1/storage/cleanup/jobs/*`, gated on `storage_cleanup`); the Optimize
    storage (migration) UI was removed — its fetchers stay in `storage.ts`
    for operators. An ungated block renders nothing. A running job is polled every
    `JOB_POLL_INTERVAL_MS` (2 s) by `useStorageMaintenance`, stopped on a
    terminal state and on unmount, and health is re-read when it settles.
    Clean-up ticks every kind BUT `main` by default, applies with the PLAN's
    `confirmationToken` (never a client-minted one) behind `useTypedConfirm`,
    whose confirm enables only when the field equals `CLEAN UP` exactly, and
    a finished job with `deleted > 0` fires `notifySessionsChanged` so the
    sidebar re-walks now. Stable codes are framed in ONE place
    (`storage-maintenance-shared.tsx`): `cleanup_plan_stale` → "plan again"
    with a Re-plan button, `migration_conflict` → the job already running →
    Refresh health, `management_unauthorized` → read-only copy in either
    card; an unknown code shows the daemon's own message.
    (`harness/storage.test.ts`, `use-storage-maintenance.test.ts`,
    `use-typed-confirm.test.tsx`, the three `storage-*-card.test.tsx`,
    `sessions-changed.test.ts`, the Playwright storage test.)
19. **The session facts live in the Session details dialog, and never show
    an address.** ⋯ → Session details (`session-details-dialog.tsx`) carries
    the session id, resolved model, permission mode, safety level (the
    daemon-reported posture) and server (managed by Studio / external
    deployment, never a host). `chat-status-strip.tsx` renders ONLY for an
    AI-debug session (the amber DEBUG target + privacy line); an ordinary
    chat has no strip, so the conversation keeps its height. The strip's
    rules below still hold where it renders. The handle is the documented BARE
    12-column literal (`src/lib/protocol/session-handle.ts` mirrors
    `client.SessionHandle` byte for byte; no `#`); a click copies the full
    id. The model is the snapshot's RESOLVED model (display name from the
    picker list, `/route` from `provider.route` — translated to
    `provider_route`, no longer silent — and the effective effort);
    "resolving model…" shows only while the detail read is pending on a
    live chat or the connection is connecting, and settles to the
    configured id or "model unavailable" (`use-agent-chat`'s
    `sessionDetailStatus`). A mode change made mid-run is HELD by
    `useSessionMode({ busy })` as `pendingMode` — the strip reads
    "(pending)", the pill stays enabled (`modeSwitchDeferred`) — and lands
    once the run ends; a run parked on an approval is still busy. The
    server segment is managed/external + deployment label only: the
    same-origin proxy hides the daemon URL (rule 3), so no host is ever
    rendered. On an AI-debug chat the strip turns amber with `DEBUG target
    <handle>` and the TUI's durable PRIVACY line (+ bound reporting
    servers), and chat-workspace withholds mode/model/effort/compact so no
    control can fork or rebind the binding. (`session-handle.test.ts`,
    `chat-status-strip.test.tsx`, `use-session-mode.test.ts`,
    `events.test.ts` provider.route, the Playwright status-strip test.)
20. **Workspace-services enrollment never keeps the consent URL, and opens
    its window on the click.** Where the daemon advertises
    `workspace_enrollment`, the TUI's "workspace services not connected"
    notice (`workspace-enrollment-notice.tsx`) sits above the live chat's
    composer with /tools-connect and /tools-cancel as buttons. Connect calls
    `window.open` SYNCHRONOUSLY on the click, BEFORE any await (popup-blocker
    safe), POSTs the BODYLESS connect (retry/cancel likewise — the daemon
    400s any body byte), points the window at the daemon's
    `presentation_url`, and observes every 3 s until the enrollment settles;
    that URL lives only in a ref while the enrollment is pending — never in
    React state, the hook's return value, the DOM or a log
    (`use-workspace-enrollment.test.tsx` serialises the state and asserts
    its absence). A stale id (412) on retry falls back to a fresh connect;
    cancel sends the exact id. The connector inventory
    (`mcp_connector_status`, read once per open) speaks a DIFFERENT
    vocabulary from the enrollment status — `completed` there is the
    connected state — and a 401/403/404/412 folds to "unknown" (notice
    shown) rather than an error. Hidden for a debug-target chat, the mock
    tour and the draft; dismissible per session. (`enrollment.test.ts`,
    `use-workspace-enrollment.test.tsx`,
    `workspace-enrollment-notice.test.tsx`, the Playwright enrollment test.)
21. **A refused credential is never "unreachable".** The liveness probe is
    typed (`HarnessStatus.status` / `.code`); `toHarnessError` reads the raw
    problem body off the SDK error's `cause` (the SDK collapses every 401
    into "Authentication failed" and narrows unregistered codes to
    "unknown"); and `offline-cause.ts` names the class: the proxy's 401
    `oidc_login_required` / `oidc_session_expired` (Studio refuses to dial
    the deployment anonymously), its 502 `oidc_idp_unavailable`, and a
    daemon 401/403 ("Credential rejected", with the MECATL_AUTH_TOKEN /
    audience / issuer remedy). Those render `auth-recovery-banner.tsx` at the
    point of failure — cause, remedy, Sign in / Sign in again (only when
    `/api/auth/oidc/status` reports `configured`; the popup opens on the
    click), "Open sign-in settings", Retry — and re-probe on the callback
    page's `mecatl-oidc` message and on window focus, so the connected flip
    (and `use-agent-chat`'s transcript rehydrate, the resume of the open
    chat) never waits for the 5 s poll. Managed mode has no upstream sign-in:
    its daemon 401 (the controller's own token refused) gets "Restart daemon"
    and the controller-token remedy instead of a sign-in link to a provider
    page with no sign-in card; an `oidc_*` code proves the proxy is external
    and keeps the sign-in actions whatever `mode` says. A chat-level 401 puts
    the same title + remedy on the error strip and re-probes at once.
    Everything else keeps
    "Mecatl is unreachable." There is ONE deployment (MECATL_BASE_URL): no
    saved-target list, and the daemon URL is never rendered (rule 3).
    (`offline-cause.test.ts`, `sdk-auth-errors.test.ts`,
    `auth-recovery-banner.test.tsx`, `runtime-status.auth.test.tsx`,
    `use-agent-chat.auth-failure.test.ts`, hermetic 401 relay row.)
22. **The top nav carries no status chips.** The former connection
    indicator and posture badge were removed for a non-technical audience:
    an unhealthy connection is the banners' job (offline / auth-recovery /
    live-feed reconnecting), the daemon's posture is read in Settings →
    Permissions, Settings → Diagnostics and ⋯ → Session details, and
    changed from Settings → Permissions or the composer's Mode menu
    ("Safety level", `usePostureControl` in `chat-input.tsx`, the same
    `saveHarnessPermissions` write with the same Auto/Yolo confirmation).
23. **The `?prompt=` deep link never sends without a click.** The chat
    route's arrival prompt (`lib/chat-seed.ts` → `use-seed-prompt.tsx`, the
    web analogue of `mecatui -p`; the PWA share target lands there too)
    pre-fills the composer. `&send=1` only puts the exact text behind the
    `SeedPromptDialog` confirmation, because a URL is drive-by reachable and
    under posture auto/yolo a prompt is tool execution in the user's
    workspace; a same-origin referrer proves nothing (a link in a chat
    message is same-origin). A leading-`/` prompt is a command and stays
    prefill-only whatever `send` says; control characters other than newline
    and tab are dropped; the query is stripped with a native replaceState on
    the CURRENT path once consumed, so a reload or Back never re-seeds.
    (`chat-seed.test.ts`, `use-seed-prompt.test.tsx`,
    `seed-prompt-dialog.test.tsx`, the Playwright `?prompt=` tests.)
24. **Daemon options are controller-owned spawn FLAGS; the browser never
    composes a mecated flag.** The Studio-owned `daemon-options.json`
    (`GET|PUT /daemon-options`, `src/lib/daemon-options.mjs`) carries the
    knobs mecatui documents for the tool catalog and the two memory stores:
    project memory (`--memory-dir`; OFF = the flag omitted, which is how
    mecated turns Remember/Recall off — never a `--no-memory`), the user
    model (`--no-user-model`, `--user-model-dir`,
    `--user-model-review-interval`, emitted only when ≠ 1), skill discovery
    (`--skills-dir`; OFF = omitted, rule 12), slash commands
    (`--commands-dir` when a dir is set, else `--enable-commands`; OFF =
    nothing, mecated's default) and MCP discovery (`--toolhive=false`,
    `--toolhive-group` only while discovery is on, `--mcp-resource-tools=false`,
    `--mcp-prompts=false`). The DEFAULT document is the pre-feature command
    line byte for byte. Knobs with another owner are NOT here — `--no-shell`
    is the permissions document's (rule 13), learning/steer/soul are the
    runtime settings' — one writer per flag. Every set directory is a TRUST
    BOUNDARY (mecated's flag help: a SKILL.md or command template steers
    the model like AGENTS.md) and is confined at save time to the live
    workspace, the default root or the mecatl config dir, lexically AND
    after realpath of its deepest existing ancestor; an existing path must
    be a real directory. Only an ENABLED store's directory is created
    before the spawn. A refused start rolls the previous document back.
    `/status.skills` / `.memory` gain `enabled` and a `scope` computed from
    the resolved path (`project` / `user` / `studio` / `other`), never a
    hard-coded "project". Both verbs are studio-header-gated (the GET names
    directories on this machine; the proxy adds the header) and 409 in
    external mode. The only UI left over this document is Settings →
    Memory's two switches (`memory-stores-card.tsx`: project memory, facts
    about you — the `capabilities.memory`/`user_model` rows); the former
    Tools page (skills directory, slash-command templates) and the MCP
    discovery flags card were removed as operator configuration, and the
    shared `daemon-options-card.tsx` scaffold went with them. The document
    keeps every other field at its default. What the UI shows as ON is
    always the daemon's capability document, never the saved document, and
    `useDaemonOptions().save` re-probes it after the restart.
    (`src/lib/daemon-options.test.ts`, `harness/daemon-options.test.ts`,
    `use-daemon-options.test.ts`, `memory-stores-card.test.tsx`,
    `skill-tool-disabled-banner.test.tsx`, `controller-security.test.ts`,
    hermetic 409 + header-gate rows.)

## Gotchas

- `npm test` runs vitest in watch mode; CI and `task studio:test` use
  `npx vitest run` + `npm run test:server`.
- The controller restarts `mecated` on every config write; in-flight runs and
  session ids die with it. Surfaces warn before writes that restart. Startup
  rides the daemon's ready file (`--ready-file` + an ephemeral `--http-addr`
  + a mkfifo lifetime pipe — Node's stdio "pipe" is a socketpair mecated
  rejects); `/status` reports the ready doc's `apiMajor`/`features`/
  `deployment`.
- `package-lock.json` cannot be regenerated from scratch while the
  `file:../sdk/typescript` link is present: both npm 10 and npm 11 crash in
  arborist (`Cannot read properties of null (reading 'edgesOut')`) walking the
  SDK's pnpm-managed `node_modules`. Keep the committed lock and install
  INCREMENTALLY (`npx -y npm@10.9.4 install`); bump a transitive family that
  peer-pins itself to one exact version (tiptap) through `overrides`, never by
  deleting the lock. `npm audit fix` hits the same crash — bump by hand.
- FireNow (`POST /v1/schedules/{name}/fire`) is synchronous — the request lasts
  the whole agent run.
- Live re-attach to a running session rides the durable watch
  (`GET /v1/sessions/{id}/watch`, ADR 0250; gate on the
  `watch_session_events` feature): SSE `{event, cursor, phase}` envelopes —
  replay from the cursor (empty = the beginning), one event-less
  `phase: "live"` boundary frame, then live follow. The SDK's
  `session.attach()`/`activity()` own the envelope decoding; `use-agent-chat`
  attaches when the inventory reads running/awaiting and Studio is not itself
  driving the run.
  Residual: `POST /prompt` still cancels its run on client disconnect, so a
  reload of the DRIVING tab still ends the run — the watch covers runs
  driven elsewhere (schedules, other tabs/clients) and parked approvals.
  An OPEN chat with no run to render ALSO holds a METADATA watch (mecatui's
  `armLiveFeed`): the daemon has no "from now" for a session with no run,
  so it replays from the beginning (or the last cursor this tab saw) and
  SKIMS — nothing rendered, `session.title` forwarded in either phase, a
  LIVE run-bearing frame this tab is not driving re-walks the inventory
  once per run id so the row flips to running and the full watch takes
  over. `session.title` translates to the `title` StreamEvent (never a
  bubble, never "not rendered yet") and `useAgentSessions().applyTitle`
  adopts it onto the row behind the title lifecycle REVISION (strictly
  newer wins; unknown on either side adopts) — header, sidebar and tab
  title update live. The SDK's `client.status` store is mirrored by
  `use-connection-status.ts` into `connection-status-banner.tsx` (layout
  band): `reconnecting` = amber "Live feed reconnecting…" (the state, not
  a counter — the SDK's per-watch `AttachOptions.onReconnect` exists, but
  one number over several watches would mislead), `unauthorized` =
  destructive strip that re-probes the runtime ONCE so the auth-recovery
  banner takes over, quiet whenever the runtime banner already owns the
  band. (`events.test.ts` session.title, `session-title.test.ts`,
  `use-agent-sessions.title.test.ts`, `use-agent-chat.title.test.ts`,
  `use-connection-status.test.ts`, `connection-status-banner.test.tsx`.)
- A tool call parked on an MCP browser sign-in (`authorization.required`)
  ENDS the prompt stream without a result; only the authorization controls
  move the run again (`GET …/mcp-authorizations/{id}/presentation`, and the
  BODYLESS `POST …/recheck` / `…/cancel` SSE relays — the daemon 400s any
  body byte; the SDK posts no body on those routes). The
  takeover card (`authorization-panel.tsx`) fetches the URL only when opened
  or copied — it never rides an event — and after an open/copy
  `use-agent-chat` re-checks every 3 s (a 10 s first-event bound per
  control, one control stream at a time so exactly one adopts the
  continuation run; polling stops when the request resolves from any
  stream, on cancel, chat switch, or unmount). Cancel confirms first: it
  fails the parked tool call.
- The composer's "Insert from MCP" (mecatui's f8 prompts and ctrl+r
  resources pickers: `mcp-prompt-picker.tsx`, `mcp-resource-picker.tsx`,
  owned by `ChatInput` through `useMcpComposerInsert`) is gated on
  `serverCapabilities.mcp` — a daemon granting only `mcp_connector_status`
  never receives a prompt or resource RPC — and the picked text is APPENDED
  after the draft as paragraphs (`appendComposerText`), reviewed, never sent.
  Prompts insert the TUI's role-prefixed join (`renderPromptForComposer`);
  a resource's binary chunks are named, never inserted; every insertion is
  clamped to 64 KiB with a note. The Tools shortcuts — `mcp.inventory` ⌘.,
  `mcp.resources` ⌘;, `mcp.prompts` ⌘' — are registered only while the
  capability is on (the chords keep their native meaning otherwise); they
  ride punctuation because every mnemonic letter chord is bound, browser-
  reserved or a Firefox DevTools key (the registry's Tools note has the
  audit). `requestOpenMcpPicker(kind)` is the outside opener for a slash
  built-in; the composer that last held focus answers. (`harness/mcp.test.ts`,
  `mcp-prompt-picker.test.tsx`, `mcp-resource-picker.test.tsx`,
  `chat-input-mcp-insert.test.tsx`, `registry.test.ts`, the Playwright
  MCP prompt / resource tests.)
- The per-chat / per-schedule TOOL PROFILE (the daemon's
  `CreateSessionRequest.profile`, `""` | `"no-fs"`, ADR 0291 — the web
  analogue of a shell-less run) is picked from the composer's own Tools
  pill (`ToolProfileSelector` in `tool-profile-picker.tsx`, next to Mode;
  the mobile mode sheet keeps the rows) on a DRAFT only and rides the mint (`useSessionMode().profileRef` →
  `useAgentChat({ createProfile })` → `createHarnessSession({ profile })`,
  `""` omits the key so the ordinary create body is unchanged). The daemon
  fixes it at create and reports it on NO snapshot or row, so a live chat
  shows only what Studio remembered choosing
  (`src/lib/session-profile-memory.ts`, browser-local, bounded; a chat
  minted elsewhere is UNKNOWN and shows no line — never a guessed default).
  Schedules own the same field in the form ("Tool profile" select,
  `schedule-form.tsx`) and the detail page's Tools fact + `no filesystem`
  badge. `serverCapabilities.bash === false` (the operator's `--no-shell`)
  adds a muted "Shell is disabled on this daemon" note; there is no
  capability flag for profiles, so a pre-0291 daemon's 400 is shown
  verbatim on the draft. (`tool-profile.test.ts`,
  `session-profile-memory.test.ts`, `create-body.test.ts`,
  `use-session-mode.test.ts`, `use-agent-chat.profile.test.ts`,
  `tool-profile-picker.test.tsx`, `schedule-form.test.tsx`,
  `use-agent-cron.test.ts`.)
- The composer has NO memory pill: memory is a daemon setting
  (`--memory-dir`, `--no-user-model`) with no per-session API, so the only
  place it is shown or changed is Settings → Memory
  (`memory-stores-card.tsx` reads the daemon's `serverCapabilities.memory`
  / `user_model` through `readMemoryStores`). Do not add a per-chat toggle:
  it could never reach the daemon.

<!-- BEGIN:nextjs-agent-rules -->

# This is NOT the Next.js you know

This version has breaking changes — APIs, conventions, and file structure may all differ from your training data. Read the relevant guide in `node_modules/next/dist/docs/` (resolved from this file's directory; in monorepos the `next` package may not be visible from the repo root) before writing any code. Heed deprecation notices.

This block is written and re-added by `next dev` — verify at `node_modules/next/dist/server/lib/generate-agent-files.js`. Removing it from a diff only re-creates the uncommitted change; committing it with your work keeps the tree clean.

<!-- END:nextjs-agent-rules -->
