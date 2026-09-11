# ADR 0327 — MCP client: automatic detection of a GET-hostile standalone SSE stream

- Status: Accepted
- Date: 2026-08-06
- Scope: `internal/adapter/mcp`
- Supersedes: none
- Superseded by: none
- Amends: [ADR 0326](./0326-mcp-standalone-sse-opt-out.md) (adds an automatic
  layer alongside the manual opt-out; the manual flag is unchanged)

## Context

ADR 0326 added `ServerConfig.DisableNotifications` /
`MCP_<NAME>_DISABLE_NOTIFICATIONS=true`: a per-server opt-out of the
standalone SSE GET stream for gateways that accept the GET and then close it
immediately while POST traffic stays healthy. The pinned go-sdk's
`handleSSE` retry loop can't distinguish that from a flaky stream — it
retries, exhausts its budget, and calls `c.fail()`, which poisons the shared
connection's POST channel too. ADR 0056's reconnect heals the resulting drop
transparently, but pays a fresh `initialize` (a new `Mcp-Session-Id`, lost
server-side session state), and against a still-hostile gateway the kill →
reconnect → kill cycle repeats forever.

The manual opt-out only helps an operator who already knows their gateway
behaves this way. ADR 0326 named the gap directly: *"A future upstream fix...
would make this knob a no-op-cost default; it stays as the explicit
opt-out."* Nobody configures a flag for a failure they haven't seen yet — the
first encounter with a GET-hostile gateway still pays the full churn cost
before an operator can even discover the flag exists.

Two facts from the pinned SDK (`github.com/modelcontextprotocol/go-sdk@v1.6.1`)
shape what's possible here:

- `DisableStandaloneSSE` is read **only** at `StreamableClientTransport.Connect`
  time (`streamable.go:1613,1631`) — there is no live setter. Flipping it
  requires a fresh dial. mecatl's `Server.dial` (`internal/adapter/mcp/mcp.go`)
  is already that single construction site, reused by both the initial
  `Connect` and every ADR 0056 reconnect.
- The SDK exposes no hook that distinguishes "the standalone GET failed" from
  "POST failed" — both surface as the same connection-wide `c.fail()`. But
  mecatl already owns the `*http.Client`/`Transport` per server
  (used today to inject auth headers), and the standalone GET is uniquely
  identifiable at that layer: `Method == GET` with
  `Accept: text/event-stream` (`streamable.go:2213`) — no other request this
  client makes carries that combination. Its response body is *always*
  closed by the SDK's `processStream` (`streamable.go:2077-2082`, a deferred
  close whether it read 0 events or many), giving a reliable, deterministic
  point to record "this GET delivered nothing."

## Decision

Add an always-on, per-server observer that detects GET-hostility itself, at
runtime, from outside the SDK — no operator action required.

1. **`sseHealthTracker`** (`internal/adapter/mcp/ssehealth.go`): one instance
   per `mcp.Server`, created once in `Connect` and never replaced across
   reconnects. It counts **consecutive hostile standalone-GET observations**.
   A 200 `text/event-stream` response counts only when it delivers zero bytes
   and closes within 5 seconds; a non-200 response, a response with a non-SSE
   content type, or a transport-level failure counts immediately. A byte
   delivered (a real event, or an ordinary SSE keepalive comment), or a valid
   SSE stream simply staying open past 5 seconds, resets the streak to zero,
   so a single blip on an otherwise-healthy gateway can never trip the
   verdict.
2. **Threshold: 3 consecutive hostile closes.** This is deliberately below
   the SDK's own retry budget (5 retries, i.e. 6 GETs, over an exponential
   1s→30s backoff): 3 lands within the SDK's first ~2 backoff rounds
   (roughly 1-3 seconds), well before its own `c.fail()` would fire. Three
   is chosen to rule out a single transient network blip (the false-positive
   risk any consecutive-failure heuristic carries) while not waiting out the
   SDK's full budget before reaching a verdict.
3. **Observation covers every standalone GET failure shape**, via
   `sseMonitorRoundTripper` installed on *every* server's
   `http.Client.Transport` (not only header-bearing ones — the existing
   conditional install was widened to unconditional):
   - a **valid SSE response** — wrapping the body (`sseObservingBody`) counts
     bytes read and elapsed time up to `Close`, then reports once to the
     tracker's `observe`; this detects the "200, then closed empty" shape;
   - a **completed invalid response** — a non-200 status or a response whose
     `Content-Type` is not `text/event-stream` reports immediately as hostile.
     Its body is deliberately not wrapped or closed by the monitor; the SDK
     retains its normal response/body lifecycle;
   - a **transport-level failure** — connection refused, TCP reset, EOF
     before any response ever arrives (`RoundTrip` itself returning a non-nil
     error) — reports immediately as hostile, excluding the caller's own
     context cancellation. This is the shape a gateway that hard-resets
     instead of soft-closing takes; missing it would mean the tracker never
     trips against exactly that kind of gateway, and every reconnect would
     reopen the doomed GET forever.
   These paths share ONE consecutive-hostility streak, not independent
   counters. They run entirely independent of, and ahead of, the SDK's own
   internal retry accounting — neither touches or shortens the SDK's retry
   loop (see Rejected alternatives).
4. **Action: `dial` ORs the tracker's verdict into the existing knob** —
   `DisableStandaloneSSE: s.cfg.DisableNotifications || s.sseHealth.Hostile()`.
   Because `dial` already runs on every reconnect, the very next dial that a
   drop *already* triggers picks up the verdict; no second reconnect path,
   no new goroutine, no polling.
5. **The verdict is sticky for the tracker's lifetime** (the process, for
   that `Server`) — never re-probed. Re-enabling the stream to check whether
   a gateway was fixed would mean deliberately re-paying a churn cycle to
   learn nothing, since gateway behavior does not change mid-process; an
   operator who redeploys a fixed gateway restarts mecatl anyway.
6. **The manual opt-out (ADR 0326) is unchanged and stays the outer layer.**
   Setting it to `true` skips the ~3-GET detection window entirely (zero
   standalone GETs) for an operator who already knows the gateway is hostile.
   It is not a tri-state override: `false` or an unset value leaves the
   automatic verdict in force once it has tripped; neither can force-enable
   the stream again before restart.
7. **Exactly one WARN on the false→true transition** (mirroring the existing
   discipline in `handleListChanged`): `"mcp: standalone SSE stream
   auto-disabled (GET-hostile server); list-changed notifications will no
   longer arrive"` with `server` and `consecutive_early_closes` fields — so
   an operator can tell "mecatl self-healed" from "notifications are
   silently gone."

### Rejected alternatives

- **Cutting the SDK's own retry storm short** (e.g. cancelling the
  connection's context as soon as the tracker trips, instead of letting the
  SDK exhaust its own budget and `reconnect` pick up the verdict on the next
  dial). This would save at most the remaining ~10-15 seconds of one round's
  churn, once, per hostile server, per process — at the cost of a second
  cancellation trigger racing the SDK's own `c.fail()`, for a one-time saving
  that isn't worth the added complexity.
- **Periodic re-probing** of a tripped server (e.g. retry the stream every N
  minutes in case the gateway was fixed). Rejected per point 5 above: no
  signal exists that a redeploy happened, so a re-probe is pure wasted churn
  most of the time, and mecatl restarting after a config change already
  clears the verdict for free.
- **Making auto-detection the ONLY mechanism** (retiring the manual flag).
  Rejected: the manual flag has zero detection-window cost for a known
  gateway and is the human override if the heuristic's 3-in-a-row threshold
  ever needs bypassing in either direction.

## Consequences

- **The first encounter with a GET-hostile gateway now self-resolves within
  seconds**, without an operator ever setting an environment variable.
  Before this ADR, `TestGetHostileGatewayReconnectsTransparently`
  characterized the churn as *repeating* on every reconnect against a
  still-hostile gateway ("the honest residual cost ADR 0326's opt-out exists
  to stop"); as of this ADR that repeat no longer happens automatically — the
  test now asserts the GET count plateaus instead of growing, which is this
  ADR's whole point, not a regression.
- **A false positive costs only list-changed notifications**, never
  correctness or security: the trade this heuristic can misjudge is the same
  one the manual flag already accepts deliberately (ADR 0326), just reached
  without a human choosing it in that instance. This bounded blast radius is
  what makes an automatic heuristic acceptable here at all — a heuristic
  that could misjudge something safety- or correctness-relevant would not
  meet this bar.
- **The automatic verdict does not survive a restart** (no new persisted or
  leased resource — see the ADR 0027 List 1 addendum on row 28). It is sticky
  for the current process lifetime: after it trips, setting the manual flag
  to `false` (or leaving it unset) cannot force-enable the stream. A restarted
  process starts with no automatic verdict and re-detects a still-hostile
  gateway within the same few-second window unless the manual `true` opt-out
  is set. This is an accepted, bounded, recurring cost — not a correctness
  gap.
- **The header-conditional transport wrap became unconditional**: every
  connected server, headers or not, now carries one extra RoundTripper
  wrapper. This is process-local, allocation-free on the hot POST path (only
  a standalone GET's body is ever wrapped), and does not change any
  observable request/response behavior for non-GET traffic.

## See also

- [ADR 0326](./0326-mcp-standalone-sse-opt-out.md) — the manual opt-out this
  ADR automates detection for; still the operator-facing override.
- [ADR 0057](./0057-mcp-server-notifications.md) — the standalone SSE stream
  this ADR (transitively) makes conditional.
- [ADR 0056](./0056-mcp-client-reconnect.md) — the reconnect-on-drop path
  whose next dial is what actually applies this ADR's verdict.
- `docs/adr/0027-cloud-native.md` List 1, row 28 addendum — why
  `sseHealthTracker` needs no new resource row.
- `user-docs/building/what-you-get/mcp-client.md` — the operator-facing documentation.
