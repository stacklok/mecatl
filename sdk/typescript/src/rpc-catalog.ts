import type {
  DescMessage,
  DescMethodBiDiStreaming,
  DescMethodServerStreaming,
  DescMethodStreaming,
  DescMethodUnary,
  JsonValue,
} from "@bufbuild/protobuf";

import { HarnessService } from "./gen/mecatl/v1/harness_pb.js";
import { ScheduleService } from "./gen/mecatl/v1/schedule_pb.js";

// BEGIN MECATL_RPC_CATALOG_SERVICES
export const RPC_CATALOG_SERVICES = ["HarnessService", "ScheduleService"] as const;
// END MECATL_RPC_CATALOG_SERVICES

// BEGIN MECATL_RPC_CATALOG_EXCLUDED_SERVICES
export const RPC_CATALOG_EXCLUDED_SERVICES = ["LocalSessionContextService"] as const;
// END MECATL_RPC_CATALOG_EXCLUDED_SERVICES

// BEGIN MECATL_RPC_TRANSPORT_KINDS
export type RPCTransportKind = "grpc" | "http" | "route-family";
// END MECATL_RPC_TRANSPORT_KINDS

// BEGIN MECATL_RPC_ROUTE_FAMILIES
export const RPC_ROUTE_FAMILIES = ["Converse"] as const;
// END MECATL_RPC_ROUTE_FAMILIES

export type RPCServiceName = (typeof RPC_CATALOG_SERVICES)[number];
export type RPCStreamingShape = "unary" | "server_streaming" | "bidi_streaming";
export type RPCRouteFamily = (typeof RPC_ROUTE_FAMILIES)[number];
export type HTTPMethod = "DELETE" | "GET" | "POST" | "PUT";
export type HTTPRequestBody = "json" | "none" | "optional-json";
export type HTTPResponseKind = "json" | "sse";

type RPCDescriptor = DescMethodUnary | DescMethodServerStreaming | DescMethodBiDiStreaming;

type DescriptorForShape<S extends RPCStreamingShape> = S extends "unary"
  ? DescMethodUnary
  : S extends "server_streaming"
    ? DescMethodServerStreaming
    : DescMethodBiDiStreaming;

export interface GRPCTransportClassification<D extends RPCDescriptor = RPCDescriptor> {
  readonly descriptor: D;
  readonly kind: "grpc";
  readonly rationale?: string;
}

export interface HTTPTransportClassification {
  /** Dot-separated request field whose value is the complete HTTP body. */
  readonly bodyField?: string;
  readonly decoder: DescMessage;
  readonly kind: "http";
  readonly method: HTTPMethod;
  /** Go ServeMux template, before request fields are encoded into it. */
  readonly pathTemplate: string;
  /** `template-name=request_field`; an `@` value is a reviewed fixed segment. */
  readonly pathParameters: readonly string[];
  readonly queryParameters: readonly string[];
  readonly requestBody: HTTPRequestBody;
  /** Response field that receives the complete HTTP JSON document. */
  readonly responseField?: string;
  readonly response: HTTPResponseKind;
}

export interface RouteFamilyTransportClassification {
  readonly controls: readonly HTTPOnlyControlName[];
  readonly family: RPCRouteFamily;
  readonly kind: "route-family";
}

// BEGIN MECATL_RPC_TRANSPORT_CLASSIFICATIONS
export type RPCTransportClassification =
  | GRPCTransportClassification
  | HTTPTransportClassification
  | RouteFamilyTransportClassification;
// END MECATL_RPC_TRANSPORT_CLASSIFICATIONS

export interface RPCCatalogEntry<
  S extends RPCServiceName = RPCServiceName,
  M extends string = string,
  Shape extends RPCStreamingShape = RPCStreamingShape,
  D extends DescriptorForShape<Shape> = DescriptorForShape<Shape>,
> {
  readonly backingService: string;
  readonly grpc: GRPCTransportClassification<D>;
  readonly http: RPCTransportClassification;
  readonly key: `${S}.${M}`;
  readonly method: M;
  readonly service: S;
  readonly shape: Shape;
}

interface HTTPRouteReview {
  readonly bodyField?: string;
  readonly kind: "http";
  readonly method: HTTPMethod;
  readonly pathTemplate: string;
  readonly pathParameters: readonly string[];
  readonly queryParameters: readonly string[];
  readonly requestBody: HTTPRequestBody;
  readonly responseField?: string;
  readonly response: HTTPResponseKind;
}

interface GRPCOnlyReview {
  readonly kind: "grpc";
  readonly rationale: string;
}

interface RouteFamilyReview {
  readonly controls: readonly HTTPOnlyControlName[];
  readonly family: RPCRouteFamily;
  readonly kind: "route-family";
}

type HTTPReview = HTTPRouteReview | GRPCOnlyReview | RouteFamilyReview;

function grpc<D extends RPCDescriptor>(descriptor: D): GRPCTransportClassification<D> {
  return { descriptor, kind: "grpc" };
}

function http(
  method: HTTPMethod,
  pathTemplate: string,
  pathParameters: readonly string[],
  queryParameters: readonly string[],
  requestBody: HTTPRequestBody,
  response: HTTPResponseKind,
  bodyField = "",
  responseField = "",
): HTTPRouteReview {
  return {
    ...(bodyField === "" ? {} : { bodyField }),
    kind: "http",
    method,
    pathParameters,
    pathTemplate,
    queryParameters,
    requestBody,
    ...(responseField === "" ? {} : { responseField }),
    response,
  };
}

function grpcOnly(rationale: string): GRPCOnlyReview {
  return { kind: "grpc", rationale };
}

function routeFamily(
  family: RPCRouteFamily,
  controls: readonly HTTPOnlyControlName[],
): RouteFamilyReview {
  return { controls, family, kind: "route-family" };
}

function rpc<
  const S extends RPCServiceName,
  const M extends string,
  const Shape extends RPCStreamingShape,
  const D extends DescriptorForShape<Shape>,
>(row: {
  readonly backingService: string;
  readonly grpc: GRPCTransportClassification<D>;
  readonly http: HTTPReview;
  readonly key: `${S}.${M}`;
  readonly method: M;
  readonly service: S;
  readonly shape: Shape;
}): RPCCatalogEntry<S, M, Shape, D> {
  const secondary: RPCTransportClassification =
    row.http.kind === "http"
      ? { ...row.http, decoder: row.grpc.descriptor.output }
      : row.http.kind === "grpc"
        ? { ...row.http, descriptor: row.grpc.descriptor }
        : row.http;
  return { ...row, http: secondary };
}

export const HTTP_ONLY_CONTROLS = {
  approve: http("POST", "/v1/sessions/{id}/approve", ["id=session_id"], [], "json", "json"),
  cancel: http("POST", "/v1/sessions/{id}/cancel", ["id=session_id"], [], "optional-json", "json"),
  cancelChild: http(
    "POST",
    "/v1/sessions/{id}/cancel-child",
    ["id=session_id"],
    [],
    "json",
    "json",
  ),
  cancelSteer: http(
    "POST",
    "/v1/sessions/{id}/cancel-steer",
    ["id=session_id"],
    [],
    "optional-json",
    "json",
  ),
  prompt: http("POST", "/v1/sessions/{id}/prompt", ["id=session_id"], [], "json", "sse"),
  retry: http("POST", "/v1/sessions/{id}/retry", ["id=session_id"], [], "none", "sse"),
  steer: http("POST", "/v1/sessions/{id}/steer", ["id=session_id"], [], "json", "json"),
} as const;

export type HTTPOnlyControlName = keyof typeof HTTP_ONLY_CONTROLS;

export interface ResolvedHTTPOnlyControl {
  readonly method: HTTPMethod;
  readonly path: string;
  readonly requestBody: HTTPRequestBody;
  readonly response: HTTPResponseKind;
}

export interface ResolvedHTTPRoute {
  readonly body: JsonValue | undefined;
  readonly classification: HTTPTransportClassification;
  readonly method: HTTPMethod;
  readonly path: string;
}

type JsonRecord = Record<string, JsonValue>;

function jsonRecord(value: JsonValue | undefined): JsonRecord {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return {};
  return value as JsonRecord;
}

function mappingParts(mapping: string): readonly [target: string, source: string] {
  const separator = mapping.indexOf("=");
  return separator < 0
    ? [mapping, mapping]
    : [mapping.slice(0, separator), mapping.slice(separator + 1)];
}

function fieldValue(input: JsonRecord, path: string): JsonValue | undefined {
  let current: JsonValue | undefined = input;
  for (const segment of path.split(".")) {
    current = jsonRecord(current)[segment];
    if (current === undefined) return undefined;
  }
  return current;
}

function cloneJson(value: JsonValue): JsonValue {
  if (Array.isArray(value)) return value.map(cloneJson);
  if (typeof value !== "object" || value === null) return value;
  const cloned: JsonRecord = {};
  for (const [name, child] of Object.entries(value)) cloned[name] = cloneJson(child);
  return cloned;
}

function deleteField(input: JsonRecord, path: string): void {
  const segments = path.split(".");
  let current = input;
  for (const segment of segments.slice(0, -1)) {
    const child = current[segment];
    if (typeof child !== "object" || child === null || Array.isArray(child)) return;
    current = child as JsonRecord;
  }
  delete current[segments.at(-1) ?? ""];
}

function scalarText(value: JsonValue, label: string): string {
  if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  throw new TypeError(`${label} must map to a scalar request field`);
}

function routeBody(
  classification: HTTPTransportClassification,
  input: JsonRecord,
): JsonValue | undefined {
  if (classification.requestBody === "none") return undefined;
  const body = jsonRecord(
    classification.bodyField === undefined
      ? jsonRecord(cloneJson(input))
      : cloneJson(fieldValue(input, classification.bodyField) ?? {}),
  );
  if (classification.bodyField === undefined) {
    for (const mapping of [...classification.pathParameters, ...classification.queryParameters]) {
      const [, source] = mappingParts(mapping);
      if (!source.startsWith("@")) deleteField(body, source);
    }
  }
  if (
    classification.requestBody === "optional-json" &&
    Object.keys(jsonRecord(body)).length === 0
  ) {
    return undefined;
  }
  return body;
}

/** Resolves one catalogued HTTP request without introducing a second route table. */
export function resolveHTTPRoute(
  method: DescMethodUnary | DescMethodStreaming,
  input: JsonRecord,
): ResolvedHTTPRoute | undefined {
  const entry = rpcCatalogByDescriptor.get(method);
  if (entry?.http.kind !== "http") return undefined;
  const classification = entry.http;
  let path = classification.pathTemplate;
  for (const mapping of classification.pathParameters) {
    const [placeholder, source] = mappingParts(mapping);
    const value = source.startsWith("@") ? source.slice(1) : fieldValue(input, source);
    if (value === undefined) throw new TypeError(`Missing HTTP route field ${source}`);
    path = path.replace(
      `{${placeholder}}`,
      encodeURIComponent(scalarText(value, `HTTP route field ${source}`)),
    );
  }
  const query = new URLSearchParams();
  for (const mapping of classification.queryParameters) {
    const [name, source] = mappingParts(mapping);
    const value = fieldValue(input, source);
    if (value === undefined) continue;
    if (Array.isArray(value)) {
      for (const item of value) query.append(name, scalarText(item, `HTTP query field ${source}`));
    } else {
      query.set(name, scalarText(value, `HTTP query field ${source}`));
    }
  }
  const suffix = query.toString();
  return {
    body: routeBody(classification, input),
    classification,
    method: classification.method,
    path: `${path}${suffix === "" ? "" : `?${suffix}`}`,
  };
}

/** Resolve one descriptorless HTTP control without admitting it to RPC coverage. */
export function resolveHTTPOnlyControl(
  name: HTTPOnlyControlName,
  requestFields: Readonly<Record<string, string>>,
): ResolvedHTTPOnlyControl {
  const control = HTTP_ONLY_CONTROLS[name];
  let path = control.pathTemplate;
  for (const mapping of control.pathParameters) {
    const separator = mapping.indexOf("=");
    const placeholder = mapping.slice(0, separator);
    const field = mapping.slice(separator + 1);
    const value = requestFields[field];
    if (value === undefined) {
      throw new TypeError(`Missing HTTP control path field ${field}`);
    }
    path = path.replace(`{${placeholder}}`, encodeURIComponent(value));
  }
  return {
    method: control.method,
    path,
    requestBody: control.requestBody,
    response: control.response,
  };
}

const rpcCatalogRows = [
  rpc({
    key: "HarnessService.GetCompatibilityInfo",
    service: "HarnessService",
    method: "GetCompatibilityInfo",
    shape: "unary",
    backingService: "CompatibilityInfo",
    grpc: grpc(HarnessService.method.getCompatibilityInfo),
    http: http("GET", "/v1/compatibility", [], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.CreateSession",
    service: "HarnessService",
    method: "CreateSession",
    shape: "unary",
    backingService: "CreateSessionWithProfile",
    grpc: grpc(HarnessService.method.createSession),
    http: http("POST", "/v1/sessions", [], [], "json", "json"),
  }),
  rpc({
    key: "HarnessService.GetServerInfo",
    service: "HarnessService",
    method: "GetServerInfo",
    shape: "unary",
    backingService: "serverInfoResponse",
    grpc: grpc(HarnessService.method.getServerInfo),
    http: http("GET", "/v1/info", [], ["provider_id"], "none", "json"),
  }),
  rpc({
    key: "HarnessService.GetSession",
    service: "HarnessService",
    method: "GetSession",
    shape: "unary",
    backingService: "GetSession",
    grpc: grpc(HarnessService.method.getSession),
    http: http("GET", "/v1/sessions/{id}", ["id=session_id"], [], "none", "json", "", "session"),
  }),
  rpc({
    key: "HarnessService.GetSessionTranscript",
    service: "HarnessService",
    method: "GetSessionTranscript",
    shape: "unary",
    backingService: "GetTranscript",
    grpc: grpc(HarnessService.method.getSessionTranscript),
    http: http("GET", "/v1/sessions/{id}/transcript", ["id=session_id"], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.SetMode",
    service: "HarnessService",
    method: "SetMode",
    shape: "unary",
    backingService: "SetMode",
    grpc: grpc(HarnessService.method.setMode),
    http: http(
      "POST",
      "/v1/sessions/{id}/mode",
      ["id=session_id"],
      [],
      "json",
      "json",
      "",
      "session",
    ),
  }),
  rpc({
    key: "HarnessService.CloseSession",
    service: "HarnessService",
    method: "CloseSession",
    shape: "unary",
    backingService: "EndSession",
    grpc: grpc(HarnessService.method.closeSession),
    http: http("DELETE", "/v1/sessions/{id}", ["id=session_id"], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.RenameSession",
    service: "HarnessService",
    method: "RenameSession",
    shape: "unary",
    backingService: "RenameSession",
    grpc: grpc(HarnessService.method.renameSession),
    http: http(
      "POST",
      "/v1/sessions/{id}/rename",
      ["id=session_id"],
      [],
      "json",
      "json",
      "",
      "session",
    ),
  }),
  rpc({
    key: "HarnessService.DeleteSession",
    service: "HarnessService",
    method: "DeleteSession",
    shape: "unary",
    backingService: "DeleteSession",
    grpc: grpc(HarnessService.method.deleteSession),
    http: http("POST", "/v1/sessions/{id}/delete", ["id=session_id"], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.CompactSession",
    service: "HarnessService",
    method: "CompactSession",
    shape: "unary",
    backingService: "CompactSession",
    grpc: grpc(HarnessService.method.compactSession),
    http: http("POST", "/v1/sessions/{id}/compact", ["id=session_id"], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.ClearSession",
    service: "HarnessService",
    method: "ClearSession",
    shape: "unary",
    backingService: "ClearSessionSuccessor",
    grpc: grpc(HarnessService.method.clearSession),
    http: http(
      "POST",
      "/v1/sessions/{id}/clear",
      ["id=source_session_id"],
      [],
      "optional-json",
      "json",
    ),
  }),
  rpc({
    key: "HarnessService.ForkSession",
    service: "HarnessService",
    method: "ForkSession",
    shape: "unary",
    backingService: "ForkSessionSuccessor",
    grpc: grpc(HarnessService.method.forkSession),
    http: http(
      "POST",
      "/v1/sessions/{id}/fork",
      ["id=source_session_id"],
      [],
      "optional-json",
      "json",
    ),
  }),
  rpc({
    key: "HarnessService.Converse",
    service: "HarnessService",
    method: "Converse",
    shape: "bidi_streaming",
    backingService: "StartRunContent",
    grpc: grpc(HarnessService.method.converse),
    http: routeFamily("Converse", ["prompt", "retry", "approve", "cancel", "cancelChild"]),
  }),
  rpc({
    key: "HarnessService.ListMcpResources",
    service: "HarnessService",
    method: "ListMcpResources",
    shape: "unary",
    backingService: "ListMcpResources",
    grpc: grpc(HarnessService.method.listMcpResources),
    http: http("GET", "/v1/mcp/resources", [], ["server"], "none", "json"),
  }),
  rpc({
    key: "HarnessService.ReadMcpResource",
    service: "HarnessService",
    method: "ReadMcpResource",
    shape: "unary",
    backingService: "ReadMcpResource",
    grpc: grpc(HarnessService.method.readMcpResource),
    http: http("GET", "/v1/mcp/resources/read", [], ["server", "uri"], "none", "json"),
  }),
  rpc({
    key: "HarnessService.ListMcpPrompts",
    service: "HarnessService",
    method: "ListMcpPrompts",
    shape: "unary",
    backingService: "ListMcpPrompts",
    grpc: grpc(HarnessService.method.listMcpPrompts),
    http: http("GET", "/v1/mcp/prompts", [], ["server"], "none", "json"),
  }),
  rpc({
    key: "HarnessService.GetMcpPrompt",
    service: "HarnessService",
    method: "GetMcpPrompt",
    shape: "unary",
    backingService: "GetMcpPrompt",
    grpc: grpc(HarnessService.method.getMcpPrompt),
    http: http("POST", "/v1/mcp/prompts/get", [], [], "json", "json"),
  }),
  rpc({
    key: "HarnessService.ListMcpSources",
    service: "HarnessService",
    method: "ListMcpSources",
    shape: "unary",
    backingService: "ListMcpSources",
    grpc: grpc(HarnessService.method.listMcpSources),
    http: http("GET", "/v1/mcp/sources", [], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.RefreshMcpSources",
    service: "HarnessService",
    method: "RefreshMcpSources",
    shape: "unary",
    backingService: "RefreshMcpSources",
    grpc: grpc(HarnessService.method.refreshMcpSources),
    http: http("POST", "/v1/sessions/{id}/mcp-refresh", ["id=session_id"], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.ListToolHiveGroups",
    service: "HarnessService",
    method: "ListToolHiveGroups",
    shape: "unary",
    backingService: "ListToolHiveGroups",
    grpc: grpc(HarnessService.method.listToolHiveGroups),
    http: http("GET", "/v1/mcp/toolhive/groups", [], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.ListAgents",
    service: "HarnessService",
    method: "ListAgents",
    shape: "unary",
    backingService: "ListAgents",
    grpc: grpc(HarnessService.method.listAgents),
    http: http("GET", "/v1/agents", [], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.ListCommands",
    service: "HarnessService",
    method: "ListCommands",
    shape: "unary",
    backingService: "ListCommandsForSession",
    grpc: grpc(HarnessService.method.listCommands),
    http: http("GET", "/v1/commands", [], ["session_id"], "none", "json"),
  }),
  rpc({
    key: "HarnessService.ListWorktrees",
    service: "HarnessService",
    method: "ListWorktrees",
    shape: "unary",
    backingService: "ListWorktreesForSession",
    grpc: grpc(HarnessService.method.listWorktrees),
    http: http("GET", "/v1/worktrees", [], ["session_id"], "none", "json"),
  }),
  rpc({
    key: "HarnessService.StreamSessionEvents",
    service: "HarnessService",
    method: "StreamSessionEvents",
    shape: "server_streaming",
    backingService: "StreamSessionEvents",
    grpc: grpc(HarnessService.method.streamSessionEvents),
    http: http("GET", "/v1/sessions/{id}/events", ["id=session_id"], [], "none", "sse"),
  }),
  rpc({
    key: "HarnessService.StreamSessionLive",
    service: "HarnessService",
    method: "StreamSessionLive",
    shape: "server_streaming",
    backingService: "Subscribe",
    grpc: grpc(HarnessService.method.streamSessionLive),
    http: grpcOnly("The live process-local subscription stream has no HTTP route."),
  }),
  rpc({
    key: "HarnessService.WatchSessionEvents",
    service: "HarnessService",
    method: "WatchSessionEvents",
    shape: "server_streaming",
    backingService: "WatchSessionEvents",
    grpc: grpc(HarnessService.method.watchSessionEvents),
    http: http(
      "GET",
      "/v1/sessions/{id}/watch",
      ["id=session_id"],
      ["cursor", "run_id"],
      "none",
      "sse",
    ),
  }),
  rpc({
    key: "HarnessService.ListSessions",
    service: "HarnessService",
    method: "ListSessions",
    shape: "unary",
    backingService: "ListSessionPage",
    grpc: grpc(HarnessService.method.listSessions),
    http: http("GET", "/v1/sessions", [], ["page_size", "cursor"], "none", "json"),
  }),
  rpc({
    key: "HarnessService.GetStorageHealth",
    service: "HarnessService",
    method: "GetStorageHealth",
    shape: "unary",
    backingService: "StorageHealth",
    grpc: grpc(HarnessService.method.getStorageHealth),
    http: http("GET", "/v1/storage/health", [], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.PlanSessionMigration",
    service: "HarnessService",
    method: "PlanSessionMigration",
    shape: "unary",
    backingService: "PlanSessionMigration",
    grpc: grpc(HarnessService.method.planSessionMigration),
    http: http("POST", "/v1/storage/migrations/plan", [], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.ApplySessionMigration",
    service: "HarnessService",
    method: "ApplySessionMigration",
    shape: "unary",
    backingService: "ApplySessionMigration",
    grpc: grpc(HarnessService.method.applySessionMigration),
    http: http("POST", "/v1/storage/migrations/apply", [], [], "json", "json"),
  }),
  rpc({
    key: "HarnessService.ResumeSessionMigration",
    service: "HarnessService",
    method: "ResumeSessionMigration",
    shape: "unary",
    backingService: "ResumeSessionMigration",
    grpc: grpc(HarnessService.method.resumeSessionMigration),
    http: http(
      "POST",
      "/v1/storage/migrations/{id}/resume",
      ["id=job_id"],
      [],
      "optional-json",
      "json",
    ),
  }),
  rpc({
    key: "HarnessService.CancelSessionMigration",
    service: "HarnessService",
    method: "CancelSessionMigration",
    shape: "unary",
    backingService: "CancelSessionMigration",
    grpc: grpc(HarnessService.method.cancelSessionMigration),
    http: http("POST", "/v1/storage/migrations/{id}/cancel", ["id=job_id"], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.GetSessionMigrationJob",
    service: "HarnessService",
    method: "GetSessionMigrationJob",
    shape: "unary",
    backingService: "SessionMigrationJob",
    grpc: grpc(HarnessService.method.getSessionMigrationJob),
    http: http("GET", "/v1/storage/migrations/{id}", ["id=job_id"], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.PlanSessionCleanup",
    service: "HarnessService",
    method: "PlanSessionCleanup",
    shape: "unary",
    backingService: "PlanSessionCleanup",
    grpc: grpc(HarnessService.method.planSessionCleanup),
    http: http("POST", "/v1/storage/cleanup:plan", [], [], "json", "json"),
  }),
  rpc({
    key: "HarnessService.ApplySessionCleanup",
    service: "HarnessService",
    method: "ApplySessionCleanup",
    shape: "unary",
    backingService: "ApplySessionCleanup",
    grpc: grpc(HarnessService.method.applySessionCleanup),
    http: http("POST", "/v1/storage/cleanup:apply", [], [], "json", "json"),
  }),
  rpc({
    key: "HarnessService.CancelSessionCleanup",
    service: "HarnessService",
    method: "CancelSessionCleanup",
    shape: "unary",
    backingService: "CancelSessionCleanup",
    grpc: grpc(HarnessService.method.cancelSessionCleanup),
    http: http("POST", "/v1/storage/cleanup/jobs/{id}/cancel", ["id=job_id"], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.GetSessionCleanupJob",
    service: "HarnessService",
    method: "GetSessionCleanupJob",
    shape: "unary",
    backingService: "SessionCleanupJob",
    grpc: grpc(HarnessService.method.getSessionCleanupJob),
    http: http("GET", "/v1/storage/cleanup/jobs/{id}", ["id=job_id"], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.ListSkills",
    service: "HarnessService",
    method: "ListSkills",
    shape: "unary",
    backingService: "ListSkills",
    grpc: grpc(HarnessService.method.listSkills),
    http: http("GET", "/v1/skills", [], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.GetSoul",
    service: "HarnessService",
    method: "GetSoul",
    shape: "unary",
    backingService: "GetSoul",
    grpc: grpc(HarnessService.method.getSoul),
    http: http("GET", "/v1/soul", [], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.GetUserModel",
    service: "HarnessService",
    method: "GetUserModel",
    shape: "unary",
    backingService: "GetUserModel",
    grpc: grpc(HarnessService.method.getUserModel),
    http: http("GET", "/v1/usermodel", [], ["key"], "none", "json"),
  }),
  rpc({
    key: "HarnessService.ReflectSession",
    service: "HarnessService",
    method: "ReflectSession",
    shape: "unary",
    backingService: "ReflectSession",
    grpc: grpc(HarnessService.method.reflectSession),
    http: http("POST", "/v1/sessions/{id}/reflect", ["id=session_id"], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.GetLearningAttempt",
    service: "HarnessService",
    method: "GetLearningAttempt",
    shape: "unary",
    backingService: "GetLearningAttempt",
    grpc: grpc(HarnessService.method.getLearningAttempt),
    http: http("GET", "/v1/learning/attempts/{id}", ["id=id"], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.ListLearningAttempts",
    service: "HarnessService",
    method: "ListLearningAttempts",
    shape: "unary",
    backingService: "ListLearningAttempts",
    grpc: grpc(HarnessService.method.listLearningAttempts),
    http: http("GET", "/v1/learning/attempts", [], ["state", "cursor", "limit"], "none", "json"),
  }),
  rpc({
    key: "HarnessService.RetryLearningAttempt",
    service: "HarnessService",
    method: "RetryLearningAttempt",
    shape: "unary",
    backingService: "RetryLearningAttempt",
    grpc: grpc(HarnessService.method.retryLearningAttempt),
    http: http("POST", "/v1/learning/attempts/{id}/retry", ["id=id"], [], "json", "json"),
  }),
  rpc({
    key: "HarnessService.AbandonLearningAttempt",
    service: "HarnessService",
    method: "AbandonLearningAttempt",
    shape: "unary",
    backingService: "AbandonLearningAttempt",
    grpc: grpc(HarnessService.method.abandonLearningAttempt),
    http: http("POST", "/v1/learning/attempts/{id}/abandon", ["id=id"], [], "json", "json"),
  }),
  rpc({
    key: "HarnessService.GenerateDreamPlan",
    service: "HarnessService",
    method: "GenerateDreamPlan",
    shape: "unary",
    backingService: "GenerateDream",
    grpc: grpc(HarnessService.method.generateDreamPlan),
    http: http("POST", "/v1/dream/plans", [], [], "json", "json"),
  }),
  rpc({
    key: "HarnessService.DecideDreamPlan",
    service: "HarnessService",
    method: "DecideDreamPlan",
    shape: "unary",
    backingService: "DecideDream",
    grpc: grpc(HarnessService.method.decideDreamPlan),
    http: http(
      "POST",
      "/v1/dream/plans/{plan_id}/decision",
      ["plan_id=plan_id"],
      [],
      "json",
      "json",
    ),
  }),
  rpc({
    key: "HarnessService.ListLearningProposals",
    service: "HarnessService",
    method: "ListLearningProposals",
    shape: "unary",
    backingService: "ListLearningProposals",
    grpc: grpc(HarnessService.method.listLearningProposals),
    http: http(
      "GET",
      "/v1/learning/proposals",
      [],
      ["status", "cursor", "limit", "project"],
      "none",
      "json",
    ),
  }),
  rpc({
    key: "HarnessService.GetLearningProposal",
    service: "HarnessService",
    method: "GetLearningProposal",
    shape: "unary",
    backingService: "GetLearningProposal",
    grpc: grpc(HarnessService.method.getLearningProposal),
    http: http("GET", "/v1/learning/proposals/{id}", ["id=id"], ["project"], "none", "json"),
  }),
  rpc({
    key: "HarnessService.DecideLearningProposal",
    service: "HarnessService",
    method: "DecideLearningProposal",
    shape: "unary",
    backingService: "DecideLearningProposal",
    grpc: grpc(HarnessService.method.decideLearningProposal),
    http: http("POST", "/v1/learning/proposals/{id}/decision", ["id=id"], [], "json", "json"),
  }),
  rpc({
    key: "HarnessService.UndoLearningPromotion",
    service: "HarnessService",
    method: "UndoLearningPromotion",
    shape: "unary",
    backingService: "UndoLearningPromotion",
    grpc: grpc(HarnessService.method.undoLearningPromotion),
    http: http("POST", "/v1/learning/proposals/{id}/undo", ["id=id"], [], "json", "json"),
  }),
  rpc({
    key: "HarnessService.ListLearnedSkills",
    service: "HarnessService",
    method: "ListLearnedSkills",
    shape: "unary",
    backingService: "ListLearnedSkills",
    grpc: grpc(HarnessService.method.listLearnedSkills),
    http: http(
      "GET",
      "/v1/skills/learned",
      [],
      ["project", "cursor", "limit", "state", "owner_agent"],
      "none",
      "json",
    ),
  }),
  rpc({
    key: "HarnessService.GetLearnedSkill",
    service: "HarnessService",
    method: "GetLearnedSkill",
    shape: "unary",
    backingService: "GetLearnedSkill",
    grpc: grpc(HarnessService.method.getLearnedSkill),
    http: http(
      "GET",
      "/v1/skills/learned/{id}",
      ["id=id"],
      ["project", "owner_agent", "version"],
      "none",
      "json",
    ),
  }),
  rpc({
    key: "HarnessService.DiffLearnedSkillVersions",
    service: "HarnessService",
    method: "DiffLearnedSkillVersions",
    shape: "unary",
    backingService: "DiffLearnedSkillVersions",
    grpc: grpc(HarnessService.method.diffLearnedSkillVersions),
    http: http(
      "GET",
      "/v1/skills/learned/{id}/diff",
      ["id=id"],
      ["project", "owner_agent", "from=from_version", "to=to_version"],
      "none",
      "json",
    ),
  }),
  rpc({
    key: "HarnessService.ActivateLearnedSkill",
    service: "HarnessService",
    method: "ActivateLearnedSkill",
    shape: "unary",
    backingService: "ActivateLearnedSkill",
    grpc: grpc(HarnessService.method.activateLearnedSkill),
    http: http("POST", "/v1/skills/learned/{id}/activate", ["id=id"], [], "json", "json"),
  }),
  rpc({
    key: "HarnessService.RejectLearnedSkill",
    service: "HarnessService",
    method: "RejectLearnedSkill",
    shape: "unary",
    backingService: "RejectLearnedSkill",
    grpc: grpc(HarnessService.method.rejectLearnedSkill),
    http: http("POST", "/v1/skills/learned/{id}/reject", ["id=id"], [], "json", "json"),
  }),
  rpc({
    key: "HarnessService.ArchiveLearnedSkill",
    service: "HarnessService",
    method: "ArchiveLearnedSkill",
    shape: "unary",
    backingService: "ArchiveLearnedSkill",
    grpc: grpc(HarnessService.method.archiveLearnedSkill),
    http: http("POST", "/v1/skills/learned/{id}/archive", ["id=id"], [], "json", "json"),
  }),
  rpc({
    key: "HarnessService.RollbackLearnedSkill",
    service: "HarnessService",
    method: "RollbackLearnedSkill",
    shape: "unary",
    backingService: "RollbackLearnedSkill",
    grpc: grpc(HarnessService.method.rollbackLearnedSkill),
    http: http("POST", "/v1/skills/learned/{id}/rollback", ["id=id"], [], "json", "json"),
  }),
  rpc({
    key: "HarnessService.ListSkillChanges",
    service: "HarnessService",
    method: "ListSkillChanges",
    shape: "unary",
    backingService: "ListSkillChanges",
    grpc: grpc(HarnessService.method.listSkillChanges),
    http: http(
      "GET",
      "/v1/skills/learned/changes",
      [],
      ["project", "cursor", "limit"],
      "none",
      "json",
    ),
  }),
  rpc({
    key: "HarnessService.ListModels",
    service: "HarnessService",
    method: "ListModels",
    shape: "unary",
    backingService: "ListModels",
    grpc: grpc(HarnessService.method.listModels),
    http: http("GET", "/v1/models", [], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.CreateTeam",
    service: "HarnessService",
    method: "CreateTeam",
    shape: "unary",
    backingService: "CreateTeamForSession",
    grpc: grpc(HarnessService.method.createTeam),
    http: http("POST", "/v1/teams", [], [], "json", "json"),
  }),
  rpc({
    key: "HarnessService.SpawnTeammate",
    service: "HarnessService",
    method: "SpawnTeammate",
    shape: "unary",
    backingService: "SpawnTeammate",
    grpc: grpc(HarnessService.method.spawnTeammate),
    http: http("POST", "/v1/teams/{id}/members", ["id=team_id"], [], "json", "json"),
  }),
  rpc({
    key: "HarnessService.SendTeammateMessage",
    service: "HarnessService",
    method: "SendTeammateMessage",
    shape: "unary",
    backingService: "SendTeammateMessage",
    grpc: grpc(HarnessService.method.sendTeammateMessage),
    http: http("POST", "/v1/teams/{id}/messages", ["id=team_id"], [], "json", "json"),
  }),
  rpc({
    key: "HarnessService.CancelTeammate",
    service: "HarnessService",
    method: "CancelTeammate",
    shape: "unary",
    backingService: "CancelTeammate",
    grpc: grpc(HarnessService.method.cancelTeammate),
    http: http("POST", "/v1/teams/{id}/members/cancel", ["id=team_id"], [], "json", "json"),
  }),
  rpc({
    key: "HarnessService.RunTeam",
    service: "HarnessService",
    method: "RunTeam",
    shape: "server_streaming",
    backingService: "RunTeam",
    grpc: grpc(HarnessService.method.runTeam),
    http: http("POST", "/v1/teams/{id}/run", ["id=team_id"], [], "none", "sse"),
  }),
  rpc({
    key: "HarnessService.ListTeam",
    service: "HarnessService",
    method: "ListTeam",
    shape: "unary",
    backingService: "ListTeam",
    grpc: grpc(HarnessService.method.listTeam),
    http: http("GET", "/v1/teams/{id}", ["id=team_id"], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.CleanupTeam",
    service: "HarnessService",
    method: "CleanupTeam",
    shape: "unary",
    backingService: "CleanupTeam",
    grpc: grpc(HarnessService.method.cleanupTeam),
    http: http("DELETE", "/v1/teams/{id}", ["id=team_id"], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.ApprovePlan",
    service: "HarnessService",
    method: "ApprovePlan",
    shape: "server_streaming",
    backingService: "ApprovePlan",
    grpc: grpc(HarnessService.method.approvePlan),
    http: http(
      "POST",
      "/v1/sessions/{id}/plan:approve",
      ["id=session_id"],
      [],
      "optional-json",
      "sse",
    ),
  }),
  rpc({
    key: "HarnessService.GetMcpAuthorizationPresentation",
    service: "HarnessService",
    method: "GetMcpAuthorizationPresentation",
    shape: "unary",
    backingService: "MCPAuthorizationPresentation",
    grpc: grpc(HarnessService.method.getMcpAuthorizationPresentation),
    http: http(
      "GET",
      "/v1/sessions/{id}/mcp-authorizations/{authorization_id}/presentation",
      ["id=session_id", "authorization_id=authorization_id"],
      [],
      "none",
      "json",
    ),
  }),
  rpc({
    key: "HarnessService.RecheckMcpAuthorization",
    service: "HarnessService",
    method: "RecheckMcpAuthorization",
    shape: "bidi_streaming",
    backingService: "RecheckMCPAuthorization",
    grpc: grpc(HarnessService.method.recheckMcpAuthorization),
    http: http(
      "POST",
      "/v1/sessions/{id}/mcp-authorizations/{authorization_id}/recheck",
      ["id=session_id", "authorization_id=authorization_id"],
      [],
      "json",
      "sse",
    ),
  }),
  rpc({
    key: "HarnessService.CancelMcpAuthorization",
    service: "HarnessService",
    method: "CancelMcpAuthorization",
    shape: "bidi_streaming",
    backingService: "CancelMCPAuthorization",
    grpc: grpc(HarnessService.method.cancelMcpAuthorization),
    http: http(
      "POST",
      "/v1/sessions/{id}/mcp-authorizations/{authorization_id}/cancel",
      ["id=session_id", "authorization_id=authorization_id"],
      [],
      "json",
      "sse",
    ),
  }),
  rpc({
    key: "HarnessService.ListSessionMcpConnectors",
    service: "HarnessService",
    method: "ListSessionMcpConnectors",
    shape: "unary",
    backingService: "ListSessionMcpConnectors",
    grpc: grpc(HarnessService.method.listSessionMcpConnectors),
    http: http("GET", "/v1/sessions/{id}/mcp/connectors", ["id=session_id"], [], "none", "json"),
  }),
  rpc({
    key: "HarnessService.ConnectWorkspaceServices",
    service: "HarnessService",
    method: "ConnectWorkspaceServices",
    shape: "unary",
    backingService: "ConnectWorkspaceServices",
    grpc: grpc(HarnessService.method.connectWorkspaceServices),
    http: http(
      "POST",
      "/v1/sessions/{id}/workspace-enrollment/connect",
      ["id=session_id"],
      [],
      "none",
      "json",
    ),
  }),
  rpc({
    key: "HarnessService.RetryWorkspaceEnrollment",
    service: "HarnessService",
    method: "RetryWorkspaceEnrollment",
    shape: "unary",
    backingService: "RetryWorkspaceEnrollment",
    grpc: grpc(HarnessService.method.retryWorkspaceEnrollment),
    http: http(
      "POST",
      "/v1/sessions/{id}/workspace-enrollment/{enrollment_id}/retry",
      ["id=session_id", "enrollment_id=enrollment_id"],
      [],
      "none",
      "json",
    ),
  }),
  rpc({
    key: "HarnessService.CancelWorkspaceEnrollment",
    service: "HarnessService",
    method: "CancelWorkspaceEnrollment",
    shape: "unary",
    backingService: "CancelWorkspaceEnrollment",
    grpc: grpc(HarnessService.method.cancelWorkspaceEnrollment),
    http: http(
      "POST",
      "/v1/sessions/{id}/workspace-enrollment/{enrollment_id}/cancel",
      ["id=session_id", "enrollment_id=enrollment_id"],
      [],
      "none",
      "json",
    ),
  }),
  rpc({
    key: "ScheduleService.CreateSchedule",
    service: "ScheduleService",
    method: "CreateSchedule",
    shape: "unary",
    backingService: "CreateSchedule",
    grpc: grpc(ScheduleService.method.createSchedule),
    http: http("POST", "/v1/schedules", [], [], "json", "json", "spec"),
  }),
  rpc({
    key: "ScheduleService.GetSchedule",
    service: "ScheduleService",
    method: "GetSchedule",
    shape: "unary",
    backingService: "GetSchedule",
    grpc: grpc(ScheduleService.method.getSchedule),
    http: http("GET", "/v1/schedules/{name}", ["name=name"], [], "none", "json"),
  }),
  rpc({
    key: "ScheduleService.ListSchedules",
    service: "ScheduleService",
    method: "ListSchedules",
    shape: "unary",
    backingService: "ListSchedules",
    grpc: grpc(ScheduleService.method.listSchedules),
    http: http("GET", "/v1/schedules", [], [], "none", "json"),
  }),
  rpc({
    key: "ScheduleService.UpdateSchedule",
    service: "ScheduleService",
    method: "UpdateSchedule",
    shape: "unary",
    backingService: "UpdateSchedule",
    grpc: grpc(ScheduleService.method.updateSchedule),
    http: http("PUT", "/v1/schedules/{name}", ["name=spec.name"], [], "json", "json", "spec"),
  }),
  rpc({
    key: "ScheduleService.DeleteSchedule",
    service: "ScheduleService",
    method: "DeleteSchedule",
    shape: "unary",
    backingService: "DeleteSchedule",
    grpc: grpc(ScheduleService.method.deleteSchedule),
    http: http("DELETE", "/v1/schedules/{name}", ["name=name"], [], "none", "json"),
  }),
  rpc({
    key: "ScheduleService.FireNow",
    service: "ScheduleService",
    method: "FireNow",
    shape: "unary",
    backingService: "FireNow",
    grpc: grpc(ScheduleService.method.fireNow),
    http: http("POST", "/v1/schedules/{name}/fire", ["name=name"], [], "none", "json"),
  }),
  rpc({
    key: "ScheduleService.PauseSchedule",
    service: "ScheduleService",
    method: "PauseSchedule",
    shape: "unary",
    backingService: "PauseSchedule",
    grpc: grpc(ScheduleService.method.pauseSchedule),
    http: http("POST", "/v1/schedules/{name}/pause", ["name=name"], [], "none", "json"),
  }),
  rpc({
    key: "ScheduleService.ResumeSchedule",
    service: "ScheduleService",
    method: "ResumeSchedule",
    shape: "unary",
    backingService: "ResumeSchedule",
    grpc: grpc(ScheduleService.method.resumeSchedule),
    http: http("POST", "/v1/schedules/{name}/resume", ["name=name"], [], "none", "json"),
  }),
  rpc({
    key: "ScheduleService.GetFire",
    service: "ScheduleService",
    method: "GetFire",
    shape: "unary",
    backingService: "GetFire",
    grpc: grpc(ScheduleService.method.getFire),
    http: http(
      "GET",
      "/v1/schedules/{name}/fires/{id}",
      ["name=@unused", "id=fire_id"],
      [],
      "none",
      "json",
    ),
  }),
  rpc({
    key: "ScheduleService.ListFires",
    service: "ScheduleService",
    method: "ListFires",
    shape: "unary",
    backingService: "ListFires",
    grpc: grpc(ScheduleService.method.listFires),
    http: http("GET", "/v1/schedules/{name}/fires", ["name=schedule_name"], [], "none", "json"),
  }),
] as const;

type CatalogFor<Rows extends readonly RPCCatalogEntry[]> = Readonly<{
  [K in Rows[number]["key"]]: Extract<Rows[number], { readonly key: K }>;
}>;

function indexCatalog<const Rows extends readonly RPCCatalogEntry[]>(rows: Rows): CatalogFor<Rows> {
  const catalog: Record<string, RPCCatalogEntry> = {};
  for (const row of rows) {
    if (catalog[row.key] !== undefined) {
      throw new TypeError(`Duplicate RPC catalog key ${row.key}`);
    }
    catalog[row.key] = row;
  }
  return Object.freeze(catalog) as CatalogFor<Rows>;
}

/** The reviewed HarnessService + ScheduleService raw-transport completeness authority. */
export const RPC_CATALOG = indexCatalog(rpcCatalogRows);

const rpcCatalogByDescriptor = new Map<DescMethodUnary | DescMethodStreaming, RPCCatalogEntry>();
for (const entry of rpcCatalogRows) rpcCatalogByDescriptor.set(entry.grpc.descriptor, entry);

export type RPCCatalog = typeof RPC_CATALOG;
export type RPCCatalogKey = keyof RPCCatalog;
