# Example skills

This directory ships two small, real skills under `skills/` — copy either into a
`--skills-dir` tree (or the conventional `<workspace>/.mecatl/skills` location)
to make it active, or read them as reference shapes for writing your own.

A skill is a directory laid out as `<name>/SKILL.md`; see
[Skills, commands, and soul](../../user-docs/features/skills-commands-and-soul.md) for the discovery
rules, the trust boundary, and the `SkillDraft` → promote loop.

| Skill | Purpose | SKILL.md |
| --- | --- | --- |
| `commit-style` | Write Conventional-Commits messages with the required `Co-Authored-By` trailer for this repo. | [skills/commit-style/SKILL.md](skills/commit-style/SKILL.md) |
| `go-table-tests` | Write idiomatic table-driven Go tests that stay offline and follow this repo's conventions. | [skills/go-table-tests/SKILL.md](skills/go-table-tests/SKILL.md) |
