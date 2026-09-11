---
name: mecatl-learning-config
description: >-
  Configure, write, or tune the mecatl learning: settings policy, including
  learning.mode, sensitivity, reflection budgets, and validated/evaluated
  activation. NOT for running /reflect, reviewing proposals or memories,
  drafting skills, model routing, credentials, evaluator implementation, or
  other harnesses.
---

# mecatl learning configuration

Safely design and merge the operator-tier `learning:` policy after identifying
where mecatl actually runs. Do not assume that the client and server share a
host or configuration.

## Progressive reference use

After deployment and target resolution (Workflow Step 1), read only the needed
parts of [the configuration reference](references/config-format.md):

- [Exact schema and defaults](references/config-format.md#exact-schema-and-defaults)
  always;
- [Budget profiles](references/config-format.md#budget-profiles) only after the
  budget choice;
- [Operator and project tiers](references/config-format.md#operator-and-project-tiers)
  only when project configuration is discussed; and
- [Common errors](references/config-format.md#common-errors) only when validating
  or troubleshooting.

Do not require reading the entire reference at activation. Repository background
is in `user-docs/reference/configuration.md`,
`user-docs/building/deployment/settings.md`, and
`user-docs/features/learning.md`; cite those paths as prose, not links.

## Safety contract

- Resolve the deployment, startup configuration, precedence, and effective target
  before using Read on any settings file. Read only startup artifacts the operator
  identifies or that are already available in the task context; never scan
  processes/services or unrelated files to discover them.
- Read-only inspection and validator preflight are allowed before confirmation.
  The proposed preflight may write only the generated, non-secret `learning:`
  patch under the repository-local `.scratch/`; it is not a settings write and
  must be removed after validation on success, failure, or cancellation. Any
  settings-file creation or modification requires explicit confirmation.
- Inspect an established relevant target with the **Read tool**, never `cat`. Read
  the complete file.
  A missing file is fine. Do not print, log, or rewrite unrelated values,
  comments, credentials, or secret-shaped content.
- Treat malformed or unvalidated YAML as a stop condition. Do not edit until a
  minimal repair is understood and explicitly approved.
- Modify only the top-level `learning:` mapping. Preserve every other byte when
  targeted replacement permits it. Never rewrite the whole file merely to make
  YAML easier to generate.
- Before any write, show the complete proposed `learning:` block and exact
  replacement diff, then ask for explicit confirmation. Silence is not consent.
- If the file is missing, offer manual content or creation of the resolved,
  owner-intended path. Create it only after explicit confirmation. Never add
  credentials.
- The validator is read-only, uses no credentials or network, and must never
  print file contents.

## Workflow

### 1. Establish deployment, startup configuration, and effective owner

Do this before choosing or reading a settings path. First classify the deployment
as local standalone, local embedded mecatui, remote client/server, or engine
embedder. Establish the actual startup source already supplied by the operator or
task: service command, container args/config, systemd unit/overrides, or embedder
code. From that source record, in order:

1. every repeatable `--permission-config PATH` explicit operator file;
2. whether `--permissions-conventional` is enabled; and
3. the server process's `XDG_CONFIG_HOME`, or its `HOME` fallback when relevant.

Do not infer these from defaults when the effective invocation may override them.
If the startup command/unit/container args are not observable from authorized,
relevant context, ask the operator for them. Never scan running processes,
services, containers, or unrelated files.

Resolve the owning operator learning block using mecatl's actual precedence:

- inspect explicit files in CLI order, stopping at the first readable, valid file
  with a non-null top-level `learning:` block; unreadable or invalid explicit files
  are skipped by mecatl and cannot own the effective block;
- only when conventional discovery is enabled and no explicit file captured a
  learning block, consider the user file at
  `$XDG_CONFIG_HOME/mecatl/settings.yaml`, falling back to
  `$HOME/.config/mecatl/settings.yaml` when `XDG_CONFIG_HOME` is unset or empty;
  its readable, valid `learning:` block is then the owner; and
- files after the owner, and a conventional user file shadowed by an explicit
  owner, have no effect on operator learning. Never edit one of them.

Apply that resolution by deployment:

- **Local standalone or embedded mecatui:** resolve against the local server's
  actual invocation and environment, not merely the interactive shell's.
- **Remote mecatui/connect:** resolve only from server startup args and the server
  host's environment/files. Never inspect or edit local client settings. Without
  authorized server access, provide the exact block and server-side instructions.
- **Engine embedder:** configuration is code-owned unless the embedding
  application explicitly constructs `permconfig` from files. Ask the owner how
  it is supplied; do not invent a `mecated` path.

If no current block owns learning, the insertion target is the first
operator-intended explicit file chosen by the operator, or the conventional user
file only when conventional discovery is enabled. If ownership or an effective
insertion target cannot be established, do not edit automatically: provide a
manual block plus instructions to install and validate it on the owning server or
in embedder code. Record the established path privately as `<resolved-path>`.

### 2. Inspect and validate the current document

Read the complete target and classify it as missing, valid without `learning:`,
valid with `learning:`, or malformed. Retain the existing mapping, comments,
indentation, and byte range for a later targeted edit. Report only
learning-related findings.

On the server host, validate the target without exposing its content. Pass the
resolved path as one quoted argument; never concatenate it into shell syntax, a
command string, or another argument:

```text
mecated config validate --file "<resolved-path>"
```

The command reads at most 256 KiB and prints only `valid` or a sanitized error.
It requires the file to exist unless a learning patch is supplied. If `mecated`
is unavailable on a remote host, do not auto-edit: give the exact block and
server-side installation guidance, including actual-file validation after the
operator makes the change.

### 3. Elicit one answer at a time

Ask in this order. Wait after every question. Each prompt must show all prior
answers and the recommended default:

```text
✓ Autonomy: <answer>
✓ Sensitivity: <answer>

→ <one current question>
  ▸ <recommended choice> (recommended — <brief reason>)
    <other choices>
```

1. **Desired autonomy:** Review first / Auto / Off. Recommend **Review first**
   for rollout. If the operator explicitly asks for autonomous learning,
   recommend **Auto** instead. Off disables observation, not explicit tools.
2. **Sensitivity:** conservative / balanced / eager. Recommend **balanced**.
   This is independent of budget posture: any sensitivity may be combined with
   any budget profile.
3. **Skill activation assurance:** ask this when mode is Auto. Recommend
   **validated** for usable, body-only, evidence-backed autonomy. Offer
   **evaluated** only when a trusted fixture evaluator is configured; it
   requires PASS. For Review/Off, record future policy or accept validated as an
   inert default without implying activation.
4. **Budget posture:** default / budget-conscious / eager / custom. Profiles set
   only the six automatic budget values; they never choose sensitivity. Explain
   that limits and cooldowns are process-local, so N replicas may reserve about
   N times the aggregate. They are not provider quotas or cluster-global limits.
   For custom, ask one automatic value at a time and validate it; do not ask for
   sensitivity again.
5. **Existing subtree:** when present, preserve or revise it. Recommend
   **preserve and revise only selected fields**. When absent, state that a new
   subtree will be inserted.

Support `back` to revise the preceding answer. Do not collapse these into one
questionnaire.

### 4. Explain the selected behavior

Before proposing YAML, summarize mode × activation accurately:

- **Off:** no automatic observation/reflection. Explicit `/reflect`, memory and
  user-model tools, and `SkillDraft` remain available. Direct `SkillDraft`
  creates inactive content. Dream consolidation is independently configured.
- **Review:** admitted completions spend reflection capacity and stage durable
  proposals; they do not promote memory or activate learned skills.
- **Auto:** facts retain evidence, ownership, and conflict gates. With
  `validated`, a structurally safe, body-only, evidence-backed exact candidate
  activates on PASS or ABSTAIN/no evaluator. With `evaluated`, only trusted
  evaluator PASS activates. FAIL and evaluator ERROR never activate.

State separately that sensitivity selects automatic admission thresholds while
budgets bound automatic frequency and reserved tokens; users may combine any
sensitivity with any budget posture. Explicit `/reflect` bypasses automatic
sensitivity, cooldown, and count/token budgets, but retains coordinator,
provider, timeout, ownership, and lifecycle limits.

### 5. Preflight, propose, confirm, then edit

Generate a complete `learning:` block containing mode, independently selected
sensitivity, skill activation, and all six automatic controls. Do not emit a
partial subtree whose behavior depends on hidden defaults.

Before asking for confirmation, write only that exact non-secret `learning:`
block to a bounded, repo-local scratch name such as
`.scratch/learning-preflight.yaml`, then run the read-only in-memory preflight
with both paths quoted:

```text
mecated config validate --file "<resolved-path>" \
  --learning-patch ".scratch/learning-preflight.yaml"
```

The scratch file is not the settings write: it contains only the exact block
already shown, never a full settings copy, credentials, or unrelated values. The
command bounded-reads both files, requires the patch to be a single YAML document
with exactly one top-level `learning:` mapping, replaces or inserts only that node
in memory, and validates the resulting complete document through mecatl's parser.
It prints `valid` or `valid (new file)` and never writes either input. Remove the
scratch patch immediately after validation on success, failure, or cancellation.
Stop if preflight fails.

If the user requires no scratch write, provide the complete block for manual
application and require actual-file validation after the write instead; do not
claim a write-free proposed preflight was run.

Then show:

1. the complete `learning:` block;
2. an exact diff replacing only the existing mapping, or exact insertion point;
3. a concise effects/cost summary; and
4. `Apply this exact change to <resolved-path>? (yes/no)`.

Only explicit yes authorizes an edit. Re-read the complete target immediately
before editing. If it differs from the preflight input, stop, regenerate the
learning-only scratch patch, rerun `mecated config validate --file
"<resolved-path>" --learning-patch ".scratch/<bounded-name>.yaml"` against the new
bytes, remove the scratch patch, and ask again. Use a targeted exact replacement
preserving unrelated YAML and comments. If safe targeting is impossible, offer
manual merge rather than rewriting the file.

After writing, validate the actual file:

```text
mecated config validate --file "<resolved-path>"
```

A nonzero result is a failed application requiring immediate, minimal repair
guidance; do not report success. Re-read and verify the selected learning values
without showing unrelated content.

### 6. Restart and observe

Learning settings are resolved at build time: restart the local server, remote
server, or embedder instance that owns the resolved policy.

- Local embedded mecatui: `/learning` cycles mode and
  `/learning-sensitivity` cycles sensitivity; both save locally and still
  require restart. Use the full file for budgets and activation assurance.
- Remote mecatui/connect: change and restart the server host, never the client.
- Engine embedders: follow the application's configuration/rebuild lifecycle.
- After restart, inspect `/reflections`, `/skills`, and `/usermodel`; use
  `/reflect` only when an intentional live run is desired. Warn that reflection
  may make provider calls and incur token cost; verification need not force it.

Offer rollback blocks from the reference: `mode: off`, `mode: review`, and the
high-assurance `skills.activation: evaluated` tightening.

## Error handling

| Situation | Required response |
|---|---|
| Startup configuration not observable or ownership unresolved | Ask for the server command/unit/container args; otherwise supply a manual block and server/embedder validation instructions with no automatic edit. |
| Earlier explicit file has a valid `learning:` block | It owns learning; do not edit a later explicit or conventional file. Explicit files are evaluated in CLI order. |
| Explicit files have no captured block and conventional discovery is disabled | The XDG user file does not participate. Ask the operator to choose an explicit insertion target or provide a manual block. |
| Missing file | Offer manual block or confirmed creation at the established `<resolved-path>`; do not guess ownership. |
| Malformed, duplicate, unknown, or unvalidated YAML | Stop before editing; validate the complete document and obtain approval for minimal repair. |
| Invalid value or duration | Reject using the exact schema; `window` must be `1m..24h`. |
| Any maximum is zero | Warn that this bound disables all automatic reflection; cooldown zero only removes cooldown. |
| Evaluated without evaluator | Warn that ABSTAIN/no-evaluator remains staged; recommend validated or separately configure a trusted evaluator. |
| Project raises mode/sensitivity or loosens evaluated | Explain project policy is tighten-only and cannot raise operator autonomy. |
| Project specifies automatic budgets | Explain budgets are operator-only and the project block is warning-ignored. |
| Multiple replicas | Multiply the process-local envelope by replica count; never call it a cluster/provider quota. |
| Remote server inaccessible or `mecated` unavailable there | Supply the exact manual block and server-side instructions only; never inspect local client settings or auto-edit. Require `mecated config validate --file "<resolved-path>"` after installation when the binary becomes available. |
| Routing, credentials, evaluator implementation, `/reflect` execution, proposal/memory review, skill drafting, or another harness | Decline that portion and route to its workflow. |
