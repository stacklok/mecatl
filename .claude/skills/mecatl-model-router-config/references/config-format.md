# mecatl model router config format

The operator-tier `~/.config/mecatl/settings.yaml` `models:` subtree. Operator-tier
ONLY — a project-tier `models:` block is ignored with a WARN unless an operator
allowlist exists (Phase 4).

## Complete schema

```yaml
# ~/.config/mecatl/settings.yaml
models:
  aliases:                 # short names → concrete ids (the spine everything else references)
    <alias>: <provider/model-id>

  default: <provider/model-id>    # the session model

  slots:                   # route housekeeping + plan-mode to a cheaper/stronger model
    compaction: <selector>        # the compaction tier-4 summary call
    ask-reviewer: <selector>      # the headless child-ask reviewer
    guardrail: <selector>         # the LLM content checker
    plan: <selector>              # plan-mode turns swap to this model (opusplan pattern)
    router: <selector>            # the semantic subagent model-router classifier

  router:                  # pick a subagent's model per task (the taxonomy enables it)
    # disabled: true             # optional kill-switch: keep the taxonomy but turn routing off
    default-category: <name>
    classifier-slot: <selector>  # optional: override the classifier's own slot (default: router slot / cheap tier)
    categories:
      - name: <name>
        description: <one-line summary the classifier reads>
        model: <selector>         # alias / slot / concrete id

  allowlist:               # optional: operator cap for project-tier overrides (Phase 4)
    - <alias or concrete id>
```

## Key rules

- **Provider is FIXED per session.** All aliases/slots/categories pick a MODEL
  within the session's provider — never switch providers. Point every alias at a
  model id on the SAME provider (e.g., all OpenRouter ids, or all Anthropic ids).
- **`<selector>`** everywhere is an alias name OR a concrete id, resolved through
  the alias map (`lookupModelAlias`). Using aliases (not raw ids) in categories is
  the idiomatic pattern.
- **Slot defaults**: `compaction`/`ask-reviewer`/`guardrail`/`router` default to
  the `cheap` tier; `plan` defaults to `reasoning`. Tier keys (`cheap`/`fast`/
  `reasoning`) are optional aliases you may define.
- **Router taxonomy = enable.** A non-empty `categories:` list turns the router ON
  (configure = enable, the guardrails-parity model). `disabled: true` or
  `--subagent-model-router=false` turns it OFF while keeping the taxonomy.
- **Category `model:`** maps through the alias machinery — use an alias name, not a
  raw id, for consistency with the alias-spine pattern.
- **Router precedence** (by gating): per-call `model` > agent-def `Model` > fork/resume
  > router > inherited default. The router fills the gap; it never overrides pinned
  intent.
- **Fail-soft on slots**: a typo'd slot/alias WARNs and degrades to the session
  model — never wedges the call. `--subagent-model` is fail-FAST (build error).
- **3–4 categories max.** More categories degrade classifier accuracy and widen the
  steering surface (untrusted task prompts can steer toward the most expensive
  category). Difficulty-tier categories (large/medium/small) map cleanly to model
  tiers; a 4th distinct-class category (e.g. `image` for multimodal) is the sane
  upper bound.

## Worked example (OpenRouter, cost-tiered)

```yaml
models:
  aliases:
    heavy: z-ai/glm-5.2
    coder: deepseek/deepseek-v4-pro
    quick: deepseek/deepseek-v4-flash
    image: google/gemini-3.5-flash        # multimodal — invoke via per-call model: image or router

  default: z-ai/glm-5.2

  slots:
    compaction: quick
    ask-reviewer: quick
    guardrail: quick
    plan: heavy
    router: quick

  router:
    default-category: medium
    categories:
      - name: large
        description: >
          Deep multi-step reasoning, architecture design, subtle concurrency
          bugs, system-level planning. The hardest problems.
        model: heavy
      - name: medium
        description: >
          Standard implementation work — writing features, fixing bugs, writing
          and reviewing tests, refactoring within a known module.
        model: coder
      - name: small
        description: >
          Trivial mechanical tasks — single-file edits, renames, quick lookups,
          reading a file, running a known command.
        model: quick
      - name: image
        description: >
          Any task that involves visual input — screenshots, UI mockups, design
          specs, diagrams, charts, error output rendered as an image, or visual
          debugging where the model must look at what's on screen. Use this
          when the task prompt references an attached image, a pasted
          screenshot, a rendered layout, or any content that cannot be
          understood from text alone.
        model: image
```

## Verifying it's wired

On startup `mecated` logs one build-once fact per active slot
(`model slot ACTIVE`) and, when the router is enabled, `subagent model router
ACTIVE` (or a `DISABLED` WARN if the kill-switch is set). Check the mecated log
(stderr, or `$XDG_STATE_HOME/mecatl/mecatui.log` under mecatui). A slot that
failed to resolve WARNs and degrades to the session model — a missing `ACTIVE`
line is the signal something didn't bind.

[← back to the skill](../SKILL.md)
