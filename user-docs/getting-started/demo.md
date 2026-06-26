---
sidebar_position: 1
title: See it in 60 seconds
---

`mecademo` drives a real `agent.Engine` against a scripted offline provider — no network, no API key — and shows the complete loop: a tool call, a permission pause with approval, and a final result with usage accounting.

## Prerequisites

- **Go 1.26.3** (the `go` directive in `go.mod` auto-fetches the toolchain via `GOTOOLCHAIN`)
- The repo cloned locally:
  ```console
  $ git clone https://github.com/stacklok/mecatl
  $ cd mecatl
  ```

## Run it

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

## What each event means

| Event | What it represents |
|---|---|
| `turn.start` | A new model call begins. `turn=N` increments each time the loop calls the provider. |
| `message.delta` | Streamed assistant text for this turn. In production this arrives incrementally. |
| `tool.call` | The model requested a tool, with the raw JSON `args` it supplied. |
| `tool.result` | The tool's output. `error=false` means it ran cleanly; the result text is what gets fed back to the model. |
| `permission.ask` | The loop paused for client approval. Carries the tool name, proposed args, and a human-readable `reason`. The demo immediately calls `run.Approve(askID, true)`. Over HTTP this is `POST /v1/sessions/{id}/approve`. |
| `result` | Terminal event. `stop` is the reason (`end_turn`, `max_turns`, `cancelled`, …), followed by the final assistant text and cumulative token usage. `cacheHitRate` is `cacheRead / inputTokens`. |

The `permission.ask` / approve round-trip is the key integration point. Your client decides whether to allow or deny each ask; the loop resumes or surfaces an error result accordingly. In a live deployment you surface this to a human or route it through your own policy layer.

## Run it live (optional)

Drive the same scenario against a real model:

```console
$ export OPENAI_API_KEY=sk-...
$ go run ./cmd/mecademo --openai --model gpt-5
```

| Flag | Default | Meaning |
|---|---|---|
| `--openai` | `false` | Use the live OpenAI Responses API (reads `OPENAI_API_KEY`) |
| `--model` | `mock-model` | Model identifier when `--openai` is set |
| `--openai-base-url` | `""` | Override the OpenAI API base URL (any OpenAI-compatible endpoint) |

Without `--openai` the demo is fully offline. With `--openai` and no key set, it exits immediately with an error.

## What's next?

- [Deployment decision](./deployment-decision.md) — how to pick the right deployment topology for your use case
- [The agent loop](/what-you-get/agent-loop.md) — how the loop, ports, and event types fit together
