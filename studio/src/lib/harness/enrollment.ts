/**
 * Workspace-services enrollment over the mecatl TypeScript SDK — the TUI's
 * `/tools-connect` and `/tools-cancel` built-ins. A deployment whose
 * protected tools ride the MCP broker requires a pre-prompt, whole-bundle
 * consent step per session (`ServerCapabilities.workspace_enrollment`); until
 * it completes, prompts on that session are refused.
 *
 * Three unary controls (`session.workspaceEnrollment`): `connect()` begins
 * an eligible enrollment or OBSERVES the pending one (the poll form),
 * `retry(id)` cancels one exact pending enrollment and begins its
 * replacement, `cancel(id)` cancels it and clears its prompt gate. Retry and
 * cancel fail with a 412 `failed_precondition` when the id is stale — the
 * daemon already settled that enrollment.
 *
 * `presentationUrl` is an EPHEMERAL launch value (proto
 * `WorkspaceEnrollment`): a caller may only hand it to a window it opened,
 * never render, persist or log it.
 */

import { HarnessApiError } from "./errors";
import { harness, isUnsupportedByDaemon } from "./sdk";
import { harnessSession } from "./sessions";

/** The safe projection of one workspace-services enrollment. */
export interface WorkspaceEnrollment {
  enrollmentId: string;
  /**
   * The daemon's enrollment status vocabulary (internal/mcpbroker/broker.go):
   * `pending` | `connected` | `denied` | `cancelled` | `expired` | `failed`.
   * Every status but `pending` means the daemon has cleared its pending
   * record. Kept a string for forward compatibility.
   */
  status: string;
  requiredServices: number;
  /** Ephemeral: "" unless this call began an enrollment. Open it, never keep it. */
  presentationUrl: string;
}

/**
 * Begins an eligible enrollment, or observes the pending one
 * (`POST /v1/sessions/{id}/workspace-enrollment/connect`, no body). A
 * session that already carries messages is reset and re-begun by the
 * daemon, so connecting after the first prompt is valid.
 */
export async function connectWorkspaceServices(
  sessionId: string,
  signal?: AbortSignal,
): Promise<WorkspaceEnrollment> {
  const session = await harnessSession(sessionId, signal);
  return harness(() => session.workspaceEnrollment.connect({ signal }));
}

/**
 * Cancels one exact pending enrollment and begins its replacement
 * (`POST /v1/sessions/{id}/workspace-enrollment/{eid}/retry`, no body).
 */
export async function retryWorkspaceEnrollment(
  sessionId: string,
  enrollmentId: string,
  signal?: AbortSignal,
): Promise<WorkspaceEnrollment> {
  const session = await harnessSession(sessionId, signal);
  return harness(() =>
    session.workspaceEnrollment.retry(enrollmentId, { signal }),
  );
}

/**
 * Cancels one exact pending enrollment and clears its prompt gate
 * (`POST /v1/sessions/{id}/workspace-enrollment/{eid}/cancel`, no body).
 */
export async function cancelWorkspaceEnrollment(
  sessionId: string,
  enrollmentId: string,
  signal?: AbortSignal,
): Promise<WorkspaceEnrollment> {
  const session = await harnessSession(sessionId, signal);
  return harness(() =>
    session.workspaceEnrollment.cancel(enrollmentId, { signal }),
  );
}

/**
 * True for the daemon's "that enrollment is no longer pending" refusal
 * (412 `failed_precondition`): retry/cancel on an id the daemon already
 * settled. The caller re-observes or starts fresh instead of erroring.
 */
export function isStaleEnrollment(error: unknown): boolean {
  return (
    error instanceof HarnessApiError &&
    (error.status === 412 || error.code === "failed_precondition")
  );
}

interface SessionConnector {
  name: string;
  toolCount: number;
  catalogueState: string;
}

/** The owner-scoped broker connector inventory for one session. */
export interface SessionConnectors {
  availability: string;
  /**
   * The connector inventory's enrollment vocabulary
   * (internal/mcpbroker/connector.go) — a DIFFERENT vocabulary from
   * `WorkspaceEnrollment.status`: `not_required` | `not_started` | `pending`
   * | `completed` | `unknown`, where `completed` is the connected state.
   * Kept a string for forward compatibility.
   */
  enrollmentState: string;
  connectors: SessionConnector[];
  totalConnectors: number;
  truncated: boolean;
}

/**
 * Reads the session's connector inventory
 * (`GET /v1/sessions/{id}/mcp/connectors`). Inspection is available only
 * when the daemon enforces ownership and the caller is a verified principal
 * (`ServerCapabilities.mcp_connector_status`): a 401 / 403 / 404 / 412
 * refusal, or a daemon without the route, folds to `null` ("unknown") so the
 * UI degrades to the TUI's unconditional notice instead of erroring.
 */
export async function fetchSessionConnectors(
  sessionId: string,
  signal?: AbortSignal,
): Promise<SessionConnectors | null> {
  try {
    const session = await harnessSession(sessionId, signal);
    const inventory = await harness(() => session.mcpConnectors({ signal }));
    return {
      availability: inventory.availability,
      enrollmentState: inventory.enrollmentState,
      connectors: inventory.connectors.map((connector) => ({
        name: connector.name,
        toolCount: connector.toolCount,
        catalogueState: connector.catalogueState,
      })),
      totalConnectors: inventory.totalConnectors,
      truncated: inventory.truncated,
    };
  } catch (error) {
    if (isUnsupportedByDaemon(error)) return null;
    if (
      error instanceof HarnessApiError &&
      (error.status === 401 ||
        error.status === 403 ||
        error.status === 412 ||
        error.code === "unauthenticated" ||
        error.code === "permission_denied" ||
        error.code === "failed_precondition")
    ) {
      return null;
    }
    throw error;
  }
}
