### File-based permission config (`.mecatl/settings.yaml`)

The built-in permission policy (read-only tools allowed; `Bash`/`Edit`/`Write`/
`Team`/`SkillDraft` ask) can be tuned per project and per user with config files,
**re-resolved per session** against each session's workspace root by the
permission-config resolver. So two sessions running in different repos
under the same `mecated` get **different** decisions for the same tool call.

**Schema** — `.mecatl/settings.yaml` (the checked-in, shared file),
`.mecatl/settings.local.yaml` (a gitignored personal override at a higher scope),
and the user-global file all share the same shape, mirroring Claude-Code's
permissions:

```yaml
permissions:
  allow:
    - "Bash(go test:*)"   # Claude "prefix:*" form, normalised to the glob "go test*"
    - "Bash(go build*)"   # native mecatl glob
    - "Read"              # bare tool name = tool-wide
  ask:
    - "Bash(git push:*)"
  deny:
    - "Bash(rm:*)"        # deny wins absolutely, in any scope
  subagent:               # child-scoped rules: bind ONLY subagent/member/branch engines
    allow:
      - "Bash(go generate:*)"   # clears a child's substitution-floored ask (see "Compound-Bash & substitution safety" below) ONLY when the $(...) inners are read-only
    ask:
      - "Bash(go test:*)"       # a configured child Ask is NEVER auto-approved by isolation
    deny:
      - "Bash(curl:*)"
```

Each entry is a rule spec `Tool(pattern)` or bare `Tool`. Patterns use the
evaluator's glob grammar; the Claude `prefix:*` / `prefix:` form is normalised to a
`prefix*` glob. Config rules use **glob** semantics (`Exact:false`) — only LEARNED
"allow always" rules are exact. **The `permissions:` subtree parses STRICTLY**: an
unknown key inside it (`alow:`, `subagnet:`, …) is a loud parse error and the file
is skipped (logged), never silently-ignored config; the file's top level stays
lenient (`trustedWorkspaces:` etc. keep parsing).

**Audience × effect** — which engine class each bucket binds.

**Baseline first:** child engines (Subagent children, team members, Parallel
branches) default to **allow-all** — everything runs except substitution-floored
commands (see *Compound-Bash & substitution safety* below) and anything a
configured deny/ask gates. So `subagent: allow` is NOT "let children run X" —
children already run X; it matters ONLY for *clearing a child's
substitution-floored ask*. `subagent: deny` / `subagent: ask` *tighten* (block or
gate a child command the floor would otherwise allow).

| Bucket | Main engine | Subagents (Subagent children / team members / Parallel branches) |
| --- | --- | --- |
| top-level `deny` | yes | **yes** (a deny only tightens — it binds everywhere) |
| top-level `allow` / `ask` | yes | no (children are already allow-all; see baseline above) |
| `subagent: allow` | no | yes — clears a child's **substitution-floored** ask (see below) when the hidden inners are read-only |
| `subagent: ask` | no | yes — gates a child command; a configured child Ask is never auto-cleared (it surfaces to the human, or auto-denies headless) |
| `subagent: deny` | no | yes |

The `subagent: allow` clearing is **bounded** (it relaxes the substitution floor
without trusting what a substitution hides): the allow vouches **only for the
OUTER command** — every command hidden inside `$(...)`/backticks must
independently classify **positively read-only** (`go test $(git rev-parse HEAD)`
clears; `go test $(anything-else)` surfaces/denies), and the outer must pass the
worktree-escape rejections (no `git push/config/remote/fetch/pull/clone/worktree/submodule`,
no path-bearing `git -C`/`--git-dir`/`--work-tree`, no `go … -exec/-toolexec/-overlay/-o`)
as defense-in-depth.

Child engines resolve **project** rules against their **session's workspace
root** (per-session engines re-pin at session-engine assembly; the shared
default engine pins the server root it was built for) — never against their
forked worktree/copy roots (a worktree lacks the gitignored
`settings.local.yaml`, and per-fork resolution would defeat the cache). User/CLI
rules apply to children as usual. A typo inside the `permissions:` subtree skips
the whole file (deny/ask included) — the WARN names the lost per-effect rule
counts. Under `--yolo` the allow-all **rule** binds children too (so the main and
child rulesets stay in lock-step), but the substitution-floor **loosening**
remains **main-only**: it never loosens a child's substitution floor — a child's
`$(...)`/backtick/heredoc command still resolves through the child-ask model.

**Headless ask review (`--headless` + `--subagent-ask-reviewer`).** On a
**headless** run (no human approver — mecated started with `--headless`), a child
ask that nothing above resolved is normally **blanket auto-denied**. The opt-in
reviewer inserts an automated step before that deny: a tool-less, one-turn LLM
reviewer (one extra LLM call on the reviewer model) examines the command — fenced
as untrusted data, with claims of prior approval inside it declared void, and the
verdict accepted only when the reviewer's *whole* reply is a single
`{"allow":…}` object so a forged verdict echoed inside the command can't be lifted
out — against the built-in read-only/verification rubric (or your
`--subagent-ask-reviewer-policy` file) and either approves **this call only** or
keeps it denied; any reviewer error/timeout/ambiguity also keeps it denied
(**fail-safe** — the reviewer is never load-bearing for safety), and a per-run
circuit breaker (`--subagent-ask-reviewer-max-denies`) bounds reviewer spend.

**Reachability:** the reviewer fires **only** when there is no human to ask — i.e.
under `--headless`. A normal interactive mecated (the default) **and the `mecatui`
embedded server** (which is always interactive — a human sits at its approval
modal) surface every unresolved child ask to the client/modal for a person to
answer, so a reviewer configured there is inert and the server logs a startup
WARNING to that effect. To actually use the reviewer, run `mecated --headless
--subagent-ask-reviewer …` (and point `mecatui connect <address>` at it if you
want the TUI — ADR 0089 removed the inert `--subagent-ask-reviewer*` flags from
mecatui). The flag's model is still validated at startup even when inert, so a
typo is caught immediately rather than the day `--headless` is added.

Two hard bounds keep it subordinate to this section's rules: a **configured
`subagent: ask` is never delegated to the reviewer** (a deliberately-configured
Ask demands a *human* approver — it surfaces interactively or auto-denies
headless, exactly as the table above says), and a configured `deny` resolves
before any ask exists. It is deliberately a **server flag, NOT a `permissions:`
config key**: it grants an autonomous approval capability, which must be an
operator *deployment* decision — a (project-tier, possibly checked-in) settings
file must never be able to switch on a mechanism that approves commands by itself.

**Scope → location** (highest precedence first; see `engine/governance` Scope):

| Scope | Location | Trust |
| --- | --- | --- |
| `ScopeCLI` | each `--permission-config <file>` | fully trusted |
| `ScopeLocalProject` | `<workspace>/.mecatl/settings.local.yaml` (gitignored, personal); `<workspace>/.claude/settings.local.json` with `--import-claude-permissions` | **trust-gated** |
| `ScopeSharedProject` | `<workspace>/.mecatl/settings.yaml` (checked-in, shared); `<workspace>/.claude/settings.json` with `--import-claude-permissions` | **trust-gated** |
| `ScopeUser` | `$XDG_CONFIG_HOME/mecatl/settings.yaml` (or `~/.config/...`); `~/.claude/settings.json` with `--import-claude-permissions` | fully trusted |
| `ScopeBuiltinDefault` | the built-in floor (read-allow / mutate-ask) | n/a — lowest precedence |

The resolver re-reads project files **per session** against the session's workspace
root, and **revalidates** its per-root cache on the config files' mtime/size — so a
`deny` added mid-process takes effect on the next call, not at restart. Among
ask-vs-allow the configured **higher scope wins**, with ONE narrow exception: a
higher-scope config **Allow loosens ONLY the built-in `ScopeBuiltinDefault` Ask**
floor (e.g. allowing `Bash(go test:*)` relaxes the built-in Bash ask). It can never
suppress a **configured** Ask, **deny/ask in any scope still beats an allow**, and a
learned allow can never out-rank a configured deny/ask. Plan mode still hard-denies
mutations first. Config files are size- and rule-count-capped (defense-in-depth).

**The trust gate** — a project's config is part of the repo the model is editing.
Its **DENY and ASK** rules are **always** honoured (they only tighten). Its
**ALLOW** rules (shared AND local, **including `subagent:` allows**) are honoured
**only with `--trust-project`**; otherwise they are dropped (and logged) so a
checked-in `settings.yaml` cannot auto-approve tool calls in an untrusted repo —
for the main engine or its children. User-global and `--permission-config`
(CLI) files are the operator's own and are always fully trusted.

**Memory + soul are pre-approved at the floor** — the six memory tools
(`Remember`/`Recall`/`SearchMemory` and the cross-project `RememberUser`/`RecallUser`/
`SearchUserModel`) and the synthetic `soul:apply` action are explicit
`ScopeBuiltinDefault` Allows in the built-in ruleset, so by default they **do not
prompt**: an agent recalling and saving its own facts, and applying the operator's
soul, is part of "having a memory/identity", not a workspace mutation. They are
explicit (auditable in source + the `... ENABLED ...; permission: allow (built-in
default, overridable ...)` startup logs) and **overridable** — being the lowest scope,
any higher-scope config Ask/Deny wins. To require approval (or block) one, add it to a
`settings.yaml`:

```yaml
permissions:
  ask:
    - "Remember"     # require approval before the agent writes project memory
  deny:
    - "soul:apply"   # withhold the soul entirely this deployment
```

`soul:apply` is consulted at **soul-load (build time)**, not per tool call: `allow`
applies the soul, `deny` withholds it, and `ask` also **withholds** it (with a warning)
because there is no interactive gate at build time — set it back to `allow` to apply.

**Claude import is lossy** (`--import-claude-permissions`) — every lossy outcome is
logged:

| Claude spec | Outcome |
| --- | --- |
| `WebFetch(domain:x)` in an **allow** list | **demoted to `ask`** (domain/substring match is too risky to auto-allow) |
| bare `WebSearch` in an **allow** list | imported **verbatim** as `allow` — **no demotion** (its outbound payload is a query string, lower-risk than `WebFetch`'s arbitrary-URL fetch; egress is already provider-gated by `--websearch-url`) |
| `Read(~/...)` (leading `~`) | kept but **inert** — the `~` is left unexpanded, so it never matches the absolute path a tool resolves |
| unparseable spec | **dropped** |

The import never widens: a demotion only ever moves `allow → ask`, and the
`deny`/`ask` buckets import verbatim.

> **`mecatui` defaults match `mecated`.** The embedded TUI server sets
> `--permissions-conventional` and `--import-claude-permissions` ON, but
> `--trust-project` is **OFF by default** — unified with
> `mecated`. So a project's ALLOW rules and its project soul are honoured only when
> you pass `--trust-project` to `mecatui`; deny/ask are always honoured regardless.
> (Earlier builds hardcoded trust ON for the TUI; that blanket-trust regression is
> gone.) The `mecated` daemon likewise defaults `--permissions-conventional` ON but
> `--trust-project` OFF (the safe stance); it defaults `--import-claude-permissions`
> OFF (the safe network stance).

---

## 12. Permissions

### How a decision resolves

Each tool call is evaluated against a merged set of `Rule`s. A `Rule` is
`{Scope, Tool, Pattern, Effect}` where `Effect` is `deny`, `ask`, or `allow`,
`Tool` empty matches any tool, and `Pattern` empty matches any args (otherwise a
shell-style glob, with an exact-match fast path, over the canonicalized
command/argument string).

Resolution precedence:

1. **`deny` → `ask` → `allow`**: a `deny` in *any* scope beats an `ask` or
   `allow` anywhere; otherwise an `ask` beats an `allow`.
2. **Scope** breaks same-effect ties (highest precedence first):
   `Managed > CLI > LocalProject > SharedProject > User > BuiltinDefault`. (One
   narrow exception to ask-beats-allow: a higher-scope config **Allow** may
   loosen **only** the built-in `ScopeBuiltinDefault` Ask floor, never a
   *configured* Ask — see the permission-config note in §3.)
3. **No matching rule → `ask`** — the safe default. The harness never silently
   allows an unconfigured call.

A `deny`/`ask` carries a human `reason`, surfaced to the model (on deny, so it
can adapt) and to the client (on ask).

### The default ruleset `mecated` ships

| Tool | Default effect |
| --- | --- |
| `Read`, `Grep`, `Glob`, `WebFetch`, `WebSearch`, `Subagent` | `allow` |
| the six memory tools (`Remember`/`Recall`/`SearchMemory`, `RememberUser`/`RecallUser`/`SearchUserModel`) | `allow` (floor-scoped, config-overridable — see §3) |
| `InspectSubagent`, `InspectMember`, `SubagentStatus` (read-only child observability) | `allow` (floor-scoped, config-overridable) |
| `soul:apply` (the synthetic soul-load action) | `allow` (floor-scoped, config-overridable — see §3) |
| `Bash`, `Edit`, `Write`, `Team`, `SkillDraft` | `ask` |

Read-only exploration runs without interruption; anything that can mutate the
workspace pauses for approval (`Team` asks because it can spawn **mutating**
members, unlike the read-only `Subagent` explorer).

### Plan mode hard-denies mutations

When a session is in `plan` mode, the evaluator gates *before* the rule engine:

- `Edit` and `Write` are unconditionally **denied** (they always mutate).
- A `Bash` command that is **not** read-only is **denied**; read-only Bash and
  the read-only tools (`Read`/`Grep`/`Glob`) fall through to the rules.

The deny reason tells the model to present a plan and exit plan mode first.

### Compound-Bash & substitution safety

For `Bash`, the evaluator splits compound command lines and requires **every**
sub-command to pass; the **worst** outcome wins. So `git status && rm -rf /`
inherits the deny/ask from the `rm` segment even if `git status` would be
allowed. Any segment containing command/process substitution or subshell
grouping (which could smuggle a hidden inner command past the splitter) is
floored at **`ask`** — an allow rule for the outer literal can never silently
approve a concealed destructive command.

> The shipped permission rules are baked into the binary (the shared composition layer used by both `mecated` and the embedded `mecatui` server). There is no rules config file in v1; changing the built-in policy requires editing the source and rebuilding.

---

See also: [workspace trust & posture](workspace-trust.md), [hooks](hooks.md),
or the [operator guide index](../usage.md).

