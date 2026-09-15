# ADR 0346 — Studio's chat list is the daemon's session store

- Status: Accepted
- Date: 2026-08-18
- Scope: `studio/` — where a chat's existence, title, and transcript live, and which
  client actions are server calls

> History: re-lands the unmerged draft from PR #615 (numbered 0227 there before
> that id was taken on main), adapted to the Atrium workspace UI.

## Context

The prototype Studio descended from kept its chat list in browser state — first
a `localStorage` key, then mock fixtures mirrored in module scope. `mecated`
persisted the same sessions the whole time: the daemon already exposed
`GET /v1/sessions`, `GET /v1/sessions/{id}/transcript`,
`POST /v1/sessions/{id}/rename`, and `POST /v1/sessions/{id}/delete` — and the
client called none of them.

Every consequence followed from that one gap:

- A second browser, or a second machine, saw nothing. The daemon held the work;
  the client that opened it held the only record of what the work was called.
- Renaming a chat renamed a local label that no other client would ever see.
- Deleting a chat dropped the client's copy and left the session in the store
  forever — "delete" quietly meant "hide".
- A reload during a run orphaned it. `mecated` keeps running after the page
  that started it goes away, but the client had no way back to the result: the
  run streams off the `POST /prompt` response body, and that body is gone.
- The client also minted its own session ids and mapped them lazily onto daemon
  sessions, so the daemon's record and the sidebar disagreed about what even
  existed.

The persistence was never missing. It was unused.

## Decision

The daemon's session store is the record of which chats exist, what each is
called, and what was said in each. Studio reads and writes that record, and
**the sidebar id IS the daemon session id** — there is no client-side session
mapping.

- **The chat list is the session inventory.** Studio walks `GET /v1/sessions`
  when the daemon becomes reachable and on a slow poll, and merges the result
  into its list. A row is removed only when a COMPLETE walk proves it gone —
  a partial walk updates what it saw and never deletes. Rows whose one
  not-a-chat reason is `inspect_only_kind` (subagents, team members, scheduled
  fires) are filtered by the decoder, not by id-prefix guessing.
- **Rename is `POST …/rename`.** The local update is optimistic and rolls back
  if the daemon refuses. Studio adopts the title the daemon actually stored,
  which is clamped, rather than the one it asked for.
- **Delete is `POST …/delete`.** A refusal keeps the row: the chat still
  exists, and hiding it locally is the exact failure this ADR is about. Only a
  404 — the daemon saying it has no such session — removes a row without a
  witnessed walk.
- **Opening a chat reads `GET …/transcript`.** The authoritative message-level
  snapshot, which also covers scheduler-tick fires whose conversation never
  reached the durable event log, and works identically in external mode. A
  transcript the daemon cannot prove whole says so in the UI.
- **Eligibility is the daemon's, not Studio's.** Each inventory row carries
  `capabilities` plus a closed machine-readable reason per disabled action.
  Studio renders the reason; it does not re-derive who may rename or delete
  what, so tightening the rule server-side needs no client change.
- **A new chat is a draft.** No daemon session is created until the first
  prompt is sent; the mint happens then, and the route adopts the daemon's id.
  Empty sessions never accumulate in the store from idle "new chat" clicks.

## Consequences

A chat renamed or deleted on one machine is renamed or deleted on every
machine, and survives clearing the browser. Sessions can finally be removed
from the store through the UI. A reload during a run no longer loses it: the
inventory row shows the session still running on the daemon, and the
transcript is read back when it ends.

The costs, stated plainly:

- **The list reorders on rename.** A rename advances the daemon's stored
  mtime, and that mtime is the only ordering every client can agree on.
- **A rehydrated transcript is not identical to the live one.** The transcript
  endpoint returns the model's conversation, which includes the harness's own
  synthetic continuations as user-role messages with no provenance to
  distinguish them. Fixing this needs a provenance field on the daemon's
  `ConversationMessage`, not string-sniffing in the client.
- **Reattaching to a LIVE run is still not possible.** Studio can see a run in
  flight and read its transcript once it ends, but it cannot resume the
  stream: `GET /v1/sessions/{id}/events` is a finite replay, and the live
  per-session subscription is reachable only over gRPC `StreamSessionLive`.
  Closing that gap is a daemon change, not a client one.
- **Polling.** The list reconciles on an interval rather than a push, so a
  change made elsewhere appears within seconds, not instantly.

## See also

- [ADR 0345](./0345-studio-atrium-module.md) — the Studio module and its
  daemon-only posture
