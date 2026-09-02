/**
 * Transport-neutral mecatl SDK entry point.
 *
 * @packageDocumentation
 */

export type { Transport } from "@connectrpc/connect";
export type {
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
  ErrorOrigin,
  MecatlErrorCode,
  MecatlErrorOptions,
  PromptValidationReason,
  SDKErrorCode,
  ServerErrorCode,
  TransportKind,
} from "./errors.js";
export {
  AuthenticationError,
  CursorMalformedError,
  CursorScopeError,
  IncompatibleServerError,
  InvalidStateError,
  MECATL_ERROR_CODES,
  MecatlError,
  NoRunsError,
  PermissionAskAlreadyResolvedError,
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
  Event,
  EventCommon,
  EventContent,
  EventContentBlock,
  EventOf,
  EventPayloads,
  EventUsage,
  HookEventPayload,
  KnownEvent,
  KnownEventKind,
  ModelRetryEventPayload,
  ParallelEventPayload,
  PermissionAskEventPayload,
  ResultEventPayload,
  RetryDisposition,
  ScheduleEventPayload,
  SteerEventPayload,
  SteerOutcomeEventPayload,
  StreamProgress,
  SubagentEventPayload,
  TeamEvent,
  TeamEventPayload,
  TeamFindingEventPayload,
  TeamMemberDispositionEventPayload,
  TeamMemberSpecEventPayload,
  TeamTaskEventPayload,
  ToolCallEventPayload,
  ToolResultEventPayload,
  TurnEndEventPayload,
  UnknownEvent,
  UnknownGrpcEvent,
  UnknownHttpEvent,
  UserPromptEventPayload,
} from "./events.js";
export { MECATL_EVENT_KINDS } from "./events.js";
export type { HttpTransportOptions } from "./http.js";
export { createHttpTransport } from "./http.js";
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
export type { RawClient, RawClientOptions } from "./raw.js";
export { createRawClient, getRawJson, SUPPORTED_API_MAJOR } from "./raw.js";
export type {
  PermissionAskResponder,
  PermissionVerdict,
  Run,
  RunOptions,
  RunResult,
} from "./run.js";
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
