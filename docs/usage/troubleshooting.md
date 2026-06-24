## 12. Troubleshooting / FAQ

**`no LLM provider available: set one of ANTHROPIC_API_KEY (Claude), OPENAI_API_KEY (OpenAI), or OPENROUTER_API_KEY (one key, many models — a good first choice) …`**
You started `mecated` with no provider key in the environment. Set
`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or `OPENROUTER_API_KEY`; for a
compatible/proxy endpoint pass the matching key plus `--openai-base-url` /
`--anthropic-base-url` / `--openrouter-base-url`; or pass `--mock` for an
offline smoke test. (The full message is quoted in §3, "Provider selection".)

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

**Reading the JSONL replay log**
With `--store-dir DIR` (for `mecatui`, the per-workspace default under
`$XDG_STATE_HOME/mecatui/sessions/<path-slug>/`), inspect a session after the fact
— note these files hold the **raw conversation in plaintext**:

For `mecatui` you do not pass `DIR` — find your per-workspace store under the
default base and pick the subdir matching your workspace (its name is the
workspace path with `/` replaced by `-`):

```console
$ ls ~/.local/state/mecatui/sessions/   # each subdir is one workspace
-var-home-ozz-dev-mecatl   -home-ozz-scratch
```

```console
$ ls DIR
8867….session.jsonl   8867….tools.jsonl   8867….events.jsonl

# Latest session snapshot (last line wins):
$ tail -n1 DIR/8867….session.jsonl | jq .

# Every tool call with its result and duration:
$ jq . DIR/8867….tools.jsonl

# The relayed event timeline (reasoning, ask/verdict pairs, delegation lifecycle):
$ jq . DIR/8867….events.jsonl
```

