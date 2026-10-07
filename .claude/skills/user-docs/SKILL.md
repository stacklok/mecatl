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
   canonical [style guide](../../../user-docs/_STYLE.md),
   [anti-patterns](../tech-writer/references/anti-patterns.md), and the reference
   for the page's Diataxis mode before drafting.
3. Decide whether the change belongs in `user-docs/`, then find the existing page
   that answers the reader's question using the contract's ownership map. Terminal
   workflows belong in `mecatui/`, deployment and operations in `operating/`, and
   application integration in `building/`. Shared capability behavior belongs in
   `features/`; exact contracts belong in `reference/`. Extend the canonical page
   before adding a new one, and link to shared behavior from each journey.
4. Verify every behavioral claim against the shipped code and public configuration.
   Read the relevant `docs/architecture/` material named by the contract, then
   verify the deployed surface. A port or constructor does not establish
   availability. Follow the contract's generated-reference rules: change the
   owning source and run its generator instead of editing generated pages.
5. Follow the contract for placement, front matter, links, prose, and deployment
   boundaries. Preserve published URLs when splitting guides into workflow pages;
   follow the website instructions for moves. Keep roadmap content separate from
   shipped behavior and contributor qualification material in internal docs.
6. Run `task site:build` from the repository root. Report any claim that could not
   be verified instead of guessing.

For Docusaurus infrastructure and commands, consult
[`website/AGENTS.md`](../../../website/AGENTS.md). Keep this skill short: the
authoring contract is its single source of repository-specific rules.
