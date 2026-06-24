## 2. The 60-second demo

`mecademo` drives a **real `agent.Engine`** through a scripted session against a
canned offline provider (`mockllm`) — no network, no key. It proves the full
shape of the loop: an auto-allowed tool call, a tool call that requires approval
(and is approved), and a final assistant message with usage accounting.

```console
$ go run ./cmd/mecademo
=== mecatl demo (offline / mockllm) ===
Driving a real agent.Engine: auto-allowed tool call -> permission ask + approval -> final result.

[001] turn=0 turn.start
[002] turn=0 message.delta  text="I'll read the greeting file first."
[003] turn=0 tool.call      tool=Read args={"path":"greeting.txt"}
[004] turn=0 tool.result    error=false result="     1\thello from the mecatl demo workspace"
[005] turn=1 turn.start
[006] turn=1 message.delta  text="Now I'll save a short note, which needs your approval."
[007] turn=1 permission.ask ASK tool=Write reason="approval required by rule for Write (note.txt)"  -> client auto-approves
[008] turn=1 tool.call      tool=Write args={"path":"note.txt","content":"reviewed the greeting\n"}
[009] turn=1 tool.result    error=false result="wrote \"note.txt\" (22 bytes)"
[010] turn=2 turn.start
[011] turn=2 message.delta  text="Done: I read greeting.txt and saved note.txt."
[012] turn=0 result         stop=end_turn text="Done: I read greeting.txt and saved note.txt."
      usage: in=4100 out=125 cacheRead=3600 cacheWrite=0 cacheHitRate=0.88
```

What each line means:

- **`turn.start`** — a new model call begins (`turn=N`).
- **`message.delta`** — streamed assistant text for the turn.
- **`tool.call`** — the model requested a tool, with raw JSON `args`.
- **`tool.result`** — the tool's output (`error=false/true`), token-shaped by the tool.
- **`permission.ask`** — the loop paused for client approval; carries the tool,
  the proposed args, and a human `reason`. In the demo a simulated client clicks
  "allow" (`run.Approve(askID, true)`), so the loop resumes. Over HTTP, `POST
  /v1/sessions/{id}/approve` returns **`204 No Content`** when an existing stream
  (the prompt's `text/event-stream` response) carries the verdict's effects; if the
  process that parked the ask had died and a restarted process resumes the session at
  the ask, the same endpoint instead returns a **`text/event-stream`** body (the
  resumed run) — consume it exactly like the prompt stream.
- **`result`** — the terminal event: `stop` reason (`end_turn`, `max_turns`,
  `cancelled`, …), final text, and cumulative `usage`. `cacheHitRate` is
  `cacheRead / inputTokens`.

### Running the demo live

Drive the same scenario against a real model:

```console
$ export OPENAI_API_KEY=sk-...
$ go run ./cmd/mecademo --openai --model gpt-5
```

Demo flags (`cmd/mecademo`):

| Flag | Default | Meaning |
| --- | --- | --- |
| `--openai` | `false` | run against the live OpenAI Responses API (key from `OPENAI_API_KEY`) |
| `--model` | `mock-model` | model identifier when `--openai` is set |
| `--openai-base-url` | `""` | override the OpenAI API base URL |

Without `--openai` the demo is fully offline. With `--openai` and no
`OPENAI_API_KEY`, it exits with `--openai requires OPENAI_API_KEY to be set`.

