---
sidebar_position: 10
title: Using mecatui
---

# Using mecatui

`mecatui` is the terminal client for mecatl — it hosts an embedded `mecated` server in-process by default, so `mecatui` in a repo is enough to get a working session with no separate server to run. This page covers the everyday things worth knowing about: switching model or effort without losing your conversation, sessions surviving a restart, and reading what's on screen while the model works. For the exhaustive reference (every keybinding, theming, MCP overlays), see the link at the bottom.

## Seeding your first prompt from the command line

`-p`/`--prompt` (or `--prompt-file` for a longer body) launches the session with
a seed prompt auto-submitted as the FIRST turn — the equivalent of typing the
prompt and pressing enter the moment the session is ready. The TUI then stays
open for follow-ups; it is not a one-shot:

```sh
mecatui -p "Summarize the failing tests in this repo" --workspace "$PWD"
```

The seed fires once: a `/models` restart or `/clear` rebinds the session but never
re-submits it. See the [`docs/tui.md` flags reference](https://github.com/stacklok/mecatl/blob/main/docs/tui.md#seeding-an-initial-prompt) for the full details.

## Changing completed-trajectory learning (`/learning`)

`/learning` cycles the operator setting through **Off → Review → Auto** in
`$XDG_CONFIG_HOME/mecatl/settings.yaml`, preserving unrelated YAML and comments. Off means
no automatic completed-trajectory reflection or review; Review is currently inert because
no review queue exists; Auto runs the user-model reviewer after eligible clean completions.
These modes do not control a separately configured `--user-model-consolidate-interval`,
which remains an independent process-wide maintenance schedule. The setting is build-time:
restart local mecatui after saving. In connect mode, mecatui never edits local settings;
change `learning.mode` on the remote server host and restart that remote server.

## Switching models mid-conversation (`/models`)

Open the model picker with `/models`, move the cursor to a model, and press `enter` to switch **immediately** — the conversation is always kept. Because a session is pinned to one provider for its lifetime, mecatui makes this happen by closing your current session and creating a fresh one on the new model, seeded with everything you've said and done so far. The new session sees the full prior context; only the header changes to reflect it.

If you switch **across providers** (say, from an Anthropic model to an OpenAI one), provider-private state — the reasoning cache, in particular — can't carry over, so the switch note honestly says so (`switched to <model> — conversation kept (prior reasoning cache dropped)`). The text of the conversation itself is unaffected.

Switching models is a separate action from clearing your conversation. If you actually want to start over, run `/clear` instead.

`ctrl+g` on a model sets it as your client-wide default, used for any workspace you haven't picked a model for yet. Your picks persist across mecatui restarts.

The picker lists every model the server can currently reach across every configured provider — if a model you expect isn't there, it's usually a missing credential rather than a mecatui bug. A model that doesn't support images or audio is exactly as capable as the provider and mecatl's own catalog agree it is; see [Extension points: LLM provider](/extension-points/llm-provider.md#providercapabilities) if you're curious why a model can show fewer capabilities than you expected on a given adapter.

For experimental `openai-codex`, the picker lists only models the live ChatGPT
account says are entitled; it never fills gaps from the public OpenAI catalog.
A rejected/expired token or service failure is shown with an `auth.yaml`/restart
or connectivity remedy. A ChatGPT subscription is a separate billing identity
from public API credit, uses an undocumented private backend, and has no automatic
refresh in this release. Embedded setup is documented in the [full operator
reference](https://github.com/stacklok/mecatl/blob/main/docs/usage/mecated.md#openai-codex-subscription-manual-token-experimental).

## Switching reasoning effort mid-conversation (`/effort`)

`/effort` opens a picker over the fixed reasoning-effort tiers (`auto`, `low`, `medium`, `high`, `xhigh`, `max`). Pick one and press `enter` — mecatui applies it by **forking your conversation** onto a new session at the new effort level. Like the model switch, nothing is lost: you'll see a brief "switching effort — forking conversation…" note while it rebinds, then you're back in the same conversation at the new tier.

If your current model doesn't support reasoning effort, the picker warns you that the tier you pick will be ignored rather than pretending it applied.

## Your sessions survive a restart

Every session persists to disk as append-only JSONL — the conversation, the tool-call history, and the event timeline — under a per-workspace directory, mode `0700` (owner-only; it stores the raw conversation in plaintext). Quit mecatui and come back later and your work is still there.

`/sessions` opens a picker over past sessions in the current workspace. Each row shows a relative timestamp, the turn count, a title (taken from your first prompt, or the session id if there isn't one yet), and the model it ran on. Opening one replays its durable history — this is a pure read of what already happened, not a live reconnect, so a session that's still `running` or parked awaiting your approval can't be opened this way (mecatui tells you so rather than showing you a partial, misleading transcript).

## Working while the model streams

You don't have to wait for a turn to finish before typing your next thing. Pressing `enter` while a run is streaming **queues** your input instead of sending it — queue as many follow-ups as you like, and they're merged into one prompt and sent the moment the current turn ends. If you change your mind, `↑` on an empty input pulls the queued text back into the box for editing, non-destructively.

`esc` backs out of things in order: it clears whatever you've typed, then clears the queue, then — only if you press it again — cancels the run itself. Nothing is one accidental keystroke away from being lost.

When an error is a **permanent** provider rejection (a 4xx status other than 408/429, a context-window overflow, a policy block), mecatui shows a one-line summary instead of a raw error block, telling you plainly that retrying won't help and you should start a new session or change the request. The raw error is still available on `ctrl+t` expand. Permanent errors are never auto-retried by the queue.

## Tool calls render as compact, expandable cards

A tool call doesn't dump its full JSON arguments into your terminal by default — a long value collapses to a size-and-preview summary (`body: 5.1 KB / 72 lines · "## Context…"`), and a card that hid something always says so. `ctrl+t` expands the focused card to the full pretty-printed arguments and result. Edit/Write calls are the exception: their colourised diff renders in full either way, since that's the whole point of looking at them.

A Subagent card adds delegation visibility: while the child runs, its collapsed line names the child's **live current tool**, and `ctrl+t` shows a Team-format trace — tool chips with bounded previews of the child's args/results and capped message lines. The `ctrl+a` agents overlay gives Subagent and Parallel runs the same bounded-preview traces in their focus panes; the task board, findings, dispositions, and context meter stay Team-only. These previews are capped, control-byte-scrubbed, and client-only — see [Subagents, teams, and parallel](subagents-teams-parallel.md#watching-a-delegation-in-mecatui--bounded-previews).

## Approving actions

When the agent needs permission, a modal asks you to **allow once**, **always allow** (this session), or **deny**. You can answer with the keyboard chords (`a` / `w` / `d` by default, rebindable) or by **left-clicking the button** with the mouse — a click does exactly what its chord does. Plan-mode reviews get the same clickable action bar (**approve & run** / **auto-accept edits** / **iterate**). Clicking anywhere else in the modal does nothing, so a stray click can't accidentally approve something.

## Remapping keys

Every mecatui action is rebindable — and the `?` help overlay always shows your **live** bindings, so a remap is reflected in the help you see, not just in the keys that fire. Three override layers merge **per action**: the repeatable `--keymap Action=chord[,chord2]` CLI flag wins, over the `keymap:` map in `~/.config/mecatui/settings.yaml` (mecatui's own client settings file), over the deprecated legacy location:

```yaml
keymap:
  Agents: ctrl+f12
  Effort: ctrl+f5
```

The `keymap:` setting used to live in `~/.config/mecatl/settings.yaml` (the server-shared file) — that location still works but is deprecated; the client file wins on conflict.

The classic use case is **getting readline-style editing back in the prompt**. By default `ctrl+a` opens the agents overlay and `ctrl+e` opens the effort picker — which means the usual line-start / line-end chords never reach the input box. The prompt input is a standard readline-style editor with its own fixed editing keys (word jumps on `alt+f`/`alt+b`, `ctrl+w` delete-word, `ctrl+k`/`ctrl+u` kill-line, `home`/`end`, and `ctrl+a`/`ctrl+e` for line start/end); those editing keys are the input widget's own and can't be rebound. But remapping the mecatui actions off those chords — as in the YAML above — frees `ctrl+a` and `ctrl+e` to reach the input as line-start / line-end again.

The footer help line, the inline trace/collapse markers, the permission-modal buttons, and every overlay's navigation footer (agents/team/mcp/effort/models/sessions pickers) all follow remapped keys whenever the displayed action is backed by the keymap. Genuinely local/non-keyMap controls stay literal — including raw form/list arrows and tabs, the schedule panel's `c`/`p`/`r`/`f`/`d` action keys, and the inline `@`-mention/slash-palette menus — as do the input widget's own editing keys. With the default approval chords (`a`/`w`/`d`) the permission-modal buttons read `[A]llow` / `Al[w]ays` / `[D]eny`; rebound, they switch to an honest standalone form (`[Y] allow` / `[Q] always allow` / `[N] deny`).

Rules, in plain language: action names must match the documented set exactly; a **global** action (one that fires at the main prompt) must be a modified or special chord — never a bare letter that would swallow your typing — while keys that only act inside an overlay or the permission modal may be bare; two actions in the same scope can't share a chord; the deny key can't collide with allow/always-allow/submit/cancel; and the send key must differ from the newline key. An invalid override fails startup with a clear `keymap:` error.

Settings are read **once at startup** — restart mecatui to apply a change (live reload is a planned follow-up). For the full action table, the input widget's editing keys, and the exact validation rules, see the [`docs/tui.md` Remapping keys reference](https://github.com/stacklok/mecatl/blob/main/docs/tui.md#remapping-keys).

## What's next

- [`docs/tui.md`](https://github.com/stacklok/mecatl/blob/main/docs/tui.md) — the full mecatui reference: every keybinding, the command palette, the context meter, diff and reasoning panels, and the state-file layout behind model/workspace picks.
- [Core tools](/what-you-get/core-tools.md) and [Subagents, teams, and parallel](subagents-teams-parallel.md) — what the model can actually do once you're in a session.
