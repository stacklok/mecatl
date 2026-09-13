---
sidebar_position: 1
title: See it in 60 seconds
description: Run the offline Mecatl demo to see tools, approvals, teams, and subagents in action.
---

# See it in 60 seconds

`mecademo` runs three scenarios against a scripted offline provider: the core
agent loop, a two-member agent team, and a background subagent. It needs no
network connection or API key. To inspect the engine wiring, see
[`cmd/mecademo/demo.go`](https://github.com/stacklok/mecatl/blob/main/cmd/mecademo/demo.go).

## Prerequisites

- **Go 1.26.6 or newer** (the `go` directive in `go.mod` sets this minimum version)
- The repo cloned locally:
  ```console
  $ git clone https://github.com/stacklok/mecatl
  $ cd mecatl
  ```

## Act 1 — the core loop

```console
$ go run ./cmd/mecademo
=== mecatl demo (offline / mockllm) ===
Driving a real agent.Engine: auto-allowed tool call -> permission ask + approval -> final result.
guardrails: OFF (no checker model configured; bind the `guardrail` model slot or set --guardrails-model to enable)

[001] turn=0 session.init
[002] turn=0 user_prompt
[003] turn=0 turn.start
[004] turn=0 message.delta  text="I'll read the greeting file first."
[005] turn=0 turn.end
[006] turn=0 tool.call      tool=Read args={"path":"greeting.txt"}
[007] turn=0 tool.result    error=false result="     1\thello from the mecatl demo workspace"
[008] turn=1 turn.start
[009] turn=1 message.delta  text="Now I'll save a short note, which needs your approval."
[010] turn=1 turn.end
[011] turn=1 tool.call      tool=Write args={"path":"note.txt","content":"reviewed the greeting\n"}
[012] turn=1 permission.ask ASK tool=Write reason="approval required by rule for Write (note.txt)"  -> client auto-approves
[013] turn=1 approval
[014] turn=1 tool.result    error=false result="wrote \"note.txt\" (22 bytes)"
[015] turn=2 turn.start
[016] turn=2 message.delta  text="Done: I read greeting.txt and saved note.txt."
[017] turn=2 turn.end
[018] turn=0 result         stop=end_turn text="Done: I read greeting.txt and saved note.txt."
      usage: in=4100 out=125 cacheRead=3600 cacheWrite=0 cacheHitRate=0.88
```

## What each event means

| Event            | What it represents                                                                                                                                                                                                   |
| ---------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `session.init`   | Once, before the first turn — the run has started and the session is initialised.                                                                                                                                    |
| `user_prompt`    | The user message has been recorded into the session history.                                                                                                                                                         |
| `turn.start`     | A new model call begins. `turn=N` increments each time the loop calls the provider.                                                                                                                                  |
| `message.delta`  | Streamed assistant text for this turn. In production this arrives incrementally.                                                                                                                                     |
| `turn.end`       | The model finished streaming this turn (text + any tool calls received).                                                                                                                                             |
| `tool.call`      | The model requested a tool, with the raw JSON `args` it supplied.                                                                                                                                                    |
| `tool.result`    | The tool's output. `error=false` means it ran cleanly; the result text is what gets fed back to the model.                                                                                                           |
| `permission.ask` | The loop paused for client approval. Carries the tool name, proposed args, and a human-readable `reason`. The demo immediately calls `run.Approve(askID, true)`. Over HTTP this is `POST /v1/sessions/{id}/approve`. |
| `approval`       | The verdict has been received and recorded (allow once, allow always, or deny).                                                                                                                                      |
| `result`         | Terminal event. `stop` is the reason (`end_turn`, `max_turns`, `cancelled`, …), followed by the final assistant text and cumulative token usage. `cacheHitRate` is `cacheRead / inputTokens`.                        |

The `permission.ask` and approval events are the key integration point. Your
client decides whether to allow or deny each request. In a live deployment, you
can present the request to a person or route it through your policy layer.

## Act 2 — agent team

The second act runs a two-member team with a lead and a worker. The worker
records a finding, and the lead turns it into the team's final report.

```console
=== mecatl team demo (offline) ===
A lead + worker coordinate; the worker records a finding; the lead synthesises the consolidated report.

team finished in 2 round(s); quiescent=true
--- consolidated report (the team's deliverable) ---
Consolidated report: the worker confirmed greeting.txt reads cleanly; nothing to fix.
```

The report is the lead's synthesis, not a concatenation of member outputs. See [Subagents & teams](/building/what-you-get/subagents-teams-parallel.md) for how teams work.

:::note

This example only works offline. It will be disabled if you configure a live LLM backend.

:::

## Act 3 — background subagent

The third act starts a child with `background: true`. The parent receives an
immediate start result, waits with `SubagentStatus`, and collects the result
after the child finishes.

```console
=== mecatl background subagent demo (offline) ===
A subagent runs in the background; the harness notice lands at the next turn boundary; SubagentStatus collects the result.

[001] turn=0 session.init
[002] turn=0 user_prompt
[003] turn=0 turn.start
[004] turn=0 message.delta  text="I'll start a background subagent to verify the greeting while I keep this turn."
[005] turn=0 turn.end
[006] turn=0 tool.call      tool=Subagent args={"prompt":"verify the greeting file in the background","background":true}
[007] turn=0 subagent.start child=subagent-demo-background-session-call-bg-1 background=true goal="verify the greeting file in the background"
[008] turn=0 tool.result    error=false result="agentId: subagent-demo-background-session-call-bg-1 ⏎  ⏎ subagent started in the background. ..."
[009] turn=1 turn.start
[010] turn=1 message.delta  text="Waiting for the background subagent to finish."
[011] turn=1 turn.end
[012] turn=1 tool.call      tool=SubagentStatus args={"wait_ms":30000}
[013] turn=0 subagent.tool
[014] turn=0 subagent.tool
[015] turn=0 subagent.end   child=subagent-demo-background-session-call-bg-1 stop=end_turn
[016] turn=1 tool.result    error=false result="Subagents of this run (1): ⏎ - subagent-demo-background-session-call-bg-1 [subagent, background] done (end_turn) — result ready; collect it with agent_id"
[017] turn=2 user_prompt
[018] turn=2 turn.start
[019] turn=2 message.delta  text="The harness notice says it finished — collecting its result."
[020] turn=2 turn.end
[021] turn=2 tool.call      tool=SubagentStatus args={"agent_id":"subagent-demo-background-session-call-bg-1"}
[022] turn=2 tool.result    error=false result="agentId: subagent-demo-background-session-call-bg-1 ⏎  ⏎ Background check complete: greeting.txt is intact and well-formed."
[023] turn=3 turn.start
[024] turn=3 message.delta  text="Done: the background subagent verified the greeting and I collected its result."
[025] turn=3 turn.end
[026] turn=0 result         stop=end_turn text="Done: the background subagent verified the greeting and I collected its result."
      usage: in=0 out=0 cacheRead=0 cacheWrite=0 cacheHitRate=0.00
--- harness notice(s) injected into the model's history at the turn boundary ---
[harness note: 1 background subagent(s) finished: subagent-demo-background-session-call-bg-1 (end_turn). Collect each result with SubagentStatus before relying on it.]
```

The `user_prompt` at event 017 is the harness notice — a recorded user-role message the model sees at the next turn boundary. It is not a user keystroke; it is the mechanism by which the loop informs the model that a background child finished.

:::note

This example only works offline. It will be disabled if you configure a live LLM backend.

:::

## Run it live (optional)

Drive the same scenario against a real model:

```console
$ export OPENAI_API_KEY=sk-...
$ go run ./cmd/mecademo --openai --model gpt-5
```

| Flag                | Default      | Meaning                                                           |
| ------------------- | ------------ | ----------------------------------------------------------------- |
| `--openai`          | `false`      | Use the live OpenAI Responses API (reads `OPENAI_API_KEY`)        |
| `--model`           | `mock-model` | Model identifier when `--openai` is set                           |
| `--openai-base-url` | `""`         | Override the OpenAI API base URL (any OpenAI-compatible endpoint) |

Without `--openai` the demo is fully offline. With `--openai` and no key set, it exits immediately with an error.

Currently only OpenAI and a mock model are supported in the demo.

## Next steps

- [Deployment decision](./deployment-decision.md) — how to pick the right deployment topology for your use case
- [The agent loop](/building/what-you-get/agent-loop.md) — how the loop, ports, and event types fit together
