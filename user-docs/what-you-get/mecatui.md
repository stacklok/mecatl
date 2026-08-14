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

## Continue a chat when mecatui starts

Pass `--resume SESSION_ID` to open one exact owned main chat, or `--resume-latest`
to open the newest eligible one. Both selectors work when mecatui hosts its embedded
server and in `mecatui connect` mode, and cannot be combined:

```sh
mecatui --resume 01JOPAQUESESSIONID
mecatui connect 127.0.0.1:8080 --resume-latest
```

mecatui loads the authoritative stored transcript and adopts that same chat; it does
not create a throwaway session. The chat keeps its stored workspace, mode, model, and
capabilities. Latest skips active or awaiting chats, scheduled and child runs, unknown
rows, and rows without a complete transcript. An exact selector reports why the chosen
row cannot be continued.

The first new prompt still goes through the server's atomic attachment and lease checks.
If that fails, the prior transcript remains read-only and no fallback chat is created:
press `r` to retry the preserved prompt, or `esc` to go Back and edit it. You can combine
a resume selector with `--prompt` or `--prompt-file`; mecatui adopts the old transcript
first, then sends the seed exactly once as the next turn.

While mecatui is open, `/session` then `c` is the quickest way to copy the exact active
ID. If you quit normally instead, mecatui restores the terminal and then prints one stable
line to stderr:

```text
mecatui: final-session-id="01JOPAQUESESSIONID"
```

Everything after `=` is a JSON string. Decode it with a JSON decoder to recover the
byte-exact final active ID, including after you continued another chat, changed model or
effort, or switched worktrees. Save that ID and pass it to `--resume` next time. The line
is deliberately absent if no session was established, startup/the TUI failed, or a signal
interrupted or forced the exit; normal stdout remains available to scripts. See the
[`docs/tui.md` startup continuation reference](https://github.com/stacklok/mecatl/blob/main/docs/tui.md#continue-a-chat-at-startup).

## Changing completed-trajectory learning (`/learning`)

`/learning` cycles the operator setting through **Off → Review → Auto** in
`$XDG_CONFIG_HOME/mecatl/settings.yaml`, preserving unrelated YAML and comments. Off means
no automatic completed-trajectory reflection. Review signal-gates eligible completions and
stages evidence-backed proposals without changing memory; Auto uses the same stage-first path
and may conservatively promote eligible non-conflicting facts. These modes do not control a separately configured `--user-model-consolidate-interval`,
which remains an independent process-wide maintenance schedule. The setting is build-time:
restart local mecatui after saving. In connect mode, mecatui never edits local settings;
change `learning.mode` on the remote server host and restart that remote server.

## Reviewing reflections (`/reflections`, `/reflect`)

When the server advertises staged learning, `/reflections` opens a bounded proposal list and detail
view with independent operator/project pagination. Before approval, the scrollable detail shows the complete bounded canonical key, value, scope, and optional description rather than only a benign summary, together with each evidence handle's ownership-checked, digest-reverified source session/sequence/tool-call/digest provenance and bounded redacted canonical preview. Approve or reject staged fact proposals with version-checked decisions; evidence-backed procedures can be materialized and evaluated into a linked learned skill. Active agent-owned skills are labelled with owner/version in `/skills`; learned rows open bounded body/evaluation/receipt detail and activate/reject/archive/rollback only through revision-CAS server calls. Updates never auto-open an overlay. Stale decisions offer an in-place refresh, and promoted facts offer a
compensating undo only while their linked memory revision is still current and the partition's convergence-capable memory target is available. When a project target is unavailable or is not the exact trusted configured root, approve and undo are disabled with the server-provided reason while the proposal remains inspectable and rejectable. Unavailable, changed, and cross-owner evidence is shown honestly without a preview and cannot be approved; raw tool/permission arguments, reasoning, binary data, controls, and secrets are omitted. `/reflect` explicitly submits
the current completed session synchronously on that session's persisted provider/model and works even when automatic learning is Off through lazy initialization. Older or unconfigured
servers hide these commands through capability discovery.

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

`/sessions` opens a searchable inventory in four tabs: **Chats**, **Scheduled runs**, **Child runs**, and **Other**. Unknown legacy or custom rows appear under Other instead of being mislabeled as children. Rows show state, time, turns, title, model, and a short digest handle; the chat you are currently using is marked **`[current]`**. The search applies to the selected tab and matches the row's title, model, workspace, digest, and available relationship details.

The main header uses that same compact digest instead of exposing the full opaque ID. Type `/session` to see the active chat's safely quoted full ID and metadata (title, state, workspace, known timestamps, provider, and model), then press `c` to copy the exact ID. Clipboard failure or a session switch is reported rather than shown as a successful stale copy.

Press `enter` on a Chat to **Continue** it when the server says it is publicly continuable. mecatui loads the authoritative snapshot transcript first, then makes that chat the active prompt target. Scheduled, Child, and inspect-capable Other runs are normally **Inspect** instead: their same authoritative snapshot transcript opens read-only, without changing your active chat. This is deliberately non-destructive — `esc` is **Back** to the inventory. If a transcript cannot be loaded completely, mecatui does not continue it; the error view offers **`r` Retry** or **Back**. The durable event log may help live delivery catch-up, but it is not used as the conversation transcript or as proof that a transcript is complete.

## Suspending and quitting like a terminal app

`ctrl+z` **suspends** mecatui to your shell, exactly like `vim` or `top`: the UI drops out of the way and `fg` brings it back. One thing to know — **the engine keeps running while you're suspended.** If a turn was in flight, the embedded `mecated` and the agent keep working in the background; when you `fg`, the UI catches up to wherever the run got to, and a notice in the conversation names what was suspended so you're not surprised. (If you'd rather the run stop, cancel it first, then suspend.)

Quitting follows the unix double-press habit: `ctrl+c` on an empty prompt (or `ctrl+d`, the EOF key) arms a "press again to quit" guard, and a second press within a few seconds exits. The two keys are independent — neither one confirms the other. `ctrl+c` on a *populated* prompt clears your draft instead; `ctrl+d` on a populated prompt stays the usual delete-forward.

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
