# ADR 0227 — Studio's chat list is the daemon's session store

- Status: Accepted
- Date: 2026-08-18
- Scope: `studio/` — where a chat's existence, title, and transcript live, and which
  client actions are server calls

## Context

Mecatl Studio kept its entire chat history in one browser key,
`mecatl-studio-tasks`. `mecated` persisted the same sessions the whole time — the
daemon Studio talks to already exposed `GET /v1/sessions`,
`GET /v1/sessions/{id}/transcript`, `POST /v1/sessions/{id}/rename`, and
`POST /v1/sessions/{id}/delete` — and Studio called none of them.

Every consequence followed from that one gap:

- A second browser, or a second machine, saw nothing. The daemon held the work;
  the client that opened it held the only record of what the work was called.
- Renaming a chat renamed a local label. Reload the page from a different
  browser profile and the old title was back, because the rename never left the
  tab.
- Deleting a chat dropped Studio's copy and left the session in the store for
  ever, with no way to remove it — the daemon has no client-driven GC, so
  "delete" quietly meant "hide".
- A reload during a run orphaned it. `mecated` keeps running after the page that
  started it goes away, but the client had no way back to the result: the run is
  streamed off the `POST /prompt` response body, and that body is gone.
- The interruption notice told the operator the task "was interrupted", which
  was not true. The daemon had very likely finished it.

The persistence was never missing. It was unused.

## Decision

The daemon's session store is the record of which chats exist, what each is
called, and what was said in each. Studio reads and writes that record:

- **The chat list is the session inventory.** Studio walks `GET /v1/sessions`
  when the daemon becomes reachable and on a slow poll, and merges the result
  into its list. A row the walk did not return is removed only once that session
  has first been observed present in a COMPLETE walk — absence is proof of
  deletion only for something previously proven to exist.
- **Rename is `POST …/rename`.** The local update is optimistic and is rolled
  back if the daemon refuses. Studio adopts the title the daemon actually
  stored, which is clamped, rather than the one it asked for.
- **Delete is `POST …/delete`.** It removes the snapshot and its sidecars. A
  refusal keeps the row: the chat still exists, and hiding it locally is the
  exact failure this ADR is about.
- **Opening a chat reads `GET …/transcript`.** A row from the inventory carries
  no conversation, and a cached one is re-read once the daemon reports the
  session advanced without us.
- **Eligibility is the daemon's, not Studio's.** Each inventory row carries
  `capabilities` plus a closed machine-readable reason per disabled action.
  Studio renders the reason; it does not re-derive who may rename or delete
  what, so tightening the rule server-side needs no client change.
- **A session survives a failed turn.** Only a 404 — the daemon saying it has no
  such session — detaches a chat from its session id. Every other failure leaves
  it attached, because `mecated` recovers a completed, cancelled, or failed
  session at its run entry rather than needing a fresh one.

`localStorage` stays, demoted to a cache: it paints the list on a cold start and
holds unsent drafts, which exist nowhere else. It is no longer the record.

## Consequences

A chat renamed or deleted on one machine is renamed or deleted on every machine,
and survives clearing the browser. Sessions can finally be removed from the
store through the UI. A reload during a run no longer loses it: the chat is
marked as still working on the daemon, and its transcript is read back once the
run finishes.

The costs, stated plainly:

- **The list reorders on rename.** A rename is a write, so the daemon's stored
  mtime advances, and that mtime is the only ordering every client can agree on.
  Studio previously pinned a renamed row in place; it now lets it rise, because
  pinning locally would just disagree with the next browser to open.
- **A rehydrated transcript is not identical to the live one.** The transcript
  endpoint returns the model's conversation, which includes the harness's own
  synthetic continuations — the no-progress nudge, background-completion notices
  — as user-role messages with no provenance to distinguish them. Reopening a
  chat therefore shows text the operator never typed. Fixing this needs a
  provenance field on the daemon's `ConversationMessage`, not string-sniffing in
  the client.
- **Reattaching to a LIVE run is still not possible.** Studio can see that a run
  is in flight and can read the transcript once it ends, but it cannot resume
  the stream. `GET /v1/sessions/{id}/events` is a finite replay of the durable
  log, and the live per-session subscription (`Service.Subscribe`) is reachable
  only over gRPC `StreamSessionLive`, which the ordinary run relay does not
  publish to. Closing that gap is a daemon change, not a client one.
- **Polling.** The list reconciles on an interval rather than a push, so a change
  made elsewhere appears within seconds, not instantly.
- **A store that cannot page session metadata answers 501.** Studio stops asking
  and runs on its local cache, which is the pre-ADR behaviour.

## See also

- [ADR 0225](./0225-studio-module.md) — Studio as an in-repo module consuming
  only the public HTTP/SSE API.
- [ADR 0027](./0027-cloud-native.md) — the session store, the durable event log,
  and the run-entry recovery seams this relies on.
- `studio/CLAUDE.md` — the module's living contract, where these rules are
  restated as invariants for anyone changing the client.
