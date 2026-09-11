# ADR 0082 — Factory MCP wiring for the one-shot mains

- Status: Accepted
- Date: 2026-08-03
- Scope: `cmd/mecatequi`, `cmd/mecak8s`, `cmd/mecated` flag surface + `internal/cliconfig` (cmd/composition layer only; no `engine/` change)

## Context

A scheduler in the same platform launches mecatequi as a one-shot Kubernetes Job /
local subprocess to execute a work item, injecting a **short-lived per-run identity**
the run must present as a Bearer token to MCP endpoints — a ToolHive vmcp and a tequitl
task-graph server. The contract is the scheduler's own run-contract ADR 0010, tracked in
its repository: MCP access rides `--mcp-server <name>=<url>` plus a `MCP_<NAME>_TOKEN`
environment variable per server (issue #341; part of the dark-factory epic, pairing with
the scheduler-side halves of that contract).

Three gaps blocked that contract:

1. The `--mcp-server` flag and the `MCP_<NAME>_TOKEN` bearer convention were wired ONLY
   in `cmd/mecated/main.go` (a private `mcpServerList` flag.Value). mecatequi and
   mecak8s — the factory-facing binaries — registered no MCP flag at all, even though
   `internal/app/build.go` (`MCPServers`) already carries the entries to the
   static MCP source.
2. mecatequi's summary emit (`cmd/mecatequi/main.go` (`emitSummary`)) wrote INDENTED
   JSON, so a scheduler tailing pod logs could not parse "the last stdout line" as the
   run result.
3. Nothing documented the invocation profile a scheduler should use (posture, fencing,
   timeout, outputs), nor whether mecatequi accepts run-correlation flags
   (`--run-id`/`--task-ref`).

## Decision

1. **Extract the MCP flag into `internal/cliconfig` and register it on all three
   mains.** `cliconfig.MCPServerList` + `RegisterMCPServerFlag` are the ONE
   registration path for `--mcp-server`, shared by the three mains, (repeatable `name=URL`, split at the first `=`,
   malformed values rejected at parse) and the ONE home of the `MCP_<NAME>_TOKEN`
   convention (name upper-cased; a present token becomes
   `Authorization: Bearer <token>` on that server's `Headers`; a missing token leaves
   `Headers` nil — token optional). mecated switches to the helper; mecatequi and
   mecak8s register it and thread `Servers()` onto `app.Config.MCPServers`. This
   follows the established cliconfig discipline (provider keys, model flags):
   cmd-side env reads live in the shared helper so the mains cannot drift.

   The helper adds two hardenings the security review asked for, and they
   deliberately TIGHTEN mecated's original behavior too:

   - **Name validation + case-insensitive uniqueness (CWE-178).** A server name
     must match `[A-Za-z0-9_]+`, and two entries whose upper-cased names collide
     are rejected: `vmcp`/`VMCP`/`vMcp` all derive `MCP_VMCP_TOKEN` (Unicode
     case-folds like ı→I are excluded by the charset), and a hyphenated name
     like `my-svc` would derive the unsettable `MCP_MY-SVC_TOKEN` and silently
     connect unauthenticated. mecated previously accepted all of these.
   - **No cleartext bearer off-host (CWE-319).** When a token IS attached, the
     URL must be `https` — or `http` to an explicit loopback host (reusing
     `internal/adapter/mcp/mcp.go` (`ValidateClientURL`) / its loopback
     allowlist) — so a scheduler-injected token is never sent in cleartext off
     the host. A tokenless URL is not gated (unchanged).
2. **An EXPLICIT `--out-summary=-` selects a stdout-compact summary mode on
   mecatequi.** The Summary is emitted as a SINGLE compact JSON line as the FINAL
   stdout line — nothing follows it — so a scheduler can parse the last line of the
   pod log. The unset default (which also resolves to `-`) and an explicit file path
   keep the indented JSON: default behavior is unchanged, and the mode is a deliberate
   opt-in by the party that writes the invocation.
3. **`--run-id` / `--task-ref` are deliberately NOT accepted.** mecatequi stays
   forge- and scheduler-agnostic (ADR 0028): a scheduler correlates a run via its own
   launch identity (Job name, pod labels) plus the `Summary.session_id` it already
   emits. Passing scheduler bookkeeping through the binary would add a second
   correlation channel with no consumer inside the harness.

## Consequences

- A scheduler-launched run reaches vmcp/tequitl with a per-run bearer using only flags +
  env the scheduler controls; no engine or proto change, `engine/api/*.txt` untouched.
- The three mains share one parse/token path; a future change to the convention (e.g.
  a token file) lands once in `internal/cliconfig/mcpserver.go`.
- **The "per-run identity" framing holds for mecatequi only.** On mecak8s (a
  long-lived daemon) `MCP_<NAME>_TOKEN` is read ONCE at process startup and the
  resulting bearer is shared process-wide — across every session and every
  reconnect — for the life of the pod. A scheduler cannot rotate it per work item
  without restarting the pod. A per-session credential source (so a daemon can
  present a fresh, short-lived identity per run) is future work, tracked as
  mecatl#342.
- The hardenings above are a (small) behavior BREAK for existing mecated
  deployments that used hyphenated/dotted server names or sent a bearer over
  plaintext http off-host; both shapes now fail at flag parse with an actionable
  message. That is the intended trade — each was silently insecure.
- The compact mode keys off "flag explicitly set" (`fs.Visit`), so `--out-summary=-`
  behaves differently from the identical unset default. That subtlety is the cost of
  keeping the human-facing default (indented, jq-able) byte-identical; it is documented
  in the flag help and `docs/usage/mecatequi-ci.md`.
- Schedulers get NO in-band run-id echo; anyone needing correlation richer than
  `session_id` must carry it in their own launch metadata.
- mecatequi/mecak8s deliberately do NOT gain `--mcp-resource-tools` / `--mcp-prompts` /
  `--toolhive` here; those stay mecated-only until a factory consumer needs them
  (`app.Config` defaults them off for the new mains).

## See also

- Issue [#341](https://github.com/stacklok/mecatl/issues/341); the scheduler's
  run-contract ADR 0010 and its paired issues, tracked in the scheduler's own
  repository — the scheduler half of this contract.
- [ADR 0028 — mecatequi](./0028-mecatequi.md) (the forge-agnostic single-shot runner),
  [ADR 0048 — mecak8s](./0048-mecak8s.md) (the k8s-native peer).
- `docs/usage/mecatequi-ci.md` ("The factory invocation profile") and
  `docs/usage/mecak8s.md` — the living operator docs for these flags.
