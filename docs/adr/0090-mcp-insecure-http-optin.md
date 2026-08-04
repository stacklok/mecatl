# ADR 0090 — Per-server operator opt-in for plain-http token-bearing MCP endpoints

- Status: Accepted
- Date: 2026-08-04
- Scope: `internal/cliconfig` + the three real-provider mains' `--mcp-server` surface (cmd/composition layer only; no `engine/` change)

## Context

ADR 0082 (issue #347, CWE-319) made the shared `--mcp-server` helper reject any
token-bearing URL that is not `https` — or `http` to an explicit loopback host — so a
scheduler-injected `MCP_<NAME>_TOKEN` bearer is never sent cleartext off-host. That is
the correct default, and it stays the default.

But the in-cluster scheduler (titlani, the kubernetes arm of the run contract —
titlani#40, pairing with issue #358 the way #341 paired with titlani#38) points agent
runs at plain-http MCP Services inside a NetworkPolicy-scoped namespace, where the same
short-TTL bearer already crosses the identical wire from the scheduler's own client.
Under the #347 gate those runs cannot start at all.

Two alternatives were considered and rejected:

- **A pod-local proxy sidecar** (TLS-terminating, so the agent sees https). Rejected:
  it satisfies the gate without changing wire security — the sidecar→service hop stays
  cleartext — while adding a container to every run pod. A gate you can satisfy without
  changing the property it checks is theater.
- **A global escape hatch** (`--mcp-allow-insecure` or similar). Rejected: it relaxes
  every server at once, so one legitimately-cleartext in-cluster endpoint would also
  waive the check for an internet-facing one added later.

One contract subtlety surfaced in titlani#40's devils-advocate pass: the #347 gate
fired inside `MCPServerList.Set` — at flag-parse time, in argv order — so any
relaxation flag parsed *after* its `--mcp-server` would arrive too late. Argv ordering
must not be part of the caller's contract.

## Decision

1. **Add `--mcp-server-insecure-http <name>` (repeatable) to the shared
   `internal/cliconfig` helper**, registered by the same `RegisterMCPServerFlag` call
   all three mains (mecated, mecatequi, mecak8s) already use — one registration path,
   one acknowledgment wording, no drift. The flag help states the acknowledgment
   plainly: the token travels **cleartext on the network path** to that server; the
   operator is relying on network-layer controls (NetworkPolicy / namespace trust)
   plus short-lived tokens as the mitigations.
2. **The relaxation is narrow by construction.** It relaxes ONLY the token-bearing
   scheme gate, ONLY for the named server (matched case-insensitively, the same
   `^[A-Za-z0-9_]+$` name grammar and uniqueness discipline as #347), ONLY for the
   `http` scheme. https validation and everything else are unchanged; other servers
   are unchanged; the token stays env-only (`MCP_<NAME>_TOKEN`, never a flag value).
3. **A stale acknowledgment is loud, never silently inert.** A relaxation naming a
   server with no matching `--mcp-server` registration is a validation error. So is a
   relaxation naming a server whose URL is https, loopback http, or a non-http scheme:
   the acknowledgment then contradicts the URL's real shape (e.g. the endpoint moved
   to https and the operator forgot to drop the flag), and letting it sit inert would
   leave the flag list lying about the deployment's wire posture. A relaxation naming
   a *tokenless* plain-http off-host server is accepted — that acknowledgment matches
   the URL shape; there is simply no bearer to protect yet.
4. **The scheme gate moves out of `Set` into a post-parse `Finalize` step** —
   order-independence by construction. `MCPServerList.Set` only collects entries (the
   entry-local #347 checks — name charset, env-name collision, `name=URL` shape — stay
   inline); the three mains call `MCPServerList.Finalize()` immediately after
   `flag.Parse`, which first resolves every relaxation against the collected servers
   and then runs the CWE-319 gate over the un-relaxed token-bearing entries. Both argv
   orders are pinned in tests. `Servers()` fails closed (panics) before a successful
   `Finalize`, so a future main cannot hand un-gated configs to `app.Build` by
   forgetting the call.

## Consequences

- A titlani-launched in-cluster run can name its plain-http MCP Services explicitly,
  per server, and the invocation itself documents the accepted cleartext hop — the
  security review reads the flag list, not the cluster topology.
- The default posture is byte-identical: a token-bearing http non-loopback URL without
  the relaxation still fails (the existing #347 pins keep passing), and there is no
  global escape hatch to misuse.
- The gate now fires at `Finalize` instead of `Set`, so a bad combination is reported
  after the whole argv is parsed rather than at the offending flag. The error message
  still quotes the offending `name=URL` value, names the token env var, and names the
  opt-in — the operator experience is one actionable error either way. Every future
  main must remember the `Finalize` call; the `Servers()` panic converts that mistake
  from a silent security regression into an immediate test failure.
- The relaxation's stale-acknowledgment strictness means an endpoint migration
  (http→https) is a two-step flag change: flip the URL AND drop the relaxation in the
  same edit, or startup fails loudly. That friction is deliberate — it keeps the
  acknowledgment inventory honest.
- On mecak8s the ADR-0082 caveat still applies unchanged: the token is read once at
  process startup and shared for the pod's lifetime; per-run identity remains a
  mecatequi property (mecatl#342).

## See also

- Issue [#358](https://github.com/stacklok/mecatl/issues/358); titlani#40 — the paired
  kubernetes scheduler half (source of the order-independence requirement).
- [ADR 0082 — Factory MCP wiring for the one-shot mains](./0082-factory-mcp-wiring.md)
  — the shared `--mcp-server` helper + the #347 hardenings this opt-in punches a named,
  per-server hole through.
- `docs/usage/mecatequi-ci.md` ("The factory invocation profile"),
  `docs/usage/mecak8s.md`, `docs/usage/mecated.md` — the living operator docs for the
  flag surface.
