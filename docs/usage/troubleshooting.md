## 17. Troubleshooting / FAQ

### MCP OAuth login required

A diagnostic naming `MCP OAuth login required` intentionally omits resource, issuer,
authorization URL, secret references, and adapter errors. For a mutable local profile, run
the exact remedy it prints: `mecated mcp login <server>` (add `--no-browser` only when
you want the authorization URL on stdout). For an environment-backed profile, preprovision
the opaque credential and restart the process; the environment Reader is immutable and the
login command will reject it. `invalid_grant` after a prior login usually means the refresh
token was revoked/consumed or the credential identity changed (profile, principal, client,
scopes, or resource): stop serving that profile, rerun `mecated mcp login <server>`, verify a
warm startup, then resume traffic. If rollout fails, restore the previous whole profile
(`static_bearer` where supported, or `none`) and restart; never enable browser behavior in a
daemon to repair it. A persistent failure may instead be an upstream SDK metadata-profile
incompatibility. ACP cannot provide OAuth profiles or install/drive authorization; after operator
authorization it may invoke the shared global OAuth-backed tools under ordinary permissions. There
is no per-session ACP OAuth or DCR fallback.

**`no LLM provider available: set one of ANTHROPIC_API_KEY (Claude), OPENAI_API_KEY (OpenAI), or OPENROUTER_API_KEY (one key, many models — a good first choice) …`**
You started `mecated` with no provider key in the environment. Set
`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or `OPENROUTER_API_KEY`; for a
compatible/proxy endpoint pass the matching key plus `--openai-base-url` /
`--anthropic-base-url` / `--openrouter-base-url`; or pass `--mock` for an
offline smoke test. (The full message is quoted in §3, "Provider selection".)

### OpenAI Codex manual-token and entitlement failures

These failures belong to experimental provider `openai-codex`, not public API-key
provider `openai`. Never paste the access token into an issue, log, command line,
URL, or troubleshooting output.

| Symptom | Meaning | Action |
| --- | --- | --- |
| Whole-file `auth file ... does not match the expected schema` warning | YAML decoding failed, the root/`providers` structure is invalid, a provider mapping key is duplicated, or the file contains a second YAML document. The entire file is ignored; credentials already supplied through API-key environment variables remain usable. | Compare with the [exact schema](mecated.md#openai-codex-subscription-manual-token-experimental), fix the file, keep mode `0600`, and restart. The warning intentionally never echoes values. |
| `unknown provider(s) ignored` or `provider credential entry/entries ignored` | An unknown provider is ignored, or a known provider entry has a schema-invalid mapping, non-string/duplicate/unknown field, or malformed OAuth shape. The affected provider entry is dropped; valid sibling entries survive. | Fix the named class of entry without disclosing its values, then restart. |
| `provider credential entry/entries contained ignored fields` | The entry parsed structurally, but a field is semantically invalid for that provider: for example, OAuth under an API-key provider, an API key under `openai-codex`, or empty Codex OAuth. Only the offending field is removed; other valid fields in the same entry and valid sibling entries survive. | Fix or remove the mis-scoped field, then restart. |
| `manual access token expired` | The immutable snapshot is already expired, or expired after startup. The request is refused before network I/O. | Replace `access_token` (and matching optional claims) in `auth.yaml`, then restart. There is no automatic refresh. |
| `manual access token was rejected (HTTP 401/403)` / picker says `manual token rejected` | The private backend rejected authentication/account headers, or stopped accepting the honest `originator: mecatl`. | Replace the token and restart. If a current token still fails, treat the experimental backend as incompatible; do not disguise mecatl as the Codex CLI. |
| The account lists models, but an expected model is absent | The live entitlement response is authoritative for this ChatGPT account. Public OpenAI catalog entries are not subscription entitlements. | Check the selected ChatGPT account/subscription. Choose a model that `/models` actually lists; an explicit unknown slug may fail on first inference. |
| Picker says `account lists no selectable models` / startup says `no picker-visible models` | Authentication succeeded but the account returned an empty selectable inventory. This is different from malformed/expired/unauthorized credentials. | Check the subscription/account and replace the token if it belongs to the wrong account. There is no public-catalog fallback. |
| HTTP 429 / `rate_limit_exceeded` / quota error | The backend is rate- or quota-limiting the request. It is not an auth-file parse failure. Safe pre-commit requests use the bounded shared retry policy; exhaustion remains visible. | Wait and retry, reduce concurrency, or resolve account quota. Replacing the token is not the default remedy unless it selects the wrong account. |
| `unreachable`, timeout, HTTP 5xx, or retry exhaustion | DNS/TLS/network or a transient private-service failure. Before the first successful model list, Codex contributes no inventory; after success, process-local last-known-good rows may remain visible while current inference still fails. | Check connectivity to `chatgpt.com`, wait, and retry. Restart is required only if you changed the credential; a stale visible model list is not proof inference is healthy. |

The backend is undocumented and may change or revoke third-party compatibility.
See [ADR 0215](../adr/0215-openai-subscription-manual-token.md) for that boundary.

**`--openai requires OPENAI_API_KEY to be set`** (the `cmd/mecademo` demo only)
The demo's `--openai` flag was passed but no key is in the environment.
`export OPENAI_API_KEY=…`. (`mecated`/`mecatui` instead auto-detect the provider
from the environment and emit the `no LLM provider available: …` message above when
no key resolves.)

**`bind: address already in use`**
Another `mecated` (or process) holds the port. Pick free ports with
`--http-addr` / `--grpc-addr`, or stop the other process.

**WARN: "API bound to a NON-loopback address …"**
You bound something other than `127.0.0.1` / `localhost`. The API is
unauthenticated and exposes command/file execution. Bind loopback, or put a real
trust boundary (auth/mTLS proxy, network policy) in front of it.

**Edit fails: "you must have read the file with the Read tool this session …"**
`Edit` enforces read-before-edit: the file must have been read this session and
be unchanged since. Have the model `Read` the file (again) and retry the edit.

**Tool call hangs / never completes**
It is probably a `permission.ask` awaiting approval. Watch the SSE/event stream
for `permission.ask` and resolve it with `POST …/approve` (or a `ResumeApproval`
frame over gRPC). Denied calls return the reason to the model.

**The run "won't stop" / loops**
It can't run unbounded: default limits cap it (`max_turns=2000`,
`max_tool_calls=8000`, `max_consecutive_failures=5`). The terminal `result.stop`
tells you which limit fired (`max_turns`, `max_tool_calls`,
`max_consecutive_failures`). Tighten them per session via `limits`.

**`404 no in-flight run for session`** on approve/cancel
There is no active run for that session id — the run already finished, was never
started, or you used the wrong id. Approve/cancel only work while the prompt's
SSE stream is open.

**"the provider's content filter blocked this response (reason: content_filter)"**
This is an upstream moderation decision, not a mecatl error or a transient bug —
some routes (e.g. an Azure OpenAI upstream) apply aggressive moderation to benign
security/credentials wording. It is a terminal `StopError`, never retried by
design (retrying the identical input against the same filter just reproduces the
same block). Retype or resend the message that triggered it; if it keeps
happening on legitimate input, that's a provider-side moderation tuning
conversation, not something to file against mecatl.

**Reading the JSONL replay log**
With `--store-dir DIR` (for `mecatui`, the per-workspace default under
`$XDG_STATE_HOME/mecatui/sessions/<path-slug>/`), inspect a session after the fact
— note these files hold the **raw conversation in plaintext**:

For `mecatui` you do not pass `DIR` — find your per-workspace store under the
default base and pick the subdir matching your workspace (its name is the
workspace path with `/` replaced by `-`):

```console
$ ls ~/.local/state/mecatui/sessions/   # each subdir is one workspace
-home-me-dev-mecatl   -home-me-scratch
```

```console
$ ls DIR/sid-v1
sid-v1-….session.jsonl   sid-v1-….tools.jsonl   sid-v1-….events.jsonl

# Physical tokens are not logical session ids. Find the embedded valid-UTF-8 ids:
$ for f in DIR/sid-v1/*.session.jsonl; do printf '%s  ' "$f"; tail -n1 "$f" | jq -r .id; done
DIR/sid-v1/sid-v1-….session.jsonl  session/logical-id

# Select that snapshot and derive its sidecar stem:
$ SNAPSHOT=DIR/sid-v1/sid-v1-….session.jsonl
$ FAMILY=${SNAPSHOT%.session.jsonl}

# Latest session snapshot (last line wins):
$ tail -n1 "$SNAPSHOT" | jq .

# Every tool call with its result and duration:
$ jq . "$FAMILY.tools.jsonl"

# The relayed event timeline (reasoning, ask/verdict pairs, delegation lifecycle):
$ jq . "$FAMILY.events.jsonl"
```

A pre-`sid-v1/` store may also have legacy `*.session.jsonl` families directly
under `DIR`. Do not infer their logical IDs from those lossy filenames; inspect
the latest snapshot `id`. The adapter reads or migrates a legacy family only
when that embedded id exactly matches the request.

**`✗ … — retrying won't help; the request is rejected.` (permanent provider error)**
The provider returned a **permanent** rejection — a 4xx status other than 408/429, a
context-window overflow, or a content-policy block. Replaying the identical request
cannot succeed. Start a new session (a new `CreateSession`, or restart the mecatui
client — `/clear` only wipes the local transcript and the next prompt re-enters the
SAME poisoned server session), change your prompt to stay within the budget or avoid
the blocked content, or fix the credential/permission on the provider side. A
transient failure (5xx, rate limit, or an unknown error) shows the usual error block
instead — those may succeed on retry.

---

See also: the [operator guide index](../usage.md).
