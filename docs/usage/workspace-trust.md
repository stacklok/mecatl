## 6. Workspace trust & posture

### Declarative workspace trust (`trustedWorkspaces:`)

`--trust-project` is a **per-invocation** flag. For CI, a daemon, or a power-user
who works repeatedly in a known-good checkout, declaring the trust once is more
ergonomic than passing the flag every run. The **user-global** `settings.yaml`
(`$XDG_CONFIG_HOME/mecatl/settings.yaml`, or `~/.config/mecatl/settings.yaml`)
gains an optional top-level `trustedWorkspaces:` list of **absolute workspace
paths** to pre-trust:

```yaml
# ~/.config/mecatl/settings.yaml  (the operator's own, fully-trusted file)
trustedWorkspaces:
  - /home/me/src/my-project
  - /home/me/work/known-good-repo

permissions:            # the same file also carries user-scoped permission rules
  deny:
    - "Bash(curl:*)"
```

When the current workspace's path matches a declared entry, it is trusted exactly
as `--trust-project` would trust it — **the same admission gate, no separate
path**: the project's ALLOW rules and its project soul are honoured. This is
**read-only**: mecatl only ever *reads* `trustedWorkspaces:` from your
human-authored `settings.yaml`; it never writes it (the machine-written trust
registry is a **separate** file — see the next section). Both `mecated` and
`mecatui` honour it (it lives in the shared user config).

**Precedence and semantics:**

- The effective trust is `--trust-project` **OR** a `trustedWorkspaces` match.
  The flag wins as the *source label* when both apply; either way the workspace
  is trusted.
- **Path keying is symlink-safe.** Both the declared entries and the current
  workspace are compared on their **cleaned, absolute, symlink-resolved**
  (`realpath`) form, so a moved or symlinked path cannot forge or inherit another
  workspace's trust. A declared entry that does not resolve (typo / broken
  symlink) is ignored; the rest of the list is honoured.
- **Monotonic-positive.** `trustedWorkspaces` only ever **grants** trust. It can
  never override a **Deny** or a configured **Ask** anywhere — those tighten and
  are always honoured. Trust gates only whether a project's *ALLOW* rules (and
  its soul) are admitted, never the deny-dominant evaluation.
- **Fail-safe.** A missing key, a malformed entry, or an unparseable
  `settings.yaml` resolves to **untrusted** (a corrupt config never *grants*
  trust); it is logged, never an error that aborts startup. The composition logs
  the decision: `workspace trust trusted=… source=flag|declared|none`.

### Remembered trust + drift (`trust.yaml`)

Beyond the human-authored `trustedWorkspaces:` list, mecatl keeps a
**machine-written** trust registry at
`$XDG_CONFIG_HOME/mecatl/trust.yaml` (fallback `~/.config/mecatl/trust.yaml`).
It is a **sibling of, but never inside,** the human `settings.yaml` (the
settings-vs-state split): you edit `settings.yaml`; only the harness writes
`trust.yaml`. Each entry remembers a trusted workspace (keyed by its
`realpath`) **and** the **identity-anchor hash** captured at the moment of
trust:

```yaml
# ~/.config/mecatl/trust.yaml   (machine-written; do not hand-edit)
version: 1
workspaces:
  /home/me/src/my-project:
    anchorSHA256: 9f2c…           # the project's identity surface at trust time
    trustedAt: 2026-06-04T12:00:00Z
```

A remembered entry is honoured as `source=remembered` (precedence **below**
`--trust-project` and a `trustedWorkspaces:` match) **only while its anchor still
matches** the workspace's live identity surface.

- **The identity anchor** is the high-signal, rarely-edited project authority
  surface: the project **soul** (`<ws>/.mecatl/soul.md`), and the **project-tier**
  **agent**, **slash-command**, and **skill** definitions under `<ws>/.mecatl/*`
  and `<ws>/.claude/*`. It is hashed deterministically (sorted file set, per-file
  content hashes folded).
- **`settings.yaml` is NOT in the anchor.** Editing your project's permission
  rules (which change on nearly every commit) does **not** trigger drift — that
  would nag-fatigue you into blind-clicking trust. Permission edits re-resolve
  live (permconfig's own mtime cache) without re-prompting. **Drift fires only
  when the project's persona / agents / commands / skills change.**
- **Drift fails safe.** If a remembered workspace's anchor no longer matches
  (the project's identity surface changed since you trusted it), the workspace is
  re-gated to **untrusted** for this run, logged at `WARN`
  (`workspace trust: identity anchor DRIFTED …`). `mecated` has no prompt, so a
  drifted entry never silently inherits the old grant; the interactive re-prompt
  that turns drift back into a fresh trust decision is the `mecatui` first-encounter
  prompt (see the next section). `--trust-project` always overrides (it
  short-circuits before the registry is even read).
- **`mecated` is read-only on the registry** — it *reads* `trust.yaml`
  declaratively (a remembered + undrifted workspace is trusted) but **never
  prompts and never writes** it. A repo trusted in `mecatui` for a given user is
  honoured by that same user's `mecated` (shared `$XDG_CONFIG_HOME`); cross-user
  is not shared (use that user's `trustedWorkspaces:`).
- **Security.** The registry is keyed by `realpath` (symlink-safe, symmetric on
  read and write); the write uses `O_NOFOLLOW` + `0o600` + temp-then-rename (a
  pre-planted symlink at the path is refused); and an unreadable / oversized /
  corrupt / wrong-version `trust.yaml` resolves to **untrusted** (a corrupt
  registry never *grants* trust). The path is derived solely from your user XDG
  config dir, never from a repo-controlled path — a repo cannot self-trust.

### The `mecatui` first-encounter trust prompt

`mecated` is purely declarative — it never asks. But when you launch **`mecatui`**
with its **embedded** server (the default — bare `mecatui` always embeds) in a workspace that is **not yet trusted** and
that carries a **project authority set** worth gating, `mecatui` prompts you once,
**before** the TUI takes over the screen:

```
mecatui: do you trust the project files in this workspace?
  /home/me/src/some-cloned-repo
Trusting honours this project's soul, agents, commands, skills, and ALLOW rules. Its deny/ask rules apply regardless.
[t]rust (persist) / [o]nce (this run only) / [n]o (default):
```

- **`t` (trust)** — trust this run **and remember it**: writes the workspace +
  its current identity-anchor hash to `trust.yaml`, so future launches (and your
  own `mecated`) trust it without asking, until the project's identity surface
  drifts.
- **`o` (once)** — trust **this run only**; nothing is persisted. Next launch asks
  again.
- **`n` / Enter / anything else** — **do not trust** (the safe default): the
  project's soul, agents, commands, skills, and ALLOW rules are withheld; the agent
  still runs with your user-tier config and the built-in tools.

**When the prompt fires.** Only when there is something a trust grant would
actually admit: a project soul (`<ws>/.mecatl/soul.md`), a project-tier
agent/command/skill definition, or a project `settings.yaml`/`settings.local.yaml`
carrying **ALLOW** rules. A repo with only deny/ask rules (which apply regardless)
or no project authority at all is **never** prompted — you are not nagged for a
workspace that has nothing to gate. A workspace already trusted (via
`--trust-project`, a `trustedWorkspaces:` match, or a remembered + undrifted
`trust.yaml` entry) is **not** prompted either.

**Drift is a re-prompt.** If you previously trusted a workspace and its identity
surface (soul / agents / commands / skills) has since **changed**, the prompt
re-fires with a "this workspace **CHANGED** since you trusted it" notice —
answering `t` re-persists the new anchor.

**Non-interactive = untrusted (fail-safe).** If `mecatui`'s stdin is **not a
terminal** (piped, redirected, headless), it **cannot** prompt — so it proceeds
**untrusted** for that run and prints a one-line note. It never blocks startup
waiting for input and never auto-trusts off a pipe. To trust non-interactively,
pass `--trust-project` or declare the workspace in `trustedWorkspaces:`.

**Security.** The echoed workspace path is **terminal-escape-sanitized** before
display, so a repo directory named with embedded ANSI/OSC escapes cannot corrupt
or spoof the prompt (CWE-150). The prompt and the registry write live in the
`mecatui` composition root, not the render layer.

### What an untrusted workspace withholds

Trust is **not** a kill-switch. An untrusted repo is still a fully usable coding
agent — it degrades to **"ask the human" mode**, never **"do nothing" mode**. The
line is drawn between the agent's **own capability** (never gated) and the repo's
**injected steering/authority** (gated).

**Always active on ANY repo — trusted or not (NEVER gated):**

- the built-in tools (Read, Edit, Bash, …) and the whole agent loop;
- the base system prompt;
- **your own user-tier config**: the user soul, and your user-level agent
  definitions, slash commands, and skills under `$XDG_CONFIG_HOME/mecatl/*` (or
  `~/.config/mecatl/*`) and `~/.claude/*`. A repo cannot touch these;
- **every Deny / Ask rule** from any scope (they only tighten);
- the permission prompt itself — on an untrusted repo the agent still runs; it
  just **asks** for the tool calls the repo would have auto-allowed.

**Withheld when the workspace is UNTRUSTED — the project-injected authority set:**

- the project's permission **ALLOW** rules (auto-approval the repo grants itself);
- the project **soul** (`<workspace>/.mecatl/soul.md` — a repo rewriting the
  agent's persona);
- the **project tier** of **agent definitions** (`<workspace>/.mecatl/agents`,
  `<workspace>/.claude/agents`), **slash commands** (`<workspace>/.mecatl/commands`,
  `<workspace>/.claude/commands`), and **skills** (`<workspace>/.mecatl/skills`,
  `<workspace>/.claude/skills`).

So a freshly-cloned, untrusted repo cannot silently steer the model with a
`.claude/agents/evil.md`, a malicious slash command, an injected skill, a
self-granted auto-approve, or a repo persona — but you can still read, edit, and
run-with-a-prompt in it from the first run. An explicit `--commands-dir`,
`--agents-dir`, or `--skills-dir` you pass is **operator-supplied** (not
repo-injected) and is honoured regardless of trust. Each withheld project-tier
source is logged at `WARN` so the degradation is visible. Trust the repo
(`--trust-project` or a `trustedWorkspaces:` entry) to admit its full project
authority set.

> The soul carries a **double gate**: it is admitted only if the workspace is
> trusted (provenance) **AND** the `soul:apply` permission resolves to Allow
> (policy) — a logical AND. An untrusted repo's soul is withheld irrespective of
> `soul:apply`; a trusted repo's soul still obeys an explicit `soul:apply: deny`.

### The operator posture ladder (`--posture`)

The whole prompt/trust posture is set by **one ordered operator tier** chosen at
process start by whoever owns the blast radius. Higher tiers grant more autonomy
and prompt less:

| posture | allow-all (no mutate-ask prompts) | main substitution floor | child substitution floor | project-trust floor | use it for |
|---|---|---|---|---|---|
| `strict` (**default**, fail-closed) | off | gated | gated | (your own `--trust-project`) | interactive / untrusted repos |
| `trusted` | off | gated | gated | **on** (honour the project authority set) | a repo you trust, still want prompts |
| `auto` | **on** (main + children) | loosened | **gated** (child prompt-injection defence **ON**) | on | the **recommended unattended default** |
| `yolo` | **on** (main + children) | loosened | **loosened** (child defence **OFF**) | on | a disposable, isolated, single-tenant sandbox |

Pick the tier with `--posture <strict|trusted|auto|yolo>` on `mecated` or the
embedded `mecatui` server (it is **rejected when `mecatui` dials an external
server via `connect`** — the dialed server owns its own posture). `--yolo` is an **alias for
`--posture yolo`** and `--trust-project` is an **alias for `trusted`**; passing
both a `--posture` value and an alias resolves to the **higher tier** with a
`WARN`, an unknown `--posture` value fails closed to `strict` with a `WARN`, and a
CLI flag out-ranks the user-global `posture:` setting (below). Confirm what a given
combination resolves to with `mecated serve --print-posture` (prints the tier + the
per-defence breakdown and exits).

**`auto` is the recommended unattended default.** It is allow-all for the main
agent *and* its children, so a CI / container / VM run never parks on a mutate-ask
prompt — but the **child prompt-injection defence stays ON**: a subagent /
team-member / parallel-branch `$(...)`/backtick/heredoc command still resolves
through the child-ask model rather than auto-running. Only step up to `yolo` (which
loosens that child substitution floor too) where the harness genuinely cannot cause
durable harm.

> **Behaviour change — `--yolo` now also loosens the child substitution floor.**
> Previously the substitution-floor loosening was **main-only**; under `--posture
> yolo` (= `--yolo`) a child's `$(...)`/backtick/heredoc command **auto-runs** (the
> child injection defence is **OFF**). If you want allow-all but the child defence
> kept on, use `--posture auto`.

**The agent shell never sees the harness secrets.** Independently of the posture
tier, every agent-facing Bash shell runs with the harness's credentials scrubbed
out of its environment, so even under `auto`/`yolo` (allow-all) the model **cannot**
`echo $OPENROUTER_API_KEY` or `cat /proc/self/environ` to read a provider/auth key.
The scrub (the harness's secret-scrubbing layer) is a precise denylist: it drops the exact
credential vars the harness reads (the provider keys, the websearch keys, the
`MECATL_*`/`GH_TOKEN`/`GITHUB_TOKEN` tokens) plus secret-shaped names (`*_API_KEY`,
`*_TOKEN`, `*_SECRET`, `*_PASSWORD`, `AWS_*`, `AZURE_*`), while keeping the whole
toolchain (`PATH`, `HOME`, `GOPATH`, `GOCACHE`, `TMPDIR`, `LANG`, …) so `go
build`/`go test`/`git` still work. It applies to the main shell and to every
sandboxed subagent / team-member / parallel-branch shell.

#### How allow-all works (the mechanism)

For the allow-all tiers (`auto`/`yolo`) the harness suppresses the permission
prompts for the **built-in mutate-ask floor**.

It is **not** a `PermissionMode` and **not** an evaluator bypass. It injects a
single `ScopeCLI` allow-all **rule** into the engine's static ruleset, which
loosens **only** the built-in `Bash`/`Edit`/`Write`/`Team`/`SkillDraft` Ask floor.
The governance invariants are unchanged:

- A `Deny` in **any** scope (including `ScopeManaged`) still wins — deny-dominance is
  absolute. An admin can forbid specific tools/patterns even under allow-all.
- Any **deliberately configured** `Ask` (managed/project/user) still asks. Allow-all
  never suppresses a configured Ask, so a misconfigured Ask can still **block an
  unattended run** — the startup warning says so. (The common CI case configures no
  asks beyond the built-in floor, so allow-all is fully unattended there.)

**Main and children — and the child substitution floor is the `auto` vs `yolo`
line.** The allow-all **rule** is injected into both the main engine's ruleset
(`AudienceMain`) and the child/member ruleset (`AudienceSubagent`), so the mutate-ask
floor is loosened for subagents, team members, and parallel branches as well at both
allow-all tiers. The **child substitution-floor loosening** is what separates the two
tiers:

- Under **`auto`** the loosening stays **main-only** — a child's
  `$(...)`/backtick/heredoc command still resolves through the subagent child-ask
  model (see *Compound-Bash & substitution safety* below). The child
  prompt-injection defence is **ON**.
- Under **`yolo`** the loosening **also** applies to children — a child's
  substitution command **auto-runs**. The child injection defence is **OFF**. This
  is the deliberate behaviour change from the old main-only `--yolo`.

The allow-all rule also blankets the synthetic `soul:apply` floor (the soul is
applied without prompting under `auto`/`yolo`) — consistent and expected, since the
soul is already floor-Allow by default. A **configured** `Deny`/`Ask` on `soul:apply`
(or on any memory tool) still wins, exactly like every other tool.

**Sandbox-first.** The posture bypasses the *prompt*, never a *sandbox*. The real
boundary for unattended agentic execution is OS-level isolation (container/microVM,
network-off-by-default, ephemeral filesystem) — enable an allow-all posture **only
where the harness cannot cause durable harm**, and only on single-tenant daemons (the
posture makes *every* session on that daemon allow-all).

**Root refusal.** If an **allow-all posture** (`auto` or `yolo` — both waive the
mutate-ask floor) is requested **and** the process runs as root (`euid 0`) **and**
neither `MECATL_SANDBOX=1` nor `IS_SANDBOX=1` is set, the process **refuses to
start** with a clear error: root + no prompts can modify anything on the host, so the
operator must affirm an isolated, disposable environment via the env var. (This was
generalised from the old `--yolo`-only refusal; it now gates `auto` too.)

```sh
# CI / sandboxed container, offline mock, allow-all + child defence ON:
MECATL_SANDBOX=1 bin/mecated serve --mock --posture auto

# Disposable sandbox, child defence OFF too:
MECATL_SANDBOX=1 bin/mecated serve --mock --posture yolo   # == --yolo
```

---

See also: [permissions configuration](permissions-config.md), or the
[operator guide index](../usage.md).

