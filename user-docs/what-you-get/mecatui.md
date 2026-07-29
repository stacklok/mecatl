---
sidebar_position: 10
title: Using mecatui
---

# Using mecatui

`mecatui` is the terminal client for mecatl — it hosts an embedded `mecated` server in-process by default, so `mecatui` in a repo is enough to get a working session with no separate server to run. This page covers one thing worth knowing up front: **switching your model or reasoning effort mid-conversation never loses your conversation.** For the rest of the TUI (context meter, diff rendering, the command palette, keybindings), see the full reference linked at the bottom.

## Switching models mid-conversation (`/models`)

Open the model picker with `/models`, move the cursor to a model, and press `enter` to switch **immediately** — the conversation is always kept. Because a session is pinned to one provider for its lifetime, mecatui makes this happen by closing your current session and creating a fresh one on the new model, seeded with everything you've said and done so far. The new session sees the full prior context; only the header changes to reflect it.

If you switch **across providers** (say, from an Anthropic model to an OpenAI one), provider-private state — the reasoning cache, in particular — can't carry over, so the switch note honestly says so (`switched to <model> — conversation kept (prior reasoning cache dropped)`). The text of the conversation itself is unaffected.

Switching models is a separate action from clearing your conversation. If you actually want to start over, run `/clear` instead.

`ctrl+g` on a model sets it as your client-wide default, used for any workspace you haven't picked a model for yet. Your picks persist across mecatui restarts.

The picker lists every model the server can currently reach across every configured provider — if a model you expect isn't there, it's usually a missing credential rather than a mecatui bug. A model that doesn't support images or audio is exactly as capable as the provider and mecatl's own catalog agree it is; see [Extension points: LLM provider](/extension-points/llm-provider.md#providercapabilities) if you're curious why a model can show fewer capabilities than you expected on a given adapter.

## Switching reasoning effort mid-conversation (`/effort`)

`/effort` opens a picker over the fixed reasoning-effort tiers (`auto`, `low`, `medium`, `high`, `xhigh`, `max`). Pick one and press `enter` — mecatui applies it by **forking your conversation** onto a new session at the new effort level. Like the model switch, nothing is lost: you'll see a brief "switching effort — forking conversation…" note while it rebinds, then you're back in the same conversation at the new tier.

If your current model doesn't support reasoning effort, the picker warns you that the tier you pick will be ignored rather than pretending it applied.

## What's next

- [`docs/tui.md`](https://github.com/stacklok/mecatl/blob/main/docs/tui.md) — the full mecatui reference: every keybinding, the command palette, the context meter, diff and reasoning panels, and the state-file layout behind model/workspace picks.
- [Core tools](/what-you-get/core-tools.md) and [Subagents, teams, and parallel](subagents-teams-parallel.md) — what the model can actually do once you're in a session.
