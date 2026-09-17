import { ProtocolError, type TransportKind } from "./errors.js";
import type {
  ListSessionMcpConnectorsResponse,
  WorkspaceEnrollment as ProtoWorkspaceEnrollment,
} from "./gen/mecatl/v1/harness_pb.js";

/** Availability of the process-local broker snapshot for one session. @public */
export const McpConnectorAvailability = {
  Available: "available",
  Unavailable: "unavailable",
  Unknown: "unknown",
} as const;

/** One broker-snapshot availability value. @public */
export type McpConnectorAvailability =
  (typeof McpConnectorAvailability)[keyof typeof McpConnectorAvailability];

/** Aggregate workspace-enrollment state reported by connector inventory. @public */
export const McpConnectorEnrollmentState = {
  NotRequired: "not_required",
  NotStarted: "not_started",
  Pending: "pending",
  Completed: "completed",
  Unknown: "unknown",
} as const;

/** One aggregate connector-enrollment value. @public */
export type McpConnectorEnrollmentState =
  (typeof McpConnectorEnrollmentState)[keyof typeof McpConnectorEnrollmentState];

/** Broker-local catalogue publication state for one connector. @public */
export const McpConnectorCatalogueState = {
  Hidden: "hidden",
  Declared: "declared",
  Discovered: "discovered",
  Unknown: "unknown",
} as const;

/** One connector catalogue-publication value. @public */
export type McpConnectorCatalogueState =
  (typeof McpConnectorCatalogueState)[keyof typeof McpConnectorCatalogueState];

/** A bounded connector display row. It is not a routing handle. @public */
export interface McpConnectorStatus {
  /** Display name supplied by the server. */
  readonly name: string;
  /** Broker-local catalogue publication state. */
  readonly catalogueState: McpConnectorCatalogueState;
  /** Number of published tools when the catalogue state makes that count meaningful. */
  readonly toolCount: number;
}

/** A current, nonhistorical snapshot of broker connector publication. @public */
export interface McpConnectorInventory {
  /** Whether the process-local broker snapshot is available. */
  readonly availability: McpConnectorAvailability;
  /** Aggregate whole-bundle enrollment state. */
  readonly enrollmentState: McpConnectorEnrollmentState;
  /** Ordered bounded connector display rows. */
  readonly connectors: readonly McpConnectorStatus[];
  /** Total connector count before server-side truncation. */
  readonly totalConnectors: number;
  /** Whether the connector rows omit entries because of the server bound. */
  readonly truncated: boolean;
}

/** State returned by a whole-bundle workspace-enrollment operation. @public */
export const WorkspaceEnrollmentStatus = {
  Pending: "pending",
  Connected: "connected",
  Denied: "denied",
  Cancelled: "cancelled",
  Expired: "expired",
  Failed: "failed",
  Unknown: "unknown",
} as const;

/** One workspace-enrollment operation state. @public */
export type WorkspaceEnrollmentStatus =
  (typeof WorkspaceEnrollmentStatus)[keyof typeof WorkspaceEnrollmentStatus];

/** The immediate result of one whole-bundle workspace-enrollment operation. @public */
export interface WorkspaceEnrollment {
  /** Opaque correlation for this enrollment attempt. */
  readonly enrollmentId: string;
  /** Current operation state, or `unknown` for a future wire value. */
  readonly status: WorkspaceEnrollmentStatus;
  /** Number of services that the whole bundle requires. */
  readonly requiredServices: number;
  /** Ephemeral application-facing HTTP(S) launch URL, present only while pending. */
  readonly presentationUrl?: string;
}

function availability(value: string): McpConnectorAvailability {
  switch (value) {
    case McpConnectorAvailability.Available:
    case McpConnectorAvailability.Unavailable:
      return value;
    default:
      return McpConnectorAvailability.Unknown;
  }
}

function enrollmentState(value: string): McpConnectorEnrollmentState {
  switch (value) {
    case McpConnectorEnrollmentState.NotRequired:
    case McpConnectorEnrollmentState.NotStarted:
    case McpConnectorEnrollmentState.Pending:
    case McpConnectorEnrollmentState.Completed:
      return value;
    default:
      return McpConnectorEnrollmentState.Unknown;
  }
}

function catalogueState(value: string): McpConnectorCatalogueState {
  switch (value) {
    case McpConnectorCatalogueState.Hidden:
    case McpConnectorCatalogueState.Declared:
    case McpConnectorCatalogueState.Discovered:
      return value;
    default:
      return McpConnectorCatalogueState.Unknown;
  }
}

function workspaceEnrollmentStatus(value: string): WorkspaceEnrollmentStatus {
  switch (value) {
    case WorkspaceEnrollmentStatus.Pending:
    case WorkspaceEnrollmentStatus.Connected:
    case WorkspaceEnrollmentStatus.Denied:
    case WorkspaceEnrollmentStatus.Cancelled:
    case WorkspaceEnrollmentStatus.Expired:
    case WorkspaceEnrollmentStatus.Failed:
      return value;
    default:
      return WorkspaceEnrollmentStatus.Unknown;
  }
}

function validPresentationUrl(value: string): boolean {
  if (value.trim() !== value) return false;
  try {
    const parsed = new URL(value);
    return parsed.protocol === "http:" || parsed.protocol === "https:";
  } catch {
    return false;
  }
}

function enrollmentProtocolError(transport: TransportKind): ProtocolError {
  return new ProtocolError("Workspace enrollment returned a malformed response", { transport });
}

type EnrollmentOperation =
  | { readonly kind: "connect" }
  | { readonly enrollmentId: string; readonly kind: "retry" | "cancel" };

/** Internal projection and structural validation for workspace-enrollment responses. */
export function projectWorkspaceEnrollment(
  value: ProtoWorkspaceEnrollment,
  operation: EnrollmentOperation,
  transport: TransportKind,
): WorkspaceEnrollment {
  const status = workspaceEnrollmentStatus(value.status);
  const hasPresentationUrl = value.presentationUrl !== "";
  const invalidCommon =
    value.enrollmentId === "" ||
    !Number.isInteger(value.requiredServices) ||
    value.requiredServices <= 0 ||
    (hasPresentationUrl &&
      (status !== WorkspaceEnrollmentStatus.Pending ||
        !validPresentationUrl(value.presentationUrl)));
  const invalidCorrelation =
    operation.kind === "retry"
      ? value.enrollmentId === operation.enrollmentId
      : operation.kind === "cancel"
        ? value.enrollmentId !== operation.enrollmentId ||
          status === WorkspaceEnrollmentStatus.Pending
        : false;
  if (invalidCommon || invalidCorrelation) throw enrollmentProtocolError(transport);
  return {
    enrollmentId: value.enrollmentId,
    ...(hasPresentationUrl ? { presentationUrl: value.presentationUrl } : {}),
    requiredServices: value.requiredServices,
    status,
  };
}

/** Internal projection for the session-bound connector inventory operation. */
export function projectMcpConnectorInventory(
  value: ListSessionMcpConnectorsResponse,
): McpConnectorInventory {
  return {
    availability: availability(value.availability),
    connectors: value.connectors.map((connector) => ({
      catalogueState: catalogueState(connector.catalogueState),
      name: connector.name,
      toolCount: connector.toolCount,
    })),
    enrollmentState: enrollmentState(value.enrollmentState),
    totalConnectors: value.totalConnectors,
    truncated: value.truncated,
  };
}
