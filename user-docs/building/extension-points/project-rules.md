---
sidebar_position: 9
title: Project rules
description:
  Supply trusted, optionally path-scoped project guidance through RulesSource.
---

# Project rules

Implement `prompt.RulesSource` to supply project or user guidance from a custom
backend. Mecatl includes `engine/adapter/rulesfs` for Markdown files under
conventional project and user directories.

For the user-facing behavior, see
[Project instructions and rules](/features/project-instructions-and-rules.md).

## The interface

```go
type RulesSource interface {
    ListRules(ctx context.Context) ([]Rule, error)
}

type Rule struct {
    Name   string
    Body   string
    Paths  []string
    Origin RuleOrigin
}
```

`ListRules` returns a name-sorted, deduplicated snapshot. An empty slice means
that no rules apply. The prompt assembler treats a source error as unavailable
guidance and continues the run.

Each source must limit `Body` to `prompt.MaxRuleBytes` (20 KiB). `Origin` is an
admission tier (`project`, `user`, or `driver`), not a filesystem path or URL.

`Paths` contains optional glob conditions. Mecatl includes these conditions in
the prompt and relies on the model to apply them. An empty `Paths` slice makes a
rule unconditional.

## Use the filesystem adapter

`rulesfs.ResolveSources` searches these locations in descending precedence:

1. `<WORKSPACE>/.mecatl/rules`
1. `<WORKSPACE>/.claude/rules`
1. `$XDG_CONFIG_HOME/mecatl/rules`, or `~/.config/mecatl/rules`
1. `~/.claude/rules`

The first rule with a given name wins. Project directories are included only for
trusted workspaces. User directories are always eligible. Missing directories
have no effect, and malformed files are skipped with a diagnostic.

## Rule file format

The filename stem supplies the rule name. The Markdown file can include a
`paths` field in YAML front matter:

```markdown
---
paths:
  - '**/*_test.go'
---

# Testing rule

Run Go tests before reporting that a change is complete.
```

`paths` accepts a YAML sequence, one scalar glob, or a comma-separated scalar.
Omit it to make the rule unconditional.

## Apply prompt limits

`prompt.RulesAssembler` injects at most 32 rules and 40 KiB of combined rule
content. Content beyond either limit is omitted and reported in a warning. Rule
loading fails soft: an unavailable source does not abort a run.

## Next steps

- [Configure project instructions and rules](/features/project-instructions-and-rules.md).
- [Implement an agent-definition source](agent-definitions.md).
- [Provide skills through SkillSource](tool-catalog.md#provide-skills).
