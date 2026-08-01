# ADR 0025 — mecatui UX Discoverability

- Status: Accepted
- Date: 2026
- Scope: ServerCapabilities wire channel and mecatui help overlay, honest empty-states, and zero-state card

## Context

mecatui's secondary feature set — MCP inventory, resources, prompts, agent team, expand details — was reachable only through control-key chords advertised in one cramped footer line. There was no help overlay, no first-run guidance, and on the default embedded server most chords opened an empty box with no explanation. The core problem was that a static, ui-local capability matrix cannot distinguish "feature not enabled on this server" from "feature enabled but currently empty" for an external mecated with different configuration.

## Decision

Option C was chosen: a real ServerCapabilities wire channel, delivered as a field on CreateSessionResponse so it arrives in zero additional round-trips. The server populates it from the built service state — registered tools and wired config seams — never from a static list. The client translates it to a proto-free plain struct. Phase A delivers the wire channel; Phase B delivers the UX surfaces: a question-mark help overlay (empty-input guarded), a shortened footer, caps-aware honest empty-states for MCP overlays and the slash-command palette, and a first-run zero-state welcome card. Built-in TUI commands that always exist are registered client-side independent of server caps.

## Consequences

Both phases shipped. Current behaviour is in docs/architecture.md. Status is in docs/design/PRODUCTION-READINESS.md. Adding a new optional feature requires only a new bool field on the proto message and corresponding logic in the server capabilities method; old clients treat absent fields as false. The port.ProviderCapabilities multimodal-input seam is a distinct concept from ServerCapabilities feature enablement and must not be conflated.

---

Decision: the availability seam is **Option C — a real `ServerCapabilities`
wire channel** (authoritative for embedded AND external servers), structured as
two separately-reviewable phases.

- **Phase A** — the `ServerCapabilities` wire channel: proto + server + client.
  Ships and is reviewed on its own (it is inert until Phase B consumes it).
- **Phase B** — the UX surfaces (`?` help overlay, footer, honest empty-states,
  zero-state), now driven by the relayed capabilities, not by ui-local guesses.

Scope: `contracts/proto`, `internal/adapter/server`, `internal/app`,
`cmd/mecatui/client`, `cmd/mecatui/ui`. The ui still imports nothing
`internal/...` and no proto — `client` owns the proto→plain translation.

## The complaint

> "It works well but the UI is unintuitive — I don't know how to view skills, MCPs, etc."

The whole secondary feature set — MCP inventory (`ctrl+o`), MCP resources
(`ctrl+r`), MCP prompts (`ctrl+p`), agent team (`ctrl+a`), expand details
(`ctrl+t`) — is reachable only through control-key chords advertised in one
cramped footer line (`view.go:150-151`). There is no `?` help, no menu, no
first-run guidance. The `ctrl+o/r/p MCP` token is undecodable (the o/r/p
mnemonic lives only in a code comment, `keys.go:99-101`). And on the default
embedded server most of those chords open an empty box, with no explanation of
why — because the features are off, but the footer advertises them anyway.

## Why Option C (the wire channel) over a static matrix

The whole point of the honest empty-state is to distinguish **"this feature is
not enabled on this server"** from **"this feature is enabled but currently has
nothing to show."** A static, ui-local matrix can't tell those apart for an
external `mecated` — it can only hardcode the embedded default. The server is
the only thing that knows the truth, and it knows it *precisely* (see the
population point in Phase A — it's nil-checks on already-wired Config seams plus
catalog lookups, not a hardcoded list). Option C carries that truth to the ui as
plain data, exactly like `Usage` and `Event` already cross the boundary.

The correctness crux: **caps are read from the BUILT service state, never
hardcoded.** Phase A's design pins the exact read points so they cannot drift
from what the engine/catalog actually registered.

---

# Phase A — the `ServerCapabilities` wire channel

## A1. Proto: where capabilities live (`contracts/proto/mecatl/v1/harness.proto`)

### Delivery choice: a field on `CreateSessionResponse` (RECOMMENDED)

Three candidate homes were considered:

| Option | Shape | Verdict |
|---|---|---|
| **Field on `CreateSessionResponse`** | `CreateSession` already runs once at connect (`client.go:99-108`); add `ServerCapabilities capabilities = 2;` | **CHOSEN.** Zero new round-trips — the ui already calls `CreateSession` at startup and the result already becomes `SessionReadyMsg` (`update.go:32-36`). Caps are server-lifetime-stable, so once-at-session-creation is the right cadence. Backward-compatible (new field on an existing response; old servers leave it nil → ui reads all-false, see A4). |
| **Dedicated `GetServerCapabilities` RPC** | standalone unary the client calls at connect | Rejected — a second round-trip and a second reader path for data that's free to piggyback on the create call. Justified only if a non-session client needed caps before creating a session; mecatui always creates one. |
| **Part of `Converse` handshake / `session.init`** | caps on the first stream event | Rejected — caps are connection-scoped, not run-scoped; putting them on the per-run stream re-sends them every prompt and couples a static fact to the run lifecycle. |

### Message definition

Add near `CreateSessionResponse` (around `harness.proto:165`):

```proto
// ServerCapabilities reports which optional features this server has ENABLED,
// so a client can present an honest UI (advertise only reachable features, and
// explain an empty inventory as "not enabled" vs "enabled but empty"). Each
// field reflects the BUILT service state (registered tools / wired seams), never
// a static guess — see internal/adapter/server.Service.capabilities(). All
// fields default false, so a client talking to an OLDER server (no caps field)
// degrades safely to "nothing advertised" rather than over-promising.
message ServerCapabilities {
  // mcp is true when MCP inventory/resources/prompts are available (an MCP
  // provider is wired). Gates ctrl+o / ctrl+r / ctrl+p.
  bool mcp = 1;
  // slash_commands is true when slash-command discovery is available (a command
  // lister is wired). Gates the / palette.
  bool slash_commands = 2;
  // memory is true when cross-session memory tools (Remember/Recall) are
  // registered. Agent-side: no overlay, surfaced as prose only.
  bool memory = 3;
  // skills is true when the Skill tool is registered. Gates the /skills
  // inventory browser (the read-only ListSkills snapshot). Activation stays the
  // model's concern — the panel is discovery only.
  bool skills = 4;
  // teams is true when agent teams are enabled (a member-engine factory is
  // wired). Gates the ctrl+a deep view's relevance.
  bool teams = 5;
  // bash is true when the Bash tool is registered (i.e. NOT --no-bash).
  bool bash = 6;
}
```

```proto
// CreateSessionResponse carries the newly-allocated session id.
message CreateSessionResponse {
  // session_id is the id of the created session.
  string session_id = 1;
  // capabilities reports which optional features this server has enabled, for an
  // honest client UI. Nil/absent from an older server → treat as all-false.
  ServerCapabilities capabilities = 2;
}
```

**Growth without breaking the ui:** new features add a new `bool field = N;`.
Old clients ignore unknown fields (proto3); new clients reading an old server
get the zero value (false). The ui never switches exhaustively on the set — it
reads named booleans — so an added field is a pure additive change in the client
mapper + the ui renderer. No enum, no oneof: a flat bool set is the minimal
shape that grows additively and reads trivially. (A `map<string,bool>` was
considered and rejected — it loses compile-time field names and invites
stringly-typed drift; the help overlay needs to name specific features anyway.)

`contracts/gen` is generated — author the `.proto`, then `task generate` (per
CLAUDE.md; note the stale-LSP gotcha — verify with `task build`, not
diagnostics).

## A2. Server: populate caps from BUILT state (the correctness crux)

Caps are computed in the **Service**, which holds the engine (hence the
catalog) and the wired Config seams. Add one method and call it from the
`CreateSession` handler.

### The read points (each maps to a fact the Service already knows)

| Cap | Read point | Why it's authoritative |
|---|---|---|
| `mcp` | `s.cfg.MCPProvider != nil` | The same nil-check the MCP RPCs already gate on (`service.go:733,751,764,782`). Wired ⇔ MCP enabled. |
| `slash_commands` | `s.cfg.Commands != nil` | The same nil-check `ListCommands` gates on (`service.go:885`). |
| `teams` | `s.cfg.MemberEngine != nil` | The same nil-check the team methods gate on (`team.go:99`, the `ErrTeamsDisabled` source). |
| `memory` | `catalogHas("Remember")` | The Remember tool's registered name (`internal/adapter/memory/tools.go:114`). Registered ⇔ memory on. |
| `skills` | `catalogHas("Skill")` | `skills.ToolName == "Skill"` (`engine/adapter/skillfs/tool.go:14`). |
| `bash` | `catalogHas("Bash")` | The Bash tool name (`internal/adapter/tools/bash.go:87`); absent under `--no-bash`. |

The catalog is reachable as `s.cfg.Engine.Catalog` (the public field,
`engine/agent/loop.go:40`) via `Catalog.Lookup(name)` (`tool/catalog.go:46`).

```go
// capabilities reports which optional features THIS service has actually built,
// for the CreateSession response. It is the single source of truth for the
// client's honest-UI affordances; it reads the wired Config seams and the engine
// catalog (NOT a static list) so it can never claim a feature the server did not
// register. Tool presence is checked by the tools' registered names (Remember /
// Skill / Bash). A nil catalog (defensive) yields the tool caps as false.
func (s *Service) capabilities() *mecatlv1.ServerCapabilities {
    has := func(name string) bool {
        if s.cfg.Engine == nil || s.cfg.Engine.Catalog == nil {
            return false
        }
        _, ok := s.cfg.Engine.Catalog.Lookup(name)
        return ok
    }
    return &mecatlv1.ServerCapabilities{
        Mcp:           s.cfg.MCPProvider != nil,
        SlashCommands: s.cfg.Commands != nil,
        Teams:         s.cfg.MemberEngine != nil,
        Memory:        has("Remember"),
        Skills:        has(skills.ToolName), // "Skill"
        Bash:          has("Bash"),
    }
}
```

Naming note: keep the tool-name constants referenced from their owning packages
where cheap (`skills.ToolName`), or define a small `const` block in the server
package documenting the three literals it probes, so the spellings can't silently
drift from the tools. The server already imports `tool`; importing `skills` for
one const is acceptable, or hardcode `"Skill"` with a comment citing
`skills/tool.go:15` — pick one and keep it DRY with a test (A-test #1) that
fails if a tool is registered under a renamed key.

### Wire it into the handler

The gRPC `CreateSession` handler (`grpc.go`) and the HTTP one (`http.go`) both
build a `CreateSessionResponse`. Set `Capabilities: s.capabilities()` in the
Service-level create path so BOTH surfaces carry it (don't duplicate the logic
per-surface). Check whether `CreateSession` returns the proto response directly
or an id the surface wraps; populate at the layer where the proto response is
assembled, once.

> Where caps are read is deliberately the Service (post-build), not
> `internal/app` (build time). Reading at build time would require threading the
> `app.Config` flags into the response and risks "config said X but the catalog
> registered Y" drift. Reading the *built* catalog + the *wired* seams is the
> single honest source — the same predicates the feature RPCs themselves use.

## A3. Client: proto → plain struct → tea.Msg (`cmd/mecatui/client/`)

`client` is the only mecatui package that touches `contracts/gen` + grpc
(`msgs.go:1-14`). It already translates the create response (`client.go:99-108`)
and owns the `Usage`-style plain mirrors. Add a `Capabilities` plain struct and
carry it on the existing `SessionReadyMsg`.

### Plain struct (new `capabilities.go`)

```go
// Capabilities is the proto-free mirror of mecatlv1.ServerCapabilities: which
// optional features the connected server has enabled. The ui renders honest
// affordances from it (advertise only reachable features; explain empty
// inventories as "not enabled" vs "enabled but empty") WITHOUT importing proto.
// All-false is the safe default (an older server omits the field).
type Capabilities struct {
    MCP           bool
    SlashCommands bool
    Memory        bool
    Skills        bool
    Teams         bool
    Bash          bool
}

// capabilitiesFrom maps a proto ServerCapabilities (nil-safe) to the plain
// struct. A nil message (older server) yields the all-false zero value.
func capabilitiesFrom(c *mecatlv1.ServerCapabilities) Capabilities {
    if c == nil {
        return Capabilities{}
    }
    return Capabilities{
        MCP:           c.GetMcp(),
        SlashCommands: c.GetSlashCommands(),
        Memory:        c.GetMemory(),
        Skills:        c.GetSkills(),
        Teams:         c.GetTeams(),
        Bash:          c.GetBash(),
    }
}
```

### Delivery: extend `SessionReadyMsg` (`msgs.go:234`) and `CreateSession`

`SessionReadyMsg` currently carries only the id. Add the caps so the single
connect message delivers both — the ui already handles `SessionReadyMsg` in one
place (`update.go:32-36`):

```go
// SessionReadyMsg carries the session id AND the server's capabilities from the
// async CreateSession. Capabilities drives the ui's honest discoverability
// affordances; an older server yields the all-false zero value.
type SessionReadyMsg struct {
    SessionID    string
    Capabilities Capabilities
}
```

`CreateSession`'s signature returns `(string, error)` today and the ui's
`SessionCreator` interface wraps it (`model.go:24-26`, `main.go:264-272`). Two
clean options:

- **Preferred:** change `Client.CreateSession` to return
  `(string, Capabilities, error)` and update the `sessionAdapter` +
  `SessionCreator` interface to match, with the ui's `createSessionCmd`
  (`update.go:535-544`) building the richer `SessionReadyMsg`. The interface
  stays tiny and the caps ride the same call.
- Alternative: keep `CreateSession` id-only and add a sibling that returns caps.
  Rejected — splits one connect fact across two calls.

Take the preferred path. `SessionCreator` becomes:

```go
type SessionCreator interface {
    CreateSession(ctx context.Context) (string, client.Capabilities, error)
}
```

## A4. Phase A layering proof

- **Proto** is the contract; no Go import direction concerns.
- **Server** (`internal/adapter/server`) already imports `contracts/gen`,
  `engine/agent`, `engine/tool` (`service.go:13-19`) — `capabilities()`
  uses only those. It reads the catalog and Config it already owns; no new
  inward dependency.
- **Client** (`cmd/mecatui/client`) already owns the sole `contracts/gen`+grpc
  import (`msgs.go:1-14`); `capabilitiesFrom` is exactly the `usageFrom`
  pattern (`msgs.go:320`). No proto leaks past it.
- **No ui rendering change in Phase A** beyond the `SessionCreator` signature +
  the `createSessionCmd` plumbing (the ui consumes `client.Capabilities`, a
  plain struct — never proto). The overlay/footer/empty-state consumption is
  Phase B.

Phase A is inert: caps flow to the ui and are stored on the model (the `caps`
field added at B1 can land in Phase A as dead-but-wired state if you prefer the
phases strictly compile-clean), but nothing renders them yet. It compiles, the
wire carries the new field, and it's independently reviewable.

## A5. Phase A file-by-file

| File | Change | Layer |
|---|---|---|
| `contracts/proto/mecatl/v1/harness.proto` | add `ServerCapabilities` message + `capabilities` field on `CreateSessionResponse` | contract |
| `contracts/gen/**` | regenerated via `task generate` (do not hand-edit) | generated |
| `internal/adapter/server/service.go` | add `Service.capabilities()` reading catalog + wired seams | server |
| `internal/adapter/server/grpc.go` / `http.go` (or the shared create path) | set `Capabilities` on the create response (once, at the Service create path) | server |
| `cmd/mecatui/client/capabilities.go` (NEW) | `Capabilities` struct + `capabilitiesFrom` | client |
| `cmd/mecatui/client/client.go` | `CreateSession` returns `(string, Capabilities, error)` | client |
| `cmd/mecatui/client/msgs.go` | extend `SessionReadyMsg` with `Capabilities` | client |
| `cmd/mecatui/ui/model.go` | `SessionCreator` interface signature; (optionally) the `caps` field | ui (plumbing) |
| `cmd/mecatui/ui/update.go` | `createSessionCmd` builds the richer `SessionReadyMsg`; handler stores caps | ui (plumbing) |
| `cmd/mecatui/main.go` | `sessionAdapter.CreateSession` returns caps | composition root |

---

# Phase B — the UX surfaces (pure ui, driven by relayed caps)

Phase B consumes `m.caps` (the `client.Capabilities` stored in Phase A). Every
availability decision now reads relayed truth — no embedded/external hedge.

## B1. Store caps on the model (finish the Phase-A plumbing)

```go
// caps is the connected server's advertised capabilities, delivered once on
// SessionReadyMsg. It drives the honest discoverability affordances: which chords
// the help overlay annotates as available, and whether an empty MCP/commands box
// reads "not enabled" or "none configured". Zero value (all-false) until connect
// and for an older server.
caps client.Capabilities
```

Set in the `SessionReadyMsg` handler (`update.go:32-36`):

```go
case client.SessionReadyMsg:
    m.sessionID = msg.SessionID
    m.caps = msg.Capabilities
    m.phase = phaseIdle
    m.statusMsg = "connected"
    return m, nil
```

## B2. The `?` help overlay

### Keymap (`keys.go`)

Add a `Help` binding to `keyMap` + `defaultKeys`:

```go
Help: key.NewBinding(
    key.WithKeys("?"),
    key.WithHelp("?", "help"),
),
```

**The `?`-is-printable gotcha:** unlike `ctrl+o/r/p`, a bare `?` would type into
the textarea. Open help only when the input is **empty** so `?` in prose still
inserts literally. (Alternative `f1`/`ctrl+h` rejected: `?` is the universal
idiom the complaint expects; `ctrl+h` collides with backspace on many terminals.)

### Model state (`model.go`)

A single bool — the help overlay has one view, no sub-states, no cursor, no RPC
(caps already arrived). A `mcpView`-style enum would be premature structure
(MCP/agents earned their state structs via multiple sub-views + cursor + RPC
results; help has none):

```go
showHelp bool
```

### Routing (`update.go`)

Close path in `onKey`, before the phase switch, consistent with how
`onMCPKey`/`onAgentsKey` own the keyboard:

```go
if m.showHelp {
    if key.Matches(msg, m.keys.Close) || key.Matches(msg, m.keys.Help) {
        m.showHelp = false
        _ = m.ta.Focus()
        return m, nil
    }
    return m, nil // swallow other keys while help is up
}
```

Open path in `onIdleKey`, guarded by empty input:

```go
case key.Matches(msg, m.keys.Help) && strings.TrimSpace(m.ta.Value()) == "":
    m.showHelp = true
    m.ta.Blur()
    return m, nil
```

### Rendering (`view.go` + new `help.go`)

Add a body branch in `View`, first among the overlays (mutually exclusive in
practice, but help wins defensively):

```go
case m.showHelp:
    body = renderHelpOverlay(m.deps.Theme, m.caps, m.width, m.vp.Height())
```

`renderHelpOverlay` lives in `cmd/mecatui/ui/help.go`, reusing the centered-card
treatment (`askCard` + `lipgloss.Place`) shared by `renderMCPOverlay`
(`mcp.go:466-470`) and `renderAgentsOverlay` (`agents.go:232-236`). It takes
`client.Capabilities` and annotates each feature from it — **no static
embedded/external note**:

```
mecatui — keys & features

  Prompting
    enter            send the prompt
    shift+enter      newline (also ctrl+j)
    /                slash-command palette        {caps.SlashCommands ? "" : "[not enabled]"}
    esc              cancel the running turn

  Inspect (while idle)
    ctrl+o           MCP inventory                {caps.MCP ? "" : "[not enabled]"}
    ctrl+r           MCP resources                {caps.MCP ? "" : "[not enabled]"}
    ctrl+p           MCP prompts                  {caps.MCP ? "" : "[not enabled]"}
    ctrl+a           agent team                   {caps.Teams ? "shown when a team is running" : "[not enabled]"}
    ctrl+t           expand/collapse details

  General
    pgup / pgdn      scroll the conversation
    ?                this help (type ? on an empty prompt)
    ctrl+c           quit

  Skills activate automatically — the model invokes them itself. When skills are
  enabled the inventory IS browsable: {caps.Skills ? "type /skills to browse the skills inventory." : "skills are not enabled on this server (nothing to browse)."}
  Slash commands (/) are the human-facing analog.

  {caps.Memory ? "Cross-session memory is on — context carries across runs." : ""}

  esc or ? to close
```

Unavailable rows are rendered with the `muted` style + a `[not enabled]` tag
(grey-out); available rows in the normal style. The o/r/p mnemonic never needs
decoding — the labels are plain English.

### The on/off truth now comes from caps (not a hardcoded matrix)

The Phase-A server populates these precisely; the help overlay just reflects
`m.caps`. At the time of this design the **embedded default** showed: memory on;
MCP, slash-commands, skills off (`[not enabled]`); teams on (contextual).
(Since superseded: the embedded server now enables every free+local feature by
default — slash-commands and skills show as enabled out of the box; only
external/config/trust-gated features stay off.) For an **external mecated** with
MCP configured, the same overlay correctly shows MCP available — the whole
reason for Option C.

## B3. Footer (`view.go:150-151`)

```go
// before
help := "enter send · shift+enter newline · esc cancel · ctrl+o/r/p MCP · " +
    "ctrl+a team · ctrl+t details · ctrl+c quit"

// after
help := "? help · / commands · ctrl+c quit"
```

The full chord list moves into the overlay. Optionally drop `/ commands` from
the footer when `!m.caps.SlashCommands` (→ `"? help · ctrl+c quit"`) so the
footer never advertises a disabled entry point — a small honest touch the footer
can now afford because caps are available. The fitFooter tiering
(`view.go:183`) is unchanged; this line is rendered raw (`view.go:156`), not
width-tiered, and is short enough at any width.

## B4. Honest empty-states — now distinguishing "not enabled" from "empty"

This is the Option-C payoff. The MCP overlays currently show bare facts
(`mcp.go:483` "no MCP sources", `:554` "no resources", `:586` "no prompts").
Replace with caps-aware copy:

```go
// in renderMCPPanel, the empty branch (mcp.go:482-484):
if !st.loading && len(st.sources) == 0 && st.errMsg == "" {
    if !caps.MCP {
        b.WriteString(th.Style("muted").Render(
            "MCP is not enabled on this server. Run a full mecated with MCP configured to use it.") + "\n")
    } else {
        b.WriteString(th.Style("muted").Render(
            "No MCP sources configured on this server.") + "\n")
    }
}
```

Same two-branch pattern for resources (`:553`) and prompts (`:585`):
- `!caps.MCP` → "MCP is not enabled on this server."
- `caps.MCP` + empty → "No resources advertised by the connected MCP servers." /
  "No prompts advertised by the connected MCP servers."

To thread caps into the renderers: `renderMCPOverlay` is called from `View`
(`view.go:33`) with `m.mcp`; add `m.caps` to its signature (and to
`renderMCPPanel`/`renderResourceList`/`renderPromptList`). These are pure
functions taking `client.Capabilities` — still proto-free, still unit-testable.

**Commands palette** (`palette.go`): when the user types `/` but there are zero
matching commands, `renderPalette` shows a one-line muted note instead of "".

> **Update — built-in client-side commands.** The palette is no longer ever
> empty: the TUI ships **built-in** commands (`builtins.go`) that always exist,
> independent of the server's `slash_commands` capability and even with no
> `Commander` wired. `/clear` (reset conversation + scrollback) and `/help` (open
> the keys-&-features overlay) are always registered (they act purely on the
> Model); `/mcp`, `/agents`, `/team`, and `/skills` are caps-gated. `syncPalette` filters over `mergeCommands(m.builtinRows(),
> m.palette.commands)` — built-ins lead, then the discovered workspace rows, with
> a built-in winning any name collision. A bare built-in line (e.g. `/clear`) is
> intercepted in `submitPrompt` and run locally, so it never reaches the model;
> palette-enter over a built-in row runs it directly (rather than text-completing,
> which would write a trailing space and slip past that intercept). Because
> built-ins always exist, the old caps-based empty-state copy ("not enabled" vs
> "none found") is no longer meaningful — the only way to reach the note is a
> typed prefix matching nothing (e.g. `/zzz`), so `paletteEmptyNote` now renders a
> single neutral **"no matching command"**. The footer always shows
> "`/ commands`" and the help/zero-state always advertise `/`. (`/compact` is a
> deliberate follow-up: it needs a server RPC that does not yet exist.)

> **Update — issue #15: `/agents` vs `/team` split.** `/agents` is now the
> agent-**definition inventory** (palette-only, gated on `caps.agents` + a wired
> `AgentLister`): a read-only `ListAgents` panel listing the resolved registry the
> `Subagent` tool routes delegations to (name · description · `model:`/`perm:`/`tools:`
> metadata), modelled on the `/skills` panel (`agents_inventory.go`). The
> live-agent-team overlay moved to a new `/team` built-in (gated on `caps.teams`),
> still bound to `ctrl+a` (`team.go`, renamed from `agents.go`). `caps.agents` is a
> new `ServerCapabilities` bit, INDEPENDENT of `caps.teams`: defs are browsable
> even with no member-engine wired (`Service.capabilities()` sets it from a
> non-empty `Config.Agents` snapshot). `ctrl+a` now also opens the live overlay
> **mid-run** (not just idle) — it still stays inert under a permission modal.

Superseded original design (kept for context): a one-line note branched on caps:
- `!caps.SlashCommands` → muted "slash commands are not enabled on this server"
- `caps.SlashCommands` + zero → muted "no slash commands found in this workspace"

`renderPalette` (`palette.go:166`) returns `""` when closed; a sibling path
renders the one muted line when `commandPrefix` is true and `filtered` is empty.
`m.caps` is threaded into `renderPalette` (called from `view.go`).

**Memory needs no empty-state** — agent-side, no overlay; surfaced only as the
help-overlay prose line gated on `caps.Memory`.

## B5. First-run zero-state card (`view.go` / new `help.go`)

On launch the viewport is blank; the textarea placeholder (`model.go:145`) is
the only onboarding. Render a centered welcome card in the empty viewport when
idle and the conversation is empty.

`conversation` has a `blocks []block` slice and no emptiness predicate yet
(`conversation.go:177`) — add `func (c *conversation) isEmpty() bool { return len(c.blocks) == 0 }` (tidy-first) and branch in `View`'s default body:

```go
default:
    if m.conv.isEmpty() && m.phase == phaseIdle {
        body = renderZeroState(m.deps.Theme, m.caps, m.width, m.vp.Height())
    } else {
        body = m.vp.View()
    }
```

Content, tailored to caps (only suggest reachable entry points):

```
Welcome to mecatui

  Type a request below and press enter.

  ?   keys & features
  {caps.SlashCommands ? "/   slash commands" : ""}
  {caps.Teams ? "ctrl+a  agent team (when running)" : ""}
  ctrl+t  details

  {caps.Memory ? "Cross-session memory is on — I'll remember context across runs." : ""}
```

Not an overlay (no keyboard capture) — it is just the empty-viewport content, so
typing / `/` / `?` work over it. It vanishes the instant the first prompt is
recorded. Center via the same `lipgloss.Place` the overlays use.

**Update (welcome splash):** the plain card above was later replaced by a richer
first-run **splash** built in the new `cmd/mecatui/ui/welcome` subpackage (which
imports only `theme` + the charm libraries + stdlib — never `client`/`ui`, keeping
the inward-only convention; `ui` imports it). `renderZeroState` is now a `Model`
method that assembles a `welcome.Info` (cwd / model / version / tagline + the SAME
caps-tailored `zeroStateRows()` affordances + the `caps.Memory` note, all
byte-equivalent in semantics to the card above) and calls `welcome.Splash`, then
frames it with `centerCard`. The splash adds a faithful **mascot** (truecolor
half-block on any terminal, plus a zero-dependency Kitty Unicode-placeholder
high-res path on kitty/Ghostty/WezTerm/Konsole) and a gradient **"mecatl"
wordmark** (truecolor jade→gold, collapsing to the single accent color on a poorer
profile) above the info block. `--no-banner` (and `--quiet` / a non-interactive
stdin) short-circuits to the legacy plain card. The affordance/caps semantics are
preserved exactly — only the surrounding presentation is richer. See
`docs/tui.md` ("First-run welcome splash") for the user-facing description.

## B6. Agents-roster hint line — VERIFIED ALREADY PRESENT (stale review item)

Prior review item #6 ("roster lacks an in-overlay hint line") is **stale**. The
roster already ends with one (`agents.go:333`):

```go
out.WriteString("\n" + muted.Render("↑/↓ select · enter focus member · t tasks · esc close"))
```

The focus pane (`agents.go:422` "esc back") and tasks sub-view (`agents.go:558`
"t roster · esc close") also have them. No change needed. Optional Low polish:
extend the roster line to mention the windowed jumps (`pgup/pgdn`, `home/end` —
`keys.go:143-150`): `"↑/↓ select · pgup/pgdn page · enter focus · t tasks · esc close"`.

## B7. Phase B layering proof

Every Phase-B change is pure-ui and respects `docs/tui.md:16-20` (ui imports no
`internal/...`, no proto):

- `help.go` (overlay + zero-state), the footer, the empty-states, the palette
  note — all render from `theme.Theme` + `client.Capabilities` (a plain
  proto-free struct, mirroring how the ui already consumes `client.Usage`,
  `client.MCPSource`, etc.). No proto, no internal.
- `m.caps` is `client.Capabilities` — the same kind of relayed plain data the ui
  already holds. The ui never learns capabilities by importing config; it reads
  the struct `client` translated for it. That is the layering rule honored
  exactly: proto→plain translation lives in `client`, the ui renders plain data.
- Routing changes in `update.go` add no imports (`key`, `client`, `strings`
  already imported).

Naming note: the ui-side field is `caps`/`client.Capabilities` — the same word
as the wire type, which is fine here because the client struct IS the
authoritative relayed truth (unlike the rejected static-matrix design, there's
no second "documented default" concept to disambiguate from). Keep it distinct
from `port.ProviderCapabilities` (a different, ACP-only concept; see the
corrections section).

## B8. Phase B file-by-file

| File | Change | Layer |
|---|---|---|
| `cmd/mecatui/ui/keys.go` | add `Help` binding (`?`) | ui |
| `cmd/mecatui/ui/model.go` | add `showHelp bool`; (caps field from Phase A) | ui |
| `cmd/mecatui/ui/update.go` | route help open (empty-input guard) + close | ui |
| `cmd/mecatui/ui/view.go` | help body branch; zero-state branch; shorten footer; thread `m.caps` into MCP/palette renderers | ui |
| `cmd/mecatui/ui/help.go` (NEW) | `renderHelpOverlay` + `renderZeroState` + content builders (caps-driven) | ui |
| `cmd/mecatui/ui/mcp.go` | caps-aware empty-state copy; thread caps into `renderMCPOverlay`/panels | ui |
| `cmd/mecatui/ui/palette.go` | caps-aware "no commands" / "not enabled" note | ui |
| `cmd/mecatui/ui/conversation.go` | add `isEmpty()` predicate | ui |
| `cmd/mecatui/ui/team.go` (was `agents.go`) | (optional Low) extend roster hint with pgup/pgdn/home/end | ui |

---

## Test plan (offline)

mecatui tests are fully offline: a scripted fake behind `Recv()` plus teatest
View/whole-program goldens at a fixed size (`docs/tui.md:154-161`,
`task test:golden`). Server/client tests use `mockllm` + `memfs`.

### Phase A

1. **Server caps reflect the catalog (table test).** Build a `Service` with
   varying `Config` and catalogs; assert `capabilities()` matches:
   - MCPProvider nil/non-nil → `Mcp` false/true.
   - Commands nil/non-nil → `SlashCommands` false/true.
   - MemberEngine nil/non-nil → `Teams` false/true.
   - catalog with/without `Remember` → `Memory`; with/without `Skill` →
     `Skills`; with/without `Bash` → `Bash`.
   This is the correctness guard: it fails if a tool is renamed (catching the
   drift a static design couldn't) or if a future refactor flips a default.
   Mirror the existing `TestProviderCapabilitiesSurface` (`multimodal_test.go:162`).
2. **CreateSession carries caps (gRPC + HTTP).** Assert the create response's
   `Capabilities` is populated and matches `capabilities()` on both surfaces —
   guards that population happens at the shared create path, not one surface.
3. **Client translation (`capabilities_test.go`).** `capabilitiesFrom(nil)` →
   all-false; a populated proto → matching struct. Mirror the `usageFrom` tests
   (`msgs_test.go`). Plus a fake-client test that `CreateSession` returns the
   relayed caps and `createSessionCmd` builds a `SessionReadyMsg` carrying them.
4. **Backward-compat.** A create response with a nil `capabilities` field
   (older server) → ui receives all-false → no over-promise. Covered by the
   `capabilitiesFrom(nil)` test plus an integration assertion that the ui at
   all-false greys everything.

### Phase B

5. **Help overlay open/close (unit).** `?` on empty input → `showHelp` true,
   textarea blurred; `esc`/`?` → false, focused; `?` on non-empty input → stays
   false and the rune reaches the textarea. Pins the empty-input guard.
6. **Help overlay goldens (View) — two caps fixtures.** Snapshot `View()` with
   `showHelp` set under (a) embedded-default caps (memory on; mcp/commands/skills
   off; teams on) and (b) all-on caps. NEW goldens. This proves the caps-driven
   annotations render both ways — the core Option-C behavior.
7. **Caps-aware empty-states (unit).** Render `renderMCPPanel` with empty
   sources under `caps.MCP=false` → "not enabled"; under `caps.MCP=true` → "none
   configured". Same for resources/prompts and the palette note. Pure functions,
   table-tested.
8. **Zero-state golden(s).** Fresh idle model, empty conversation, under
   embedded-default caps → welcome card omits `/` line; under all-on caps →
   includes it. NEW goldens. Confirm it vanishes after `addUser`.
9. **Footer golden refresh.** The shortened footer help line changes the bottom
   line of EVERY footer-bearing whole-program/View golden. **All footer goldens
   refresh** (`task test:golden`); review the diff is limited to the help line.
   This is the widest blast radius — flag in the Phase-B PR.

No test hits a network or a real model. The help overlay and zero-state have no
RPC; caps arrive on the scripted `SessionReadyMsg`, so goldens are trivial to
drive at chosen caps fixtures.

---

## Corrections carried from the prior review (verified against current code)

- **Agents-roster hint line is already present** (`agents.go:333`) — review item
  #6 is stale; no change needed (optional Low polish only). See B6.
- **`port.ProviderCapabilities` is the multimodal-input seam** — it gates which
  non-text prompt input the wired provider consumes. It is advertised via the ACP
  `handleInitialize` handshake AND (since the `@`-mention/media iteration) projected
  onto the gRPC `ServerCapabilities.image`/`.audio` fields, which gate the mecatui
  `@`-mention file-attach UX (the client refuses to send a media part a provider
  cannot read). It remains a distinct CONCEPT from the rest of `ServerCapabilities`
  (feature enablement / wired seams) even though both now ride the same gRPC
  message: the media caps are read from the provider seam (`Engine.Capabilities()`),
  the others from registered tools / nil-checked config. Keep the source-of-truth
  distinction; don't conflate the population logic.

---

## Risks / open questions

1. **`CreateSession` signature change ripples** (`(string, error)` →
   `(string, Capabilities, error)`). It touches `Client.CreateSession`, the ui
   `SessionCreator` interface (`model.go:24-26`), the `sessionAdapter`
   (`main.go:264-272`), and `createSessionCmd` (`update.go:535-544`). All within
   Phase A; small and mechanical, but it's the one cross-file ripple — call it
   out in the Phase-A PR. The ACP/other surfaces don't use this client method,
   so blast radius is mecatui-only.
2. **Backward-compat both directions.** New client + old server: caps field
   absent → all-false → ui under-advertises (safe). Old client + new server:
   ignores the new field (proto3) → unchanged behavior. Both safe; #4 tests it.
3. **Where exactly the create response is assembled** (Service vs per-surface).
   Verify `CreateSession` builds the proto response in one Service-level place;
   if gRPC and HTTP each assemble their own response, populate caps in BOTH from
   the shared `capabilities()` — do NOT let one surface omit it. (The design
   assumes a shared create path; confirm during Phase A.)
4. **Tool-name drift.** `capabilities()` probes `"Remember"`, `"Skill"`,
   `"Bash"` by literal name. If a tool is renamed, caps silently go false. A-test
   #1 guards this by asserting caps against a catalog built from the real tool
   constructors — reference `skills.ToolName` rather than a bare literal where
   cheap, and keep the test as the backstop.
5. **Footer golden churn** (Phase B). Wide but mechanical; review the diff is
   only the help line.
6. **`?` mid-prompt** (Phase B). The empty-input guard means `?` won't open help
   while typing a prompt. Standard tradeoff; the footer + placeholder advertise
   it. Accept.
7. **Help is idle-only.** A user mid-run can't open help to learn `ctrl+t`.
   Minor — inline collapsible headers already advertise expand/collapse
   (`view.go:147-149`); not worth letting help compete with the streaming
   transcript.
8. **Phase-A "inert" question.** Strictly, Phase A introduces `client.Capabilities`
   and the `SessionReadyMsg` field but nothing reads them until B. Decide whether
   to add the `m.caps` model field (B1) in Phase A (so the data is stored, just
   unrendered — cleaner git boundary) or in Phase B (so Phase A has zero ui
   field churn). Recommend storing in Phase A (the `SessionReadyMsg` handler is
   already being edited there for the richer msg) so Phase B is purely render.


---

*Part of the [design docs](../design/README.md). Related: [Clipboard image paste (`ctrl+v`)](0026-clipboard-image-paste.md).*
