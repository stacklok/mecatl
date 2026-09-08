---
id: 05-environment-ledger-separation
title: Separate Workspace content from Environment ledger ownership
blocked_by: [04-docs-and-layering]
status: done
branch: "plan-persistent-read-before-write-ledgers/05-environment-ledger-separation"
worktree: ""
issue: "888"
retries: 0
last_error: ""
accumulator: acc/persistent-read-ledgers
---

# Repair-wave brief

Resolve the panel's architectural blocker by removing `RecordRead` and `RecordedVersion` from `tool.Workspace` and carrying a mandatory, independently selected `tool.ReadLedger` on `tool.Environment`. File tools obtain content/version/CAS from `env.Workspace()` and evidence from `env.ReadLedger()`, keyed with the existing I/O-free `tool.LedgerKey`. Default/main session composition supplies a fresh memledger. Every child gets a fresh child ledger: isolated forks pair it with the fork Workspace; base-sharing/direct-write children retain the exact parent content backend and appropriate runner through any required authority-narrowing Workspace view, never reconstructing osfs from `Root()` and never falling back to the parent ledger. Preserve all content namespaces, ACP buffers, path-escape containment, shell semantics, final CAS, no-fs behavior, and provider-neutral boundaries. Replace public `FileVersion.Token()` with a narrow persistence/transport codec that rejects invalid zero versions while preserving valid empty opaque tokens. Update API baseline/changelog but not living/acceptance/ADR docs.

## Repair acceptance

- Workspace contains only file-content/search/versioned-mutation operations; Environment exposes non-null Workspace and non-null ReadLedger as separate capabilities.
- Direct-write/base-sharing children retain the exact parent content backend through any stricter child authority view and get a fresh ledger; forked children use their fork content backend and a fresh ledger.
- No failure path silently substitutes osfs or inherits a parent ledger.
- The narrow FileVersion serialization path round-trips valid empty/non-empty tokens and rejects invalid zero values without exposing an interpretation API.
- Existing and new child, ACP, file-tool, environment, and module/API gates pass.
