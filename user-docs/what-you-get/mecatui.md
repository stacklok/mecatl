---
sidebar_position: 10
title: Using mecatui
---

# Using mecatui

`mecatui` is the terminal client for mecatl — it hosts an embedded `mecated` server in-process by default, so `mecatui` in a repo is enough to get a working session with no separate server to run. This page covers the everyday things worth knowing about: switching model or effort without losing your conversation, sessions surviving a restart, and reading what's on screen while the model works. For the exhaustive reference (every keybinding, theming, MCP overlays), see the link at the bottom.

## Switching models mid-conversation (`/models`)

Open the model picker with `/models`, move the cursor to a model, and press `enter` to switch **immediately** — the conversation is always kept. Because a session is pinned to one provider for its lifetime, mecatui makes this happen by closing your current session and creating a fresh one on the new model, seeded with everything you've said and done so far. The new session sees the full prior context; only the header changes to reflect it.

If you switch **across providers** (say, from an Anthropic model to an OpenAI one), provider-private state — the reasoning cache, in particular — can't carry over, so the switch note honestly says so (`switched to <model> — conversation kept (prior reasoning cache dropped)`). The text of the conversation itself is unaffected.

Switching models is a separate action from clearing your conversation. If you actually want to start over, run `/clear` instead.

`ctrl+g` on a model sets it as your client-wide default, used for any workspace you haven't picked a model for yet. Your picks persist across mecatui restarts.

The picker lists every model the server can currently reach across every configured provider — if a model you expect isn't there, it's usually a missing credential rather than a mecatui bug. A model that doesn't support images or audio is exactly as capable as the provider and mecatl's own catalog agree it is; see [Extension points: LLM provider](/extension-points/llm-provider.md#providercapabilities) if you're curious why a model can show fewer capabilities than you expected on a given adapter.

## Switching reasoning effort mid-conversation (`/effort`)

`/effort` opens a picker over the fixed reasoning-effort tiers (`auto`, `low`, `medium`, `high`, `xhigh`, `max`). Pick one and press `enter` — mecatui applies it by **forking your conversation** onto a new session at the new effort level. Like the model switch, nothing is lost: you'll see a brief "switching effort — forking conversation…" note while it rebinds, then you're back in the same conversation at the new tier.

If your current model doesn't support reasoning effort, the picker warns you that the tier you pick will be ignored rather than pretending it applied.

## Your sessions survive a restart

Every session persists to disk as append-only JSONL — the conversation, the tool-call history, and the event timeline — under a per-workspace directory, mode `0700` (owner-only; it stores the raw conversation in plaintext). Quit mecatui and come back later and your work is still there.

`/sessions` opens a picker over past sessions in the current workspace. Each row shows a relative timestamp, the turn count, a title (taken from your first prompt, or the session id if there isn't one yet), and the model it ran on. Opening one replays its durable history — this is a pure read of what already happened, not a live reconnect, so a session that's still `running` or parked awaiting your approval can't be opened this way (mecatui tells you so rather than showing you a partial, misleading transcript).

## Working while the model streams

You don't have to wait for a turn to finish before typing your next thing. Pressing `enter` while a run is streaming **queues** your input instead of sending it — queue as many follow-ups as you like, and they're merged into one prompt and sent the moment the current turn ends. If you change your mind, `↑` on an empty input pulls the queued text back into the box for editing, non-destructively.

`esc` backs out of things in order: it clears whatever you've typed, then clears the queue, then — only if you press it again — cancels the run itself. Nothing is one accidental keystroke away from being lost.

## Tool calls render as compact, expandable cards

A tool call doesn't dump its full JSON arguments into your terminal by default — a long value collapses to a size-and-preview summary (`body: 5.1 KB / 72 lines · "## Context…"`), and a card that hid something always says so. `ctrl+t` expands the focused card to the full pretty-printed arguments and result. Edit/Write calls are the exception: their colourised diff renders in full either way, since that's the whole point of looking at them.

A Subagent card adds delegation visibility: while the child runs, its collapsed line names the child's **live current tool**, and `ctrl+t` shows a Team-format trace — tool chips with bounded previews of the child's args/results and capped message lines. The `ctrl+a` agents overlay gives Subagent and Parallel runs the same bounded-preview traces in their focus panes; the task board, findings, dispositions, and context meter stay Team-only. These previews are capped, control-byte-scrubbed, and client-only — see [Subagents, teams, and parallel](subagents-teams-parallel.md#watching-a-delegation-in-mecatui--bounded-previews).

## What's next

- [`docs/tui.md`](https://github.com/stacklok/mecatl/blob/main/docs/tui.md) — the full mecatui reference: every keybinding, the command palette, the context meter, diff and reasoning panels, and the state-file layout behind model/workspace picks.
- [Core tools](/what-you-get/core-tools.md) and [Subagents, teams, and parallel](subagents-teams-parallel.md) — what the model can actually do once you're in a session.
