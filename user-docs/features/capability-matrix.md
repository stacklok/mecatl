---
sidebar_position: 2
title: Capability and deployment matrix
description: Check which mecatl capabilities are available in each deployment shape.
---

# Capability and deployment matrix

This page will be the source of truth for capability availability across
mecatl deployment shapes.

:::note[Documentation scaffold]

The inventory is being reconciled against the current source tree and complete
Git history. Classification labels are deliberately left provisional until
intentional local-only designs are separated from remote implementation gaps.

:::

## Classification

- **Portable** means a core port or wire contract exists and at least one
  non-filesystem implementation satisfies it.
- **Local-only by design** means the capability is inherently tied to a local
  shell, checkout, process, editor, or host namespace.
- **Local-only gap** means a local implementation exists, but no structural
  barrier prevents a remote implementation and none is currently wired.

## Deployment shapes

The matrix will cover these shapes:

- `mecated`
- `mecak8s`
- `mecatequi`
- `mecatui`
- Embedded `engine`
- gRPC and HTTP clients

## Inventory

| Capability | mecated | mecak8s | mecatequi | mecatui | Embedded engine | Classification |
| --- | --- | --- | --- | --- | --- | --- |
| _Inventory under review_ | - | - | - | - | - | - |

## Evidence and decisions

Every row must be traceable to a current flag, configuration key, port, tool,
composition path, or driver service. Historical implementation commits that
are not ancestors of the current product do not establish availability.

Open classification decisions will be recorded here only after review. They
will not be inferred from ADR titles or implementation filenames alone.

## Related information

- [Features](./index.md)
- [Deployment decision](../getting-started/deployment-decision.md)

