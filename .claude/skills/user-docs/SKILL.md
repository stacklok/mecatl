---
name: user-docs
description: >-
  Authors or updates Mecatl's public user documentation. Use when a change affects
  user-docs/, the Docusaurus site content, or a user-facing behavior that needs
  documentation. It follows the repository's lightweight authoring contract.
---

# User documentation

Use this skill for public documentation in `user-docs/`, not for internal
architecture records, ADRs, or implementation plans.

## Workflow

1. Read [`user-docs/_README.md`](../../../user-docs/_README.md). It is the
   repository-owned authoring and information-architecture contract.
2. Decide whether the change belongs in `user-docs/`, then find the existing page
   that answers the reader's question. Extend that page before adding a new one.
3. Verify every behavioral claim against the shipped code and public configuration.
   Read the relevant `docs/architecture/`, `docs/usage/`, and `docs/adr/` material
   named by the contract. A port or a constructor does not establish availability.
4. Follow the contract for placement, front matter, links, prose, and deployment
   boundaries. If the shared `tech-writer` skill is available, it can provide
   additional editorial guidance, but it is not required.
5. Run `task site:build` from the repository root. Report any claim that could not
   be verified instead of guessing.

For Docusaurus infrastructure and commands, consult
[`website/AGENTS.md`](../../../website/AGENTS.md). Keep this skill short: the
authoring contract is its single source of repository-specific rules.
