---
name: user-docs
description: >-
  Authors or updates Mecatl's public user documentation. Use when a change affects
  user-docs/, the Docusaurus site content, or a user-facing behavior that needs
  documentation. It follows the repository's authoring contract and canonical
  tech-writer guidance.
---

# User documentation

Use this skill for public documentation in `user-docs/`, not for internal
architecture records, ADRs, or implementation plans.

## Workflow

1. Read [`user-docs/_README.md`](../../../user-docs/_README.md). It is the
   repository-owned information-architecture, ownership, link, and verification
   contract.
2. Use the sibling [`tech-writer` skill](../tech-writer/SKILL.md). Read its
   canonical [style guide](../tech-writer/references/style-guide.md),
   [anti-patterns](../tech-writer/references/anti-patterns.md), and the reference
   for the page's Diataxis mode before drafting.
3. Decide whether the change belongs in `user-docs/`, then find the existing page
   that answers the reader's question. Extend that page before adding a new one.
4. Verify every behavioral claim against the shipped code and public configuration.
   Read the relevant `docs/architecture/` and `docs/adr/` material named by the
   contract, then verify the deployed surface. `docs/usage/` contains historical
   compatibility pointers and is not a source. A port or constructor does not
   establish availability.
5. Follow the contract for placement, front matter, links, prose, and deployment
   boundaries.
6. Run `task site:build` from the repository root. Report any claim that could not
   be verified instead of guessing.

For Docusaurus infrastructure and commands, consult
[`website/AGENTS.md`](../../../website/AGENTS.md). Keep this skill short: the
authoring contract is its single source of repository-specific rules.
