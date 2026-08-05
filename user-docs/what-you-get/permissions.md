---
sidebar_position: 3
title: Permissions & guardrails
---

# Permissions & guardrails

mecatl gates tool execution with **two independent layers**. Layer 1 is a
rule-based permission engine that fires on **every** tool call before execution
and resolves to allow / ask / deny. Layer 2 is an optional model-backed guardrail
checker that inspects tool input and output on **operator-configured matchers** —
it inspects *content*, where Layer 1 inspects the *call*.

The layers are separate by design: Layer 1 decides whether a call is permitted to
run at all; Layer 2 inspects the data crossing the tool boundary once a call is
permitted. Configure them independently. Layer 1 is always on (it ships with a
safe default ruleset); Layer 2 is off until you give it a checker model.

---

## Layer 1 — the permission rule engine

Every tool call is evaluated against a merged set of rules. A rule is
`{Scope, Tool, Pattern, Effect}`: `Effect` is `allow`, `ask`, or `deny`; an empty
`Tool` matches any tool; an empty `Pattern` matches any arguments, otherwise it is
a shell-style glob over the canonicalized command/argument string.

### Resolution: deny-dominant, then scope

A decision resolves in this order:

1. **Effect dominance: `deny` → `ask` → `allow`.** A `deny` in *any* scope beats an
   `ask` or `allow` *anywhere* — a deny is absolute and final. Otherwise an `ask`
   beats an `allow`.
2. **Scope breaks same-effect ties** (highest precedence first):
   `Managed > CLI > LocalProject > SharedProject > User > BuiltinDefault`.
3. **No matching rule → `ask`** — the safe default. The harness never silently
   allows an unconfigured call.

There is **one narrow exception** to "ask beats allow": a higher-scope configured
**Allow** may loosen *only* the built-in `BuiltinDefault` Ask floor (for example,
allowing `Bash(go test:*)` relaxes the built-in Bash ask). It can **never** suppress
a *configured* Ask, and it can never out-rank a deny in any scope.

A `deny` or `ask` carries a human-readable reason: surfaced to the model on a deny
(so it can adapt) and to the client on an ask.

### The scope hierarchy

Scopes are where a rule comes from, highest precedence first:

| Scope | Source | Trust |
|---|---|---|
| `Managed` | enterprise/admin floor | always honoured; nothing below overrides its deny |
| `CLI` | each `--permission-config <file>` | fully trusted (the operator's own) |
| `LocalProject` | `<workspace>/.mecatl/settings.local.yaml` (gitignored, personal) | **trust-gated** |
| `SharedProject` | `<workspace>/.mecatl/settings.yaml` (checked-in, shared) | **trust-gated** |
| `User` | `$XDG_CONFIG_HOME/mecatl/settings.yaml` | fully trusted (the operator's own) |
| `BuiltinDefault` | the built-in floor (read-allow / mutate-ask) | n/a — lowest precedence |

Project files are re-resolved **per session** against each session's workspace
root, and the resolver revalidates its cache on the config files' mtime/size — so a
`deny` added mid-process takes effect on the next call, not at restart. Two sessions
running in different repos under the same server get different decisions for the
same tool call.

### The default ruleset

The built-in floor allows read-only exploration and asks before anything that can
mutate:

| Tool | Default effect |
|---|---|
| `Read`, `Grep`, `Glob`, `WebFetch`, `WebSearch`, `Subagent` | `allow` |
| `Bash`, `Edit`, `Write`, `Team`, `SkillDraft` | `ask` |

(The memory tools, the synthetic `soul:apply` action, and the read-only child
observability tools are also floor-scoped allows — pre-approved but overridable by
any higher-scope config.) Read-only exploration runs uninterrupted; anything that
can mutate the workspace pauses for approval. `Team` asks because it can spawn
mutating members, unlike the read-only `Subagent` explorer.

### Effects: allow, ask, deny

- **`allow`** — the call runs without prompting.
- **`ask`** — the call pauses and is surfaced to the client for an approval
  decision (see [The `permission.ask` flow](#the-permissionask-flow)). An approval
  can be **allow-once** (this call only) or **allow-always** (learned for the
  session — see below).
- **`deny`** — the call never runs; the model receives the deny reason and adapts.

**Allow-once vs allow-always.** When a client approves an ask, it chooses the
duration. *Allow-once* clears only the current call. *Allow-always* feeds the
permission policy's `Learn` path, which derives a per-session rule so the same call
is not re-asked. A learned allow is consulted at the **lowest** scope only — it can
never override a deny or a configured ask.

### Compound Bash and substitution safety

For `Bash`, the evaluator splits a compound command line (`&&`, `||`, `;`, `|`, a
bare `&`, newlines, honouring quotes) and requires **every** sub-command to pass;
the **worst** outcome wins. So `git status && rm -rf /` inherits the deny/ask from
the `rm` segment even if `git status` alone would be allowed.

Any segment containing command/process substitution or subshell grouping (`$(...)`,
backticks, `<(...)`, `(`/`{` grouping) — which could smuggle a hidden inner command
past the splitter — is floored at **`ask`**. An allow rule for the outer literal can
never silently approve a concealed command. (The substitution floor can be loosened
by posture — see below — but only for read-only inners.)

### Plan mode

When a session is in `plan` mode, the evaluator gates *before* the rule engine:
`Edit` and `Write` are unconditionally **denied**, and any non-read-only `Bash`
command is **denied**. Read-only tools and read-only Bash fall through to the rules.
The deny reason tells the model to present a plan and exit plan mode first.

**Getting out of plan mode.** Once the model has a complete plan, it calls the
`PresentPlan` tool. That parks the run on a **plan-approval** ask — a distinct
gate from an ordinary permission ask, though it reuses the same ask machinery
described above. On an interactive client (`mecatui` shows a dedicated "Plan
ready for review" modal), you pick one of three outcomes: approve and run
(switches to `default` mode, so mutating tools still go through the ordinary
deny/ask/allow rules), approve with edits auto-accepted (switches to
`acceptEdits` mode), or iterate (stay in plan mode while the model revises and
re-presents).

In a **headless** deployment there's no human to review the plan, so by
default the ask is auto-denied and the model just keeps iterating. The opt-in
`--plan-mode-auto-approve` flag (operator-tier only, off by default) instead
auto-approves a parked plan ask. This is a deliberate autonomous-approval
capability for the operator, not a safety mechanism: the engine still requires
`PresentPlan` to reach this ask in the first place — the flag only decides who
resolves it once parked, human or auto-approve. See [Plan
approval](https://github.com/stacklok/mecatl/blob/main/docs/usage.md#plan-approval)
for the full gRPC/HTTP/ACP wire reference.

### Configuring rules

`.mecatl/settings.yaml` (checked-in, shared), `.mecatl/settings.local.yaml`
(gitignored, personal), and the user-global file all share the same shape:

```yaml
permissions:
  allow:
    - "Bash(go test:*)"   # the "prefix:*" form, normalised to the glob "go test*"
    - "Bash(go build*)"   # native glob form
    - "Read"              # bare tool name = tool-wide
  ask:
    - "Bash(git push:*)"
  deny:
    - "Bash(rm:*)"        # deny wins absolutely, in any scope — binds children too
  subagent:
    deny:
      - "Bash(gh pr merge:*)"   # tighten a child's Bash beyond the main rules
    allow:
      - "Bash(go vet:*)"        # clears this from a child's substitution-floored ask
```

Each entry is a rule spec `Tool(pattern)` or a bare `Tool`. Config rules use **glob**
semantics; the `prefix:*` / `prefix:` form is normalised to a `prefix*` glob. The
`permissions:` subtree parses **strictly** — an unknown key (a typo like `alow:`) is
a loud parse error and the whole file is skipped (and logged), never silently
ignored; the `subagent:` subtree inside it parses just as strictly, on its own.

### Importing Claude Code's permissions

If you already have a Claude Code `.claude/settings.json` in the repo (or your home
directory), `--import-claude-permissions` reads its permissions alongside your
`.mecatl/` config instead of making you duplicate the rules. The import is
deliberately **lossy** — every lossy outcome is logged, and it never *widens* what
Claude Code itself would have allowed:

- `WebFetch(domain:x)` in an allow list is demoted to `ask` — a domain/substring
  match is too risky to auto-allow without you seeing it at least once.
- A bare `WebSearch` allow imports verbatim, no demotion (its payload is a query
  string, not an arbitrary fetch).
- A `Read(~/...)` pattern imports but stays inert — the `~` is left unexpanded, so
  it never matches the absolute path a tool actually resolves to.
- Anything the importer can't parse is dropped, not guessed at.

`deny`/`ask` rules always import verbatim (tightening is never lossy). `mecatui`'s
embedded server turns this on by default, alongside `--permissions-conventional` —
if you've used Claude Code in a repo before, mecatui picks up its rules with no
extra setup.

### The posture ladder

A single operator tier — chosen at process start by whoever owns the blast radius —
sets how much the harness self-authorizes. Higher tiers grant more autonomy and
prompt less. Set it with `--posture <strict|trusted|auto|yolo>` (or the
operator-global `posture:` setting); `--trust-project` is an alias for `trusted` and
`--yolo` is an alias for `yolo`.

| posture | allow-all (no mutate-ask prompts) | child substitution floor | project trust | use it for |
|---|---|---|---|---|
| `strict` (**default**, fail-closed) | off | gated | (your own `--trust-project`) | interactive / untrusted repos |
| `trusted` | off | gated | **on** | a repo you trust, still want prompts |
| `auto` | **on** (main + children) | **gated** (injection defence **on**) | on | the recommended unattended default |
| `yolo` | **on** (main + children) | **loosened** (injection defence **off**) | on | a disposable, isolated, single-tenant sandbox |

`auto` is the recommended unattended default: allow-all for the main agent and its
children so a CI / container run never parks on a mutate-ask prompt, but the child
prompt-injection defence stays **on** (a subagent's `$(...)`/backtick command still
resolves through the child-ask model rather than auto-running). Only step up to
`yolo` — which loosens that child substitution floor too — where the harness
genuinely cannot cause durable harm.

Allow-all is **not** an evaluator bypass. It injects a single `ScopeCLI` allow-all
rule that loosens only the built-in mutate-ask floor. The governance invariants hold
at **every** tier including `yolo`:

- A `deny` in any scope (including `Managed`) still wins — deny-dominance is absolute.
- Any **deliberately configured** `ask` still asks. Allow-all never suppresses a
  configured ask, so a misconfigured ask can still block an unattended run (the
  startup warning says so).
- Plan-mode hard-denies still fire first.

:::warning[Sandbox-first]

The posture bypasses the *prompt*, never a *sandbox*. The real boundary for
unattended agentic execution is OS-level isolation (container/microVM,
network-off-by-default, ephemeral filesystem). Enable an allow-all posture only
where the harness cannot cause durable harm, and only on single-tenant daemons (the
posture makes *every* session on that daemon allow-all). If an allow-all posture is
requested while running as root and no sandbox env var (`MECATL_SANDBOX=1` or
`IS_SANDBOX=1`) is set, the process **refuses to start**.

:::

Independently of posture, every agent-facing shell runs with the harness's
credentials **scrubbed** from its environment — even under `auto`/`yolo`, the model
cannot `echo $OPENROUTER_API_KEY` or `cat /proc/self/environ` to read a provider key.

### Out-of-workspace filesystem access (the path-escape posture)

The FS tools (`Read`/`Write`/`Edit`) are rooted at the session workspace; a path that
resolves outside it used to be a dead end — the call failed with a path-escape error and
the model fell back to an opaque Bash `cat /path`, losing the FS tools' invariants and
audit shape. The posture now decides what an out-of-workspace escape does instead:

| Posture | Read escape | Write escape |
|---|---|---|
| `yolo` | allow | allow |
| `auto` | allow | **ask** |
| `strict` / `trusted` | **ask** | **ask** |

- **The consent model.** Every ask is an ordinary [Layer 1 `permission.ask`](#the-permissionask-flow):
  the prompt names the exact path, and approval is **allow-once only** — approving one
  out-of-workspace call never learns a rule that pre-approves the next one. A configured
  `deny` or configured `ask` always wins over the posture row (deny-dominance and the
  configured-Ask floor are untouched), and **plan mode still hard-denies writes first**.
- **Bash parity.** At `auto`/`yolo` a Bash `cat /outside` already reads the same bytes,
  so an un-asked read boundary on the FS tools was cosmetic; writes are never silent
  below `yolo`.
- **Never relaxed, at any posture:** paths under `/proc`, `/sys`, or `/dev` are a hard
  deny everywhere. An in-process Read of `/proc/self/environ` would expose the *server's*
  raw, unscrubbed environment — a channel the env-scrubbed Bash parity path does not
  provide — so the parity premise does not extend there. **Child agents** (subagents,
  team members, parallel branches) also never get the relax at any posture: only the
  main session's workspace carries it, and a child that shares the parent's workspace is
  handed a non-relaxed view of the same root.
- **Serving stays contained.** An approved escape is served through the same
  symlink-checked containment the workspace itself uses (a fresh `os.Root` on the
  target's parent directory) — the relax widens *which* paths may be served, never *how*.
- **Optional LLM gate at `auto`.** With a guardrail checker model configured, the
  operator-tier `guardrails.escape: true` setting (user-global `settings.yaml` only)
  routes each `auto`-posture escape through the [Layer 2 checker](#layer-2--model-backed-guardrails)
  first: unsafe → deny; a checker error fails closed to the write-escape ask. Default is
  off — the plain table above. See
  [ADR 0080](https://github.com/stacklok/mecatl/blob/main/docs/adr/0080-guardrail-routed-escape-checking.md)
  and the full operator reference in
  [`docs/usage.md`](https://github.com/stacklok/mecatl/blob/main/docs/usage.md).

### Workspace trust

Trust decides whether a **project's** injected authority is admitted. It is a
composition decision, not a permission scope, and it gates exactly the project-tier
authority set:

- the project's permission **ALLOW** rules (auto-approval the repo grants itself);
- the project **soul** (`<workspace>/.mecatl/soul.md` — a repo rewriting the agent's
  persona);
- the **project tier** of agent definitions, slash commands, and skills under
  `<workspace>/.mecatl/*` and `<workspace>/.claude/*`.

Trust is **not** a kill-switch. An untrusted repo is still a fully usable coding
agent — it degrades to **"ask the human" mode**, never **"do nothing" mode**. These
stay active on **any** repo, trusted or not: the built-in tools and the whole loop,
the base system prompt, your own user-tier config, **every deny/ask rule from any
scope** (they only tighten), and the permission prompt itself. So an untrusted repo
cannot silently steer the model with an injected agent, command, skill, persona, or
self-granted auto-approve — but you can still read, edit, and run-with-a-prompt in it
from the first run.

Note the asymmetry: a project's **deny and ask** rules are *always* honoured (they
only tighten); only its **allow** rules and soul are trust-gated. Trust is
**monotonic-positive** — it only ever *grants* admission, never overrides a deny or a
configured ask.

Configure trust three ways:

- **`--trust-project`** — a per-invocation flag (the `trusted` posture alias).
- **`trustedWorkspaces:`** — a list of absolute paths in the user-global
  `settings.yaml`, for CI / daemons / repeated work in a known-good checkout:

  ```yaml
  # ~/.config/mecatl/settings.yaml  (the operator's own, fully-trusted file)
  trustedWorkspaces:
    - /home/me/src/my-project
    - /home/me/work/known-good-repo
  ```

  This is read-only — mecatl only reads it, never writes it. Paths are compared on
  their cleaned, absolute, symlink-resolved form, so a moved or symlinked path
  cannot forge another workspace's trust.

- **The `mecatui` first-encounter prompt** — when you launch the embedded TUI in an
  untrusted workspace that carries a project authority set, it prompts once
  (`[t]rust / [o]nce / [n]o`, default no) before the TUI takes over. `t` remembers
  the decision in a machine-written registry (`trust.yaml`, separate from your
  human-authored `settings.yaml`). `mecated` never prompts — it reads the registry
  declaratively.

A remembered trust is keyed to the project's **identity anchor** (its soul plus
project-tier agent/command/skill definitions — but *not* `settings.yaml`, which
changes every commit). If that anchor drifts after you trusted it, the workspace is
re-gated to untrusted (and `mecatui` re-prompts). Editing permission rules does not
re-prompt; changing the persona / agents / commands / skills does.

A corrupt or unparseable `settings.yaml` or `trust.yaml` always resolves to
**untrusted** — a broken config never grants trust.

#### Project-tier ingestion on headless roots (the opt-in design)

On a **headless** root (`--headless`), posture never raises `TrustProject`. Explicit
`--trust-project`, `trustedWorkspaces:`, or undrifted remembered trust admits BOTH repo steering and
the read-only child shell. Without any trust source, `mecatequi --posture auto` keeps allow-all
approvals but gets neither because `.git` is not vouched. See the
[workspace trust reference](https://github.com/stacklok/mecatl/blob/main/docs/usage/workspace-trust.md#project-tier-ingestion-on-headless-roots-the-opt-in-design).

---

## Layer 2 — model-backed guardrails

Guardrails inspect the data crossing the agent's tool boundary with a **separate,
tool-less checker model** and enforce a verdict on the call. It is the *dual-LLM
quarantine* pattern: a dedicated model judges tool content as **data, never as
instructions**, so a compromised tool result or a model bent on exfiltration is
caught by something the attacker cannot also prompt-inject in the same breath.

It catches two trust-boundary crossings:

- **Outbound (`PreToolUse`) — exfiltration.** The model chose the arguments. A
  guardrail inspects the args before the call runs — a secret in an HTTP body, a
  credential in an MCP call, `.env` contents addressed to an external service.
- **Inbound (`PostToolUse`) — prompt injection.** A tool *result* is
  attacker-influenced data — a fetched web page, a GitHub issue body, an MCP
  response. A guardrail inspects the result the model is about to read for
  injection-like content.

The checker is the same trust model as Layer 1 turned inward: where Layer 1 gates
*whether a call runs*, guardrails inspect *what the call carries*.

### Verdicts: block, sanitize, advisory

Each rule sets a mode:

- **`block`** — enforce. A `PreToolUse` block is a real veto (the tool never runs).
  A `PostToolUse` block **rewrites the result to a model-visible error** rather than
  vetoing (see below).
- **`sanitize`** — enforce by rewriting the args (pre) or result (post) to the
  checker's `sanitized_content`. A sanitized result carries a
  `[guardrail: redacted unsafe content]` marker so the model knows it was edited.
  Sanitize **trusts the checker's output** (a compromised checker could rewrite
  content), so use it only with a checker model you trust; a nil, oversized, or
  invalid rewrite falls back to a block.
- **`advisory`** — observe-only. A finding emits an operator-log diagnostic and a
  client-visible advisory notice on the tool card, but the call/result is
  byte-unchanged and the model sees nothing. Measure the false-positive rate, then
  promote a rule to `block` or `sanitize`.

### PostToolUse block rewrites, it does not veto

This is the key non-obvious point. By the time a `PostToolUse` hook fires, the tool
has **already run** — so an enforcing inbound block cannot un-run it. Instead the
guardrail **rewrites** the result to an error (`is_error: true`). The loop guarantees
the recorded history, the client event stream, and the model's view all show the
**effective** (rewritten) result — so the model sees the block, the client agrees,
and the raw injected result never reaches either. Only a `PreToolUse` block is a true
veto.

### Recovering from a block: approve-once

A `block` verdict is not a permanent dead end. When the checker blocks a
`PreToolUse` call, the block is *askable*: on an interactive session the harness
pauses the run and surfaces it to the human through the **same permission-ask flow**
Layer 1 uses (see [The `permission.ask` flow](#the-permissionask-flow)) — an ordinary
approval modal carrying the actual blocked call, not a slash command or a prompt
directive the human has to predict and pre-type. The human picks one of the same
three verdicts:

- **Deny** — the call never runs; the model receives the block reason and adapts.
- **Allow once** — the call runs, this time only.
- **Allow & don't ask again** — the call runs, and the harness arms a **session-scoped
  waiver**: a later call matching the *exact* tool and the *exact* normalized command
  (`Bash`) or arguments (any other tool) skips the checker for the rest of the
  session. Matching is exact — never a substring, never a blanket per-tool bypass —
  so approving one `gh pr merge` call never waves through an unrelated one. The
  waiver is in-memory only and does not survive a process restart.

A run parked on an askable guardrail block resumes exactly like any other pending
approval, including across a process restart. In a **headless** deployment there is
no human to ask, so an askable block simply resolves as a terminal block — the same
fail-safe default as an unresolved Layer 1 ask.

**Posture coupling.** Under the [`yolo` posture](#the-posture-ladder) — the fully
gate-free tier — every guardrail rule is demoted to advisory (log and notify only;
never block or ask). `strict`, `trusted`, and `auto` all keep enforcing: under `auto`
the interactive approve-once ask *is* the intended behavior (the checker blocks, an
interactive human allows it once), so `auto` is deliberately excluded from the
demotion — only `yolo` trades the guardrail's enforcement away.

### Configuring guardrails

Guardrails are **off until you configure a checker model** — configuring a model is
the opt-in to spend (the only cost is the per-call checker LLM call). With a model
and no explicit rule list, guardrails are on with the **default block ruleset**:

| Tool matcher | Phases | Mode |
|---|---|---|
| `WebSearch` | pre + post | block |
| `WebFetch` | post | block |
| `mcp__*` (all MCP tools) | pre + post | block |
| `Bash` | pre | block (read-only commands skip the checker) |

The other local tools (`Read`/`Edit`/`Write`/`Grep`/`Glob`) are deliberately not
matched — they have no outward reach, and `Edit`/`Write` are workspace mutations git
already covers as the rollback layer. `Bash` **is** matched, because the shell is an
agent's single largest blast radius: it can push, merge, delete, or exfiltrate, and a
guardrail that ignores it misses exactly that surface (the motivating incident was an
agent running `gh pr merge --squash` as a `Bash` call and merging its own PR
unattended, with guardrails never seeing it).

Inspecting every shell command would be an unacceptable latency/cost tax on the `ls` /
`grep` / `git status` traffic that dominates a session, so the default `Bash` rule
carries a **read-only pre-filter**: a command that is confidently read-only (the same
classifiers Layer 1's rule engine uses) skips the checker entirely — zero LLM calls.
Anything else — a mutating or outward command, an unrecognized verb, or a substitution
it can't prove read-only — falls through to inspection; ambiguity always fails toward
inspecting, never skipping.

The default `Bash` rule also swaps in a **Bash-specific rubric** in place of the
generic exfiltration prompt used for the network/MCP rules — the generic rubric's
"if uncertain, judge unsafe" false-positives badly on ordinary shell work (a write to
a sibling repo never leaves the machine, so it isn't exfiltration). The Bash rubric
instead judges a command **safe unless it names one of five concrete danger
categories**: (1) sending data off the machine to a network destination, especially
secrets; (2) fetching and executing remote code (`curl … | sh`); (3) an irreversible
action against a remote you may not control (force-push, push/merge, `gh pr merge`,
publishing a release, deleting a remote branch/repo); (4) a destructive, hard-to-
reverse local operation (recursive deletion, overwriting a disk device, mass
recursive chmod/chown); (5) a local-persistence write to a credential, SSH key,
shell-startup file, scheduler entry, or git hook — a write that never leaves the
machine but grants later off-machine access or persistent code execution. Ordinary
local writes (source, config, build output, notes — including to sibling repos),
builds, tests, local file moves/copies, and routine origin-remote git operations are
explicitly judged safe.

Set `defaultMode: advisory` to start the default set in observe-only mode and tune up
from there.

```yaml
# ~/.config/mecatl/settings.yaml  (user-global only — NOT a checked-in project file)
guardrails:
  model: gpt-5-mini          # configuring a model is the opt-in; default rules apply
  minContentBytes: 16        # skip a short INBOUND (post) result; outbound (pre) args are always inspected
  rules:                     # an explicit list REPLACES the default set
    - match: "WebFetch"      # inbound injection on fetched pages
      phases: ["post"]       # "pre" = outbound args, "post" = inbound result; omit = both
      mode: block
    - match: "mcp__*"        # all MCP tools, both directions
      mode: advisory         # observe first, tune later
    - match: "Bash"          # outbound exfil in shell args
      phases: ["pre"]
      mode: sanitize         # trusts the checker's rewrite — use only with a trusted checker
      failClosed: true       # a checker outage treats the content as UNSAFE (default is fail-OPEN)
```

A matcher keys on the tool **name** only (exact > `prefix*` > `*`, most-specific
wins); a tool with no matching rule is unchecked. A checker error/timeout
**fails open** by default (degrade to "no checker" with a WARN; a sustained outage
escalates to a one-time "checker DOWN" sticky WARN); set `failClosed: true` to treat
a checker error as unsafe instead. A checker **saying safe always passes**.

Unlike the headless-only ask reviewer, guardrails fire on the main loop regardless
of `--headless`.

:::warning[Operator-tier only]

The `guardrails:` config is read from the user-global `settings.yaml` and the CLI
**only** — never from a project-tier file. This inverts the usual tighten-only
project gate: a project repo disabling or weakening a security checker would be a
*downgrade*, so a project-tier `guardrails:` block is **ignored with a WARN**. The
subtree is parsed strictly, so a typo cannot silently disable a guardrail. Set the
model with `--guardrails-model` (or a bound `guardrail` model slot); force the whole
layer off with `--guardrails=off`.

:::

---

## The `permission.ask` flow

When Layer 1 resolves to `ask`, the call pauses and the harness surfaces a
`permission.ask` to the client — carrying the tool name, the (clamped, redacted)
arguments, and the human-readable reason. The client responds with a verdict:

- **deny** — the call never runs; the model receives the deny and adapts.
- **allow-once** — the call runs this time only.
- **allow-always** — the call runs and the policy *learns* a per-session rule so the
  same call is not re-asked. A learned allow lives at the lowest scope and can never
  override a deny or a configured ask.

A run that parks awaiting an approval can be approved later — even after a process
restart, the harness re-enters the loop at the pending ask when the verdict arrives.

In a **headless** deployment there is no human to ask. An unresolved ask is
auto-denied by default, with one optional step before that: the
`--subagent-ask-reviewer` (headless-only) inserts a tool-less, one-turn LLM reviewer
that can approve a child's ask for that call only — an allow is always *allow-once*,
never learned, and a deny leaves the child with the same clamped denial reason an
auto-deny would give it. It is fail-safe (any error keeps the call denied), never
delegated a *configured* ask, and deliberately a server flag rather than a config
key — granting an autonomous approval capability is an operator deployment decision,
not something a checked-in project file should switch on. A per-run breaker trips
after 3 consecutive non-allow outcomes (denies, errors, timeouts) — once open, later
asks in that run skip the reviewer and go straight to auto-deny; a single allow
resets the count.

---

## Subagents and the permission model

Children (subagent explorers, team members, parallel branches) get their **own**
scoped ruleset, distinct from the main engine's:

- Child engines default to **allow-all** at a built-in floor — everything runs
  except substitution-floored commands and anything a configured deny/ask gates.
- A top-level **`deny`** binds children too (a deny only ever tightens, so it binds
  everywhere). Top-level `allow`/`ask` are main-only (children are already allow-all).
- A `subagent:` block in the config carries child-scoped rules: `subagent: deny` /
  `subagent: ask` tighten a child command; `subagent: allow` clears a child's
  *substitution-floored* ask (and only when the hidden `$(...)` inners independently
  classify as read-only).
- Under `auto` and `yolo` posture, the allow-all rule is pushed to children too. The
  difference between the two tiers is the **child substitution floor**: `auto` keeps
  it gated (a child's `$(...)` resolves through the child-ask model — injection
  defence on); `yolo` loosens it (a child's substitution auto-runs — defence off).

A project's `subagent:` allows are themselves trust-gated, exactly like its main
allows.

---

## What's next?

- [Hook system](hooks.md) — the lifecycle phases the guardrail checker decorates,
  and how to write your own pre/post-tool and lifecycle hooks.
- [PermissionPolicy extension point](/extension-points/permission-policy.md) —
  implement the port to replace Layer 1's rule logic with your own.
