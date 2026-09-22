---
sidebar_position: 1
title: See it in 60 seconds
description:
  Run the offline Mecatl demo to see tools, approvals, teams, and subagents in
  action.
---

# See it in 60 seconds

Run `mecademo` to see the agent loop, a two-member team, and a background
subagent. The demo uses a scripted model provider, so it needs no API key or
network connection.

## Prerequisites

You need **Go 1.27 or newer** and a local clone of the Mecatl repository:

```sh
git clone https://github.com/stacklok/mecatl
cd mecatl
```

## Run the demo

From the repository root, run:

```sh
go run ./cmd/mecademo
```

The command runs three scenarios in sequence.

### Core agent loop

The first scenario shows every event in a three-turn agent loop. It reads a
file, pauses for approval before writing another file, and returns a final
response. The highlighted lines show the tool calls, approval round trip, and
terminal result.

```text {10-11,15-18,22-23}
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

Read the trace as a sequence of provider turns and tool interactions:

|Events|What happens|
|-|-|
|`session.init`, `user_prompt`|The run starts and records the prompt in session history.|
|`turn.start`, `message.delta`, `turn.end`|The provider streams one turn. Each tool result starts another provider turn.|
|`tool.call`, `tool.result`|The model requests a tool, and the loop returns the tool's output to the model.|
|`permission.ask`, `approval`|The write pauses until the demo client approves it.|
|`result`|The run ends with the final response, stop reason, and cumulative usage.|

A production client can present a permission request to a person or resolve it
through a policy layer.

### Agent team

The second scenario assigns work to a lead and a worker:

```text {4-6}
=== mecatl team demo (offline) ===
A lead + worker coordinate; the worker records a finding; the lead synthesises the consolidated report.

team finished in 2 round(s); quiescent=true
--- consolidated report (the team's deliverable) ---
Consolidated report: the worker confirmed greeting.txt reads cleanly; nothing to fix.
```

The worker records a finding, and the lead turns it into the final report.

### Background subagent

The final scenario starts a child agent in the background. The `Subagent` call
returns immediately with the child's ID, so the parent can continue before it
waits for the child and collects the result with `SubagentStatus`.

The excerpt shortens long result bodies with `...` and omits repeated
turn-boundary events.

```text {1-3,5-6,8-10}
[006] turn=0 tool.call      tool=Subagent args={"prompt":"verify the greeting file in the background","background":true}
[007] turn=0 subagent.start child=subagent-demo-background-session-call-bg-1 background=true goal="verify the greeting file in the background"
[008] turn=0 tool.result    error=false result="agentId: subagent-demo-background-session-call-bg-1 ... subagent started in the background. ..."
[...]
[012] turn=1 tool.call      tool=SubagentStatus args={"wait_ms":30000}
[015] turn=0 subagent.end   child=subagent-demo-background-session-call-bg-1 stop=end_turn
[...]
[017] turn=2 user_prompt
[021] turn=2 tool.call      tool=SubagentStatus args={"agent_id":"subagent-demo-background-session-call-bg-1"}
[022] turn=2 tool.result    error=false result="agentId: subagent-demo-background-session-call-bg-1 ... Background check complete: greeting.txt is intact and well-formed."
```

The `user_prompt` at event 017 is a harness-generated completion notice in the
model's history. It tells the parent that the background child has finished so
the parent can collect the result with `SubagentStatus`.

You have now run the agent loop, an agent team, and a background subagent
without configuring a model provider.

## Run the demo with OpenAI

After completing the offline demo, you can run its core-loop scenario against
the OpenAI Responses API:

```sh
export OPENAI_API_KEY='<OPENAI_API_KEY>'
go run ./cmd/mecademo --openai --model gpt-5
```

|Flag|Default|Description|
|-|-|-|
|`--openai`|`false`|Use the OpenAI Responses API with `OPENAI_API_KEY`.|
|`--model`|`mock-model`|Set the model ID used with `--openai`.|
|`--openai-base-url`|`""`|Use an OpenAI-compatible API endpoint.|

The team and background-subagent scenarios use the scripted provider and do not
run when you enable the live provider. A live request may incur provider
charges.

## Next steps

- [Build your first agent](./first-agent.md) to embed the engine in a Go
  application.
- [Choose how to run Mecatl](/operating/choose-deployment.md) to select a deployment
  topology.
- [Explore the agent loop](/features/sessions/agent-loop.md) to understand
  the events and control flow shown by the demo.

## Related information

- [Subagents, teams, and parallel work](/features/agent-behavior/subagents-and-teams.md)
- [`mecademo` source](https://github.com/stacklok/mecatl/blob/main/cmd/mecademo/demo.go)
