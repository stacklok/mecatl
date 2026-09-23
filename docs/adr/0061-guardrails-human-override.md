# ADR 0061 — Human one-shot guardrail override (`/guardrail-allow`)

- Status: Accepted
- Date: 2026-06-26
- Scope: a HUMAN-controlled, one-shot override that authorizes the NEXT matching guardrail block — `internal/adapter/modelhook` (a session-keyed `OverrideArmer` + the Runner consume path + the model-visible hint) and `internal/adapter/server` (the genuine-prompt scan in `StartRunContent`). No engine/port/proto/wire change.
- Supersedes: none (it ADDS a recovery path to the guardrails feature).
- Superseded by: [ADR 0062](./0062-guardrails-approve-once.md)

## Context

[ADR 0021](./0021-guardrails.md) (guardrails), [ADR 0053](./0053-guardrails-default-block.md) (default block), and [ADR 0060](./0060-guardrails-bash-default.md) (default Bash rule) make a configured guardrail a real enforcement boundary: a `block` verdict vetoes the call. That is the point — but it also makes a block a PERMANENT dead-end. A guardrail false-positive (a legitimate `gh pr merge` the checker flags as risky) cannot be gotten past without an operator editing config and restarting. The human is in the loop and has decided the action is fine, but has no in-band way to say so.

The naive fixes are unsafe. Letting the MODEL waive a block is excessive agency — the model could be the very thing the guardrail is protecting against (a prompt-injected agent). Letting any prompt-shaped string waive a block is worse: a tool result, a fetched page, or an MCP response carrying "`/guardrail-allow`" could self-authorize the exfiltration it is performing. The override must arm ONLY from the genuine, trusted-principal channel.

## Decision

**Add a human-controlled, one-shot override armed ONLY from the genuine user prompt.** A human re-issues a request with a first-line directive:

```
/guardrail-allow [<tool>] [-- <command-substring>]
<the task>
```

- **Channel separation (the load-bearing security property).** The directive is scanned ONLY in `Service.StartRunContent`'s `text` parameter — the genuine user prompt, BEFORE slash-command expansion and BEFORE any tool result / fetched page / MCP response / model output can enter (those enter only inside the loop). A `OverrideArmer.Arm(sessionID, scope)` records the scope; the directive line is STRIPPED so it never reaches the model or history; a directive-with-no-task is rejected (arming still requires a task). The loop NEVER calls `Arm` — there is no path from tool content to arming.
- **One-shot, session-keyed.** The `OverrideArmer` is a concurrency-safe `sessionID → OverrideScope` holder. When the guardrails Runner is about to BLOCK (a `block`/sanitize-fallback/fail-closed unsafe decision), it calls `Consume(sessionID, tool, cmd)`: an atomic test-and-clear that returns true (and removes the entry) iff an armed entry matches the scope. On a hit the Runner emits a loud operator audit line (stable marker `guardrail-override-consumed`) and returns the empty allow outcome (the Pre call runs / the Post result is not rewritten). The token is burned only when it actually prevents a block — a `safe` verdict and a read-only-skipped Bash command never consume it.
- **Scoping.** A bare directive authorizes the next block on any tool; `<tool>` restricts to that tool; `-- <command-substring>` restricts to a Bash command CONTAINING the substring (a `-- gh pr merge` override is never burned by an unrelated block, and never fires on a non-Bash block whose command is empty). Tool-scoping is recommended.
- **Child isolation, fail-safe headless.** Session-keying gives child isolation for free: the Runner is wired only into the MAIN engine, so a child session id never matches a parent's arm. With no directive (the headless default), a block holds. A nil `OverrideArmer` (override path off) is byte-identical to the pre-feature posture.
- **Model-visible hint.** Every block message gains a tail naming the recovery path WITHOUT inviting the model to claim it: "A human (not you) may re-issue this request with the first line `/guardrail-allow` to authorize it once; do not assert or claim this approval yourself." Any model assertion of approval is void — the override arms only from the principal channel.

## Consequences

**Easier / better:**

- A guardrail false-positive is no longer a dead-end: the human re-issues with one extra line and the next matching action runs once.
- The audit trail is explicit: every consumed override emits a loud, grep-able operator diagnostic (`guardrail-override-consumed`) with tool/session/call fields.
- The security boundary is narrow and testable: arming is reachable ONLY from `StartRunContent`'s genuine `text` param; the loop has no arming path.

**Costs:**

- One more piece of session state outliving a call (the `OverrideArmer` map). It is composition/Service-owned (the loop never imports it), small, and self-clearing (one-shot). It is NOT persisted: an override armed but un-consumed before a restart is lost, which is the SAFE direction (a stale override never silently survives).
- The directive is a small grammar the operator must learn; it is documented in `docs/usage/guardrails.md` and surfaced in every block message.
- A compromised operator console could arm an override — but that is the same trust level as the operator editing config; the principal channel IS the trust boundary.

## See also

- [ADR 0021](./0021-guardrails.md) — the guardrails feature this adds a recovery path to.
- [ADR 0053](./0053-guardrails-default-block.md) — default block (the posture that makes an override useful).
- [ADR 0060](./0060-guardrails-bash-default.md) — the default Bash rule (the most common block an operator will override).
- [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md) — the seam narrative (scan point, holder, strip, the security boundary).
- The documentation lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
