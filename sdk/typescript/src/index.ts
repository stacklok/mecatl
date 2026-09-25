/**
 * Transport-neutral Mecatl SDK entry point.
 *
 * @packageDocumentation
 */

export type { Transport } from "@connectrpc/connect";
export type {
  ClearSessionOptions,
  Client,
  ConnectionStatus,
  ConnectionStatusListener,
  ConnectionStatusStore,
  ConnectOptions,
  CreateSessionOptions,
  ForkSessionOptions,
  InjectedTransportOptions,
  Session,
  SessionLimits,
  SessionMcpServer,
  Sessions,
} from "./client.js";
export { connect } from "./client.js";
export type { CredentialOptions, CredentialProvider } from "./credentials.js";
export type {
  ClientDiagnosticsOptions,
  DiagnosticFieldValue,
  DiagnosticLevel,
  DiagnosticRecord,
  DiagnosticsSink,
  ErrorOrigin,
  MecatlErrorCode,
  MecatlErrorOptions,
  PromptValidationReason,
  SDKErrorCode,
  ServerErrorCode,
  TransportKind,
} from "./errors.js";
export {
  ActivityGapError,
  AuthenticationError,
  CursorExpiredError,
  CursorMalformedError,
  CursorScopeError,
  IncompatibleServerError,
  InvalidStateError,
  MECATL_ERROR_CODES,
  MecatlError,
  NoRunsError,
  PermissionAskAlreadyResolvedError,
  PlanApprovalRequiredError,
  PlanContinuationStartError,
  PromptValidationError,
  ProtocolError,
  ServerError,
  SessionBusyError,
  TransportError,
  UnsupportedFeatureError,
} from "./errors.js";
export type {
  AgentEvent,
  ApprovalEventPayload,
  ArchivedConversationMessage,
  CompactionArchiveEventPayload,
  ControlRefusedEventPayload,
  Event,
  EventCommon,
  EventContent,
  EventContentBlock,
  EventOf,
  EventPayloads,
  EventUsage,
  GuardrailApprovalScope,
  HookEventPayload,
  KnownEvent,
  KnownEventKind,
  ModelRetryEventPayload,
  ParallelEventPayload,
  PermissionAskEventPayload,
  PlanContinuationFailureEventPayload,
  ResultEventPayload,
  RoutingDecisionEventPayload,
  ScheduleEventPayload,
  SessionTitleEventPayload,
  SteerEventPayload,
  SteerOutcomeEventPayload,
  SubagentEventPayload,
  TeamEvent,
  TeamEventPayload,
  TeamFindingEventPayload,
  TeamMemberDispositionEventPayload,
  TeamMemberSpecEventPayload,
  TeamTaskEventPayload,
  TitleAttemptEventPayload,
  ToolCallEventPayload,
  ToolResultEventPayload,
  TurnEndEventPayload,
  UnknownEvent,
  UnknownGrpcEvent,
  UnknownHttpEvent,
  UserPromptEventPayload,
} from "./events.js";
export { MECATL_EVENT_KINDS, RetryDisposition, StreamProgress } from "./events.js";
export type { HttpTransportOptions } from "./http.js";
export { createHttpTransport } from "./http.js";
export type {
  McpAuthorization,
  McpAuthorizationFlow,
  McpAuthorizationFlowOptions,
  McpAuthorizationOperation,
  McpAuthorizationResult,
  McpAuthorizationStatus,
} from "./mcp-authorization.js";
export type {
  McpConnectorInventory,
  McpConnectorStatus,
  WorkspaceEnrollment,
} from "./mcp-workspace-enrollment.js";
export {
  McpConnectorAvailability,
  McpConnectorCatalogueState,
  McpConnectorEnrollmentState,
  WorkspaceEnrollmentStatus,
} from "./mcp-workspace-enrollment.js";
export type {
  AudioPromptPart,
  ImagePromptPart,
  MediaPartOptions,
  MediaPartSource,
  PromptInput,
  PromptPart,
  TextPromptPart,
} from "./media.js";
export {
  audioPart,
  audioPartFromBlob,
  imagePart,
  imagePartFromBlob,
  MAX_MEDIA_PART_BYTES,
  MAX_PROMPT_MEDIA_BYTES,
  MAX_PROMPT_MEDIA_PARTS,
  textPart,
} from "./media.js";
export type {
  Agents,
  Commands,
  McpInventory,
  Models,
  RequestOptions,
  Worktrees,
} from "./namespaces-core.js";
export type {
  DreamPlans,
  LearnedSkills,
  LearningAttempts,
  LearningProposals,
  Reflection,
  Schedules,
  Skills,
  Soul,
  Storage,
  UserModel,
} from "./namespaces-ops.js";
export type {
  PlanApprovalResponder,
  PlanApprovalVerdict,
  PlanResolution,
  PlanResolutionResult,
} from "./plan.js";
export type { RawClient, RawClientOptions } from "./raw.js";
export {
  createRawClient,
  getRawJson,
  SESSION_ID_HEADER_NAME,
  SUPPORTED_API_MAJOR,
  withSessionAffinity,
} from "./raw.js";
export type {
  PermissionAskResponder,
  Run,
  RunAuthorizationRequiredOutcome,
  RunCompletedOutcome,
  RunOptions,
  RunOutcome,
  RunResult,
} from "./run.js";
export { PermissionVerdict, RunAuthorizationRequiredError } from "./run.js";
export type {
  RunControls,
  RunSteerAcknowledgement,
  RunSteerCancellationAcknowledgement,
  RunSteerOptions,
} from "./run-controls.js";
export type {
  Server,
  ServerCompatibility,
  ServerInfo,
  ServerInfoOptions,
} from "./server.js";
export { ServerFeature, ServerPosture } from "./server.js";
export type {
  DreamTargetCapability,
  ManualDreamCapabilities,
  ServerCapabilities,
  SessionActivityReplayStatus,
  SessionCapabilities,
  SessionPlacement,
  SessionRelationship,
  SessionResolvedModel,
  SessionSnapshot,
  SessionSnapshotLimits,
  SessionTitle,
  SessionTitleAttempt,
  SessionTokenUsage,
  SessionTranscript,
  SessionTranscriptMessage,
} from "./session-projections.js";
export { SessionMode } from "./session-projections.js";
export type {
  CreateTeamOptions,
  Team,
  TeamMemberOptions,
  TeamMemberRunEvent,
  TeamMessageOptions,
  TeamOutcomeRunEvent,
  TeamRun,
  TeamRunEvent,
  Teams,
} from "./team.js";
export type {
  AttachedRun,
  AttachOptions,
  SdkCursor,
  SessionActivity,
  UnknownWatchEnvelope,
  WatchBoundaryEnvelope,
  WatchEnvelope,
  WatchEventEnvelope,
  WatchGapEnvelope,
} from "./watch.js";
export { MECATL_ATTACH_FILTERED_KINDS, MECATL_WATCH_PHASES } from "./watch.js";
