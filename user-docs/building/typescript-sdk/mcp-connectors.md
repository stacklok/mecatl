---
title: Enroll MCP workspace services
description:
  Inspect MCP connector availability and enroll workspace services from a
  TypeScript application.
sidebar_position: 9
---

# Enroll MCP workspace services

Use a session's MCP connector inventory to decide whether your application
should offer workspace enrollment. Your application controls when to observe,
retry, or cancel enrollment and how to present an authorization URL.

## Prerequisites

Before you begin, connect an authenticated Mecatl `Client` and obtain a bound
`Session` that the same principal owns. Keep the client and session open while
your application completes the enrollment workflow.

## Check deployment capabilities

Read the server's compatibility descriptor before offering the workflow:

```ts title="Check workspace enrollment support"
const compatibility = await client.server.compatibility();

const canInspectConnectors =
  compatibility.capabilities.mcpConnectorStatus;
const canEnrollWorkspace =
  compatibility.capabilities.workspaceEnrollment;
```

These capability values help your application choose which controls to show.
The server still decides whether each request is eligible when it receives the
request. A deployment or session can change after capability discovery.

## Inspect connector inventory

Call `listMcpConnectors()` on the session that owns the workspace:

```ts title="Inspect connector inventory"
import {
  McpConnectorAvailability,
  McpConnectorEnrollmentState,
} from '@stacklok-oss/mecatl-sdk';

const inventory = await session.listMcpConnectors();

if (inventory.availability === McpConnectorAvailability.Unavailable) {
  // Hide enrollment controls until the broker snapshot is available again.
}

if (inventory.enrollmentState === McpConnectorEnrollmentState.Completed) {
  // The broker has published the completed workspace catalogue.
}
```

The inventory is a current broker-local snapshot. It does not report connector
health, credential validity, session installation, persistence, or prompt
readiness. Connector rows are display values and cannot select a backend for an
enrollment operation.

Treat the aggregate inventory states separately from the states returned by
enrollment controls:

| Inventory field                       | Values                                                          | Meaning                                                |
| ------------------------------------- | --------------------------------------------------------------- | ------------------------------------------------------ |
| `availability`                        | `available`, `unavailable`, `unknown`                            | Whether the process-local broker snapshot can be read. |
| `enrollmentState`                     | `not_required`, `not_started`, `pending`, `completed`, `unknown` | The current whole-bundle enrollment observation.       |
| `connectors[].catalogueState`         | `hidden`, `declared`, `discovered`, `unknown`                    | Broker-local publication for one display row.          |

A zero `toolCount` means an empty discovered catalogue only when
`catalogueState` is `discovered`. The same count on `hidden` or `unknown` does
not make that claim.

## Start or observe enrollment

Call `connectWorkspaceServices()` once in response to an application action:

```ts title="Start or observe enrollment"
import {
  type Session,
  WorkspaceEnrollmentStatus,
} from '@stacklok-oss/mecatl-sdk';

async function beginWorkspaceEnrollment(
  session: Session,
): Promise<string | undefined> {
  const result = await session.connectWorkspaceServices();

  return result.status === WorkspaceEnrollmentStatus.Pending
    ? result.presentationUrl
    : undefined;
}
```

The server starts a whole bundle when none is pending and otherwise observes
the pending bundle. A pending result can include an ephemeral absolute HTTP(S)
`presentationUrl`. Pass that URL directly to your application's presentation
layer. Avoid logging or persisting it because it can contain short-lived
authorization material.

The SDK does not open a browser or schedule another observation. Your
application decides how to present the URL and when to call
`connectWorkspaceServices()` again to observe progress.

After the authorization flow finishes, have your application make its next
explicitly scheduled `connectWorkspaceServices()` call. Verify that the returned
status is `WorkspaceEnrollmentStatus.Connected`. If the status remains
`WorkspaceEnrollmentStatus.Pending`, use your application's policy to decide
whether and when to observe again. The SDK never repeats the call for you.

Control results use this separate state vocabulary:

| Status                                      | Interpretation                                                                             |
| ------------------------------------------- | ------------------------------------------------------------------------------------------ |
| `pending`                                   | The only known nonterminal state. It can carry `presentationUrl`.                           |
| `connected`                                 | Terminal success.                                                                          |
| `denied`, `cancelled`, `expired`, `failed`  | Terminal outcomes.                                                                         |
| `unknown`                                   | A future or empty server value. Stop automatic interpretation and wait for application policy. |

Inventory does not retain failed attempts. The immediate control result is the
only place that reports `failed`; a later inventory read reports `not_started`
after the server clears terminal enrollment state.

## Recover or cancel by correlation

Keep the `enrollmentId` while your application owns a pending workflow. If the
application loses a presentation URL but retains that ID, explicitly replace
the pending enrollment:

```ts title="Retry a pending enrollment"
const replacement = await session.retryWorkspaceEnrollment(enrollmentId);
const replacementPresentationUrl =
  replacement.status === WorkspaceEnrollmentStatus.Pending
    ? replacement.presentationUrl
    : undefined;
```

`retryWorkspaceEnrollment()` sends the exact prior correlation and returns a
different correlation for the replacement. `presentationUrl` can be undefined,
so your presentation function should accept `string | undefined`.

Cancel an exact pending correlation when the application abandons the flow:

```ts title="Cancel a pending enrollment"
const result = await session.cancelWorkspaceEnrollment(enrollmentId);
```

Cancellation returns the same correlation with a terminal status. A `failed`
result can mean the server settled enrollment after losing broker state.

## Handle errors and ambiguous completion

All four methods accept ordinary request options, including caller headers, an
`AbortSignal`, response callbacks, and `timeoutMs`. They preserve the session
affinity hint added by other bound `Session` methods.

Handle failures by their SDK type:

- `AuthenticationError` means the request was not authenticated.
- `ServerError` preserves domain codes such as `session_not_found`,
  `mcp_connector_unavailable`, and `failed_precondition`, plus a server request
  ID when supplied.
- `TransportError` means the SDK did not receive a usable response.
- `ProtocolError` means a successful response violated the SDK projection
  contract. Its generic message does not include rejected enrollment IDs,
  states, or presentation URLs.

A cancellation, deadline, client close, or transport failure can arrive after
the server commits an enrollment operation. The SDK does not roll back, retry,
cancel, or observe after that ambiguous result. Reconcile the server state
before choosing another explicit action. Inventory can reveal aggregate
`pending` state, but it does not expose the correlation needed for retry or
cancel.

## Next steps

- [Inspect a server before creating a session](./server-discovery.md) to build
  capability-aware application controls.
- [Work with sessions and runs](./sessions-and-runs.md) after workspace services
  are ready.

## Related information

- [TypeScript SDK core API](/reference/typescript-sdk-api/core.md)
- [Drive Mecatl through gRPC or HTTP](/building/deployment/grpc-http.md)
