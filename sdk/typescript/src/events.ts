import type { JsonValue, UnknownField } from "@bufbuild/protobuf";

import { ProtocolError, type TransportKind } from "./errors.js";
import type { Event as ProtoEvent } from "./gen/mecatl/v1/harness_pb.js";
import { getRawJson } from "./raw.js";

// BEGIN MECATL_EVENT_KINDS
/** Stable event kinds, kept in parity with the Go server vocabulary. @public */
export const MECATL_EVENT_KINDS = [
  "approval",
  "authorization.required",
  "authorization.resolved",
  "compaction",
  "compaction.archive",
  "hook",
  "message.delta",
  "model.retry",
  "network.attempt",
  "no_progress",
  "parallel.branch",
  "parallel.end",
  "parallel.start",
  "permission.ask",
  "permission.retract",
  "provider.route",
  "reasoning.delta",
  "recover_notice",
  "request.manifest",
  "result",
  "schedule.failed",
  "schedule.fired",
  "schedule.skipped",
  "session.init",
  "session.title",
  "steer",
  "steer.outcome",
  "subagent.end",
  "subagent.start",
  "subagent.tool",
  "team.end",
  "team.findings",
  "team.member",
  "team.start",
  "team.tasks",
  "tool.call",
  "tool.progress",
  "tool.result",
  "turn.end",
  "turn.start",
  "user_prompt",
] as const;
// END MECATL_EVENT_KINDS

/** A wire event kind currently understood by this SDK. @public */
export type KnownEventKind = (typeof MECATL_EVENT_KINDS)[number];

/** Token accounting carried by usage-bearing events. @public */
export interface EventUsage {
  readonly cacheReadTokens: bigint;
  readonly cacheWriteTokens: bigint;
  readonly inputTokens: bigint;
  readonly outputTokens: bigint;
  readonly reasoningTokens: bigint;
}

/** The payload of a `tool.call` event. @public */
export interface ToolCallEventPayload {
  readonly args: string;
  readonly id: string;
  readonly name: string;
}

/** One raw protobuf content block carried by a tool result. @public */
export interface EventContentBlock {
  readonly audience: readonly string[];
  readonly data: Uint8Array;
  readonly description: string;
  readonly kind: 0 | 1 | 2 | 3 | 4 | 5 | 6;
  readonly lastModified: string;
  readonly mimeType: string;
  readonly name: string;
  readonly priority: number;
  readonly size: bigint;
  readonly text: string;
  readonly title: string;
  readonly url: string;
}

/** The text, structured data, and content blocks from a `tool.result` event. @public */
export interface ToolResultEventPayload {
  readonly blocks: readonly EventContentBlock[];
  readonly callId: string;
  readonly content: string;
  readonly isError: boolean;
  readonly structuredContent: string;
}

/** The payload shared by `permission.ask` and `permission.retract`. @public */
export interface PermissionAskEventPayload {
  readonly args: string;
  readonly askId: string;
  readonly reason: string;
  readonly tool: string;
}

/** Retry classification fields carried by model-retry and result payloads. @public */
export type RetryDisposition = 0 | 1 | 2 | 3;

/** Stream-progress classification carried by model-retry and result payloads. @public */
export type StreamProgress = 0 | 1 | 2 | 3 | 4;

/** The payload of a `model.retry` event. @public */
export interface ModelRetryEventPayload {
  readonly retryDisposition: RetryDisposition;
  readonly streamProgress: StreamProgress;
}

/** The payload of a terminal `result` event. @public */
export interface ResultEventPayload {
  readonly error: string;
  readonly permanent: boolean;
  readonly retryDisposition?: RetryDisposition | undefined;
  readonly stop: string;
  readonly streamProgress?: StreamProgress | undefined;
  readonly text: string;
  readonly usage?: EventUsage | undefined;
}

/** The payload of a `turn.end` event. @public */
export interface TurnEndEventPayload {
  readonly durationMs: bigint;
  readonly usage?: EventUsage | undefined;
}

/** The payload of a `hook` event. @public */
export interface HookEventPayload {
  readonly callId: string;
  readonly decision: 0 | 1 | 2 | 3 | 4;
  readonly phase: string;
  readonly tool: string;
}

/** The payload of an `approval` replay event. @public */
export interface ApprovalEventPayload {
  readonly allowAlways: boolean;
  readonly askId: string;
  readonly callId: string;
  readonly tool: string;
  readonly verdict: string;
}

/** The safe correlation payload of an external-authorization lifecycle event. @public */
export interface AuthorizationEventPayload {
  readonly authorizationId: string;
  readonly callId: string;
  readonly displayName: string;
  readonly expiresAt?: { readonly nanos: number; readonly seconds: bigint } | undefined;
  readonly status: string;
}

/** One media part as represented on the protobuf event payloads. @public */
export interface EventContent {
  readonly data: Uint8Array;
  readonly kind: 0 | 1 | 2;
  readonly mimeType: string;
  readonly url: string;
}

/** The payload shared by `user_prompt` replay events. @public */
export interface UserPromptEventPayload {
  readonly parts: readonly EventContent[];
  readonly text: string;
}

/** One title-generation attempt projected by a `session.title` event. @public */
export interface TitleAttemptEventPayload {
  readonly id: string;
  readonly outcome: string;
}

/** The source-free payload of a `session.title` event. @public */
export interface SessionTitleEventPayload {
  readonly generationState: string;
  readonly latestAttempt?: TitleAttemptEventPayload | undefined;
  readonly provenance: string;
  readonly revision: bigint;
  readonly title: string;
}

/** One conversation entry in a compaction archive. @public */
export interface ArchivedConversationMessage {
  readonly parts: readonly EventContent[];
  readonly providerPhase: string;
  readonly reasoning: string;
  readonly reasoningItemId: string;
  readonly role: string;
  readonly text: string;
  readonly toolCalls: readonly ToolCallEventPayload[];
  readonly toolResult?: ToolResultEventPayload | undefined;
}

/** The payload of a `compaction.archive` replay event. @public */
export interface CompactionArchiveEventPayload {
  readonly replaced: readonly ArchivedConversationMessage[];
}

/** The payload of a committed `steer` event. @public */
export interface SteerEventPayload {
  readonly messageId: string;
  readonly parts: readonly EventContent[];
  readonly text: string;
}

/** The payload of a gRPC-only `steer.outcome` event. @public */
export interface SteerOutcomeEventPayload {
  readonly messageId: string;
  readonly outcome: 0 | 1 | 2 | 3 | 4 | 5;
  readonly promoted: boolean;
  readonly text: string;
}

/** The payload shared by `schedule.*` events. @public */
export interface ScheduleEventPayload {
  readonly err: string;
  readonly fireId: string;
  readonly kind: string;
  readonly scheduleName: string;
  readonly sessionId: string;
  readonly stop: string;
}

/** Bounded configured-router evidence on delegation start events. @public */
export interface RoutingDecisionEventPayload {
  readonly backend: string;
  readonly breakerOpen: boolean;
  readonly candidateCategory: string;
  readonly candidateModel: string;
  readonly classifierModel: string;
  readonly confidence?: number | undefined;
  readonly consecutiveMisses: number;
  readonly minimumConfidence?: number | undefined;
  readonly missLimit: number;
  readonly outcome: string;
}

/** The payload shared by `subagent.*` events. @public */
export interface SubagentEventPayload {
  readonly background: boolean;
  readonly cause: string;
  readonly childId: string;
  readonly detail: string;
  readonly durationMs: bigint;
  readonly goal: string;
  readonly innerKind: string;
  readonly isError: boolean;
  readonly model: string;
  readonly parentCallId: string;
  readonly routedCategory: string;
  readonly routedModel: string;
  readonly routingDecision?: RoutingDecisionEventPayload | undefined;
  readonly routingReason: string;
  readonly stop: string;
  readonly text: string;
  readonly toolCount: number;
  readonly toolName: string;
  readonly usage?: EventUsage | undefined;
}

/** One member in a `team.start` roster. @public */
export interface TeamMemberSpecEventPayload {
  readonly lead: boolean;
  readonly model: string;
  readonly mutating: boolean;
  readonly name: string;
  readonly role: string;
  readonly routedCategory: string;
  readonly routedModel: string;
  readonly routingDecision?: RoutingDecisionEventPayload | undefined;
  readonly routingReason: string;
}

/** One task in a team event snapshot. @public */
export interface TeamTaskEventPayload {
  readonly assignee: string;
  readonly deps: readonly string[];
  readonly description: string;
  readonly id: string;
  readonly state: string;
}

/** One finding in a team event snapshot. @public */
export interface TeamFindingEventPayload {
  readonly body: string;
  readonly member: string;
}

/** One terminal member disposition in a `team.end` payload. @public */
export interface TeamMemberDispositionEventPayload {
  readonly errorRounds: number;
  readonly name: string;
  readonly reason: 0 | 1 | 2 | 3;
  readonly stopped: boolean;
}

/** The payload shared by `team.*` events. @public */
export interface TeamEventPayload {
  readonly cause: string;
  readonly contextUsed: bigint;
  readonly contextWindow: bigint;
  readonly detail: string;
  readonly dispositions: readonly TeamMemberDispositionEventPayload[];
  readonly findings: readonly TeamFindingEventPayload[];
  readonly innerKind: string;
  readonly isError: boolean;
  readonly member: string;
  readonly memberSessionId: string;
  readonly parentCallId: string;
  readonly roster: readonly TeamMemberSpecEventPayload[];
  readonly rounds: number;
  readonly stop: string;
  readonly tasks: readonly TeamTaskEventPayload[];
  readonly teamId: string;
  readonly text: string;
  readonly toolName: string;
  readonly usage?: EventUsage | undefined;
}

/** The payload shared by `parallel.*` events. @public */
export interface ParallelEventPayload {
  readonly branchCount: number;
  readonly branchIndex: number;
  readonly branchLabel: string;
  readonly childId: string;
  readonly detail: string;
  readonly durationMs: bigint;
  readonly failed: boolean;
  readonly goal: string;
  readonly innerKind: string;
  readonly isError: boolean;
  readonly join: string;
  readonly kind: string;
  readonly model: string;
  readonly parentCallId: string;
  readonly routedCategory: string;
  readonly routedModel: string;
  readonly routingDecision?: RoutingDecisionEventPayload | undefined;
  readonly routingReason: string;
  readonly stop: string;
  readonly text: string;
  readonly toolCount: number;
  readonly toolName: string;
  readonly usage?: EventUsage | undefined;
  readonly winner: number;
  readonly winnerWorkspace: string;
  readonly workspace: string;
}

/** Fields decoded for every event, including future event kinds. @public */
export interface EventCommon {
  readonly runId: string;
  readonly seq: bigint;
  readonly text: string;
  readonly turn: number;
  readonly usage: EventUsage | undefined;
}

/** Maps every supported event kind to its typed payload. @public */
export interface EventPayloads {
  readonly approval: ApprovalEventPayload;
  readonly "authorization.required": AuthorizationEventPayload;
  readonly "authorization.resolved": AuthorizationEventPayload;
  readonly compaction: undefined;
  readonly "compaction.archive": CompactionArchiveEventPayload;
  readonly hook: HookEventPayload;
  readonly "message.delta": undefined;
  readonly "model.retry": ModelRetryEventPayload;
  readonly "network.attempt": undefined;
  readonly no_progress: undefined;
  readonly "parallel.branch": ParallelEventPayload;
  readonly "parallel.end": ParallelEventPayload;
  readonly "parallel.start": ParallelEventPayload;
  readonly "permission.ask": PermissionAskEventPayload;
  readonly "permission.retract": PermissionAskEventPayload;
  readonly "provider.route": undefined;
  readonly "reasoning.delta": undefined;
  readonly recover_notice: undefined;
  readonly "request.manifest": undefined;
  readonly result: ResultEventPayload;
  readonly "schedule.failed": ScheduleEventPayload;
  readonly "schedule.fired": ScheduleEventPayload;
  readonly "schedule.skipped": ScheduleEventPayload;
  readonly "session.init": undefined;
  readonly "session.title": SessionTitleEventPayload;
  readonly steer: SteerEventPayload;
  readonly "steer.outcome": SteerOutcomeEventPayload;
  readonly "subagent.end": SubagentEventPayload;
  readonly "subagent.start": SubagentEventPayload;
  readonly "subagent.tool": SubagentEventPayload;
  readonly "team.end": TeamEventPayload;
  readonly "team.findings": TeamEventPayload;
  readonly "team.member": TeamEventPayload;
  readonly "team.start": TeamEventPayload;
  readonly "team.tasks": TeamEventPayload;
  readonly "tool.call": ToolCallEventPayload;
  readonly "tool.progress": undefined;
  readonly "tool.result": ToolResultEventPayload;
  readonly "turn.end": TurnEndEventPayload;
  readonly "turn.start": undefined;
  readonly user_prompt: UserPromptEventPayload;
}

/** All currently known agent and team event variants. @public */
export type KnownEvent = {
  [Kind in KnownEventKind]: EventCommon & {
    readonly kind: Kind;
    readonly payload: EventPayloads[Kind];
  };
}[KnownEventKind];

/** The agent-lifecycle portion of the known event union. @public */
export type AgentEvent = Exclude<KnownEvent, { readonly kind: `team.${string}` }>;

/** Team lifecycle events projected onto an agent run. @public */
export type TeamEvent = Extract<KnownEvent, { readonly kind: `team.${string}` }>;

/** An unknown event received over the HTTP JSON/SSE transport. @public */
export interface UnknownHttpEvent extends EventCommon {
  readonly kind: "unknown";
  /** The exact parsed JSON object received in the SSE frame. */
  readonly rawData: JsonValue;
  readonly transport: "http";
  readonly wireKind: string;
}

/** An unknown event received over a protobuf transport. @public */
export interface UnknownGrpcEvent extends EventCommon {
  readonly kind: "unknown";
  /** The protobuf unknown fields, preserving their wire order and payload bytes. */
  readonly rawData: Uint8Array;
  readonly transport: "grpc";
  readonly wireKind: string;
}

/** A future wire event that this SDK does not yet type. @public */
export type UnknownEvent = UnknownHttpEvent | UnknownGrpcEvent;

/** A decoded agent or team event. @public */
export type Event = KnownEvent | UnknownEvent;

/** Selects one known event variant by its literal kind. @public */
export type EventOf<Kind extends KnownEventKind> = Extract<KnownEvent, { readonly kind: Kind }>;

const knownKinds = new Set<string>(MECATL_EVENT_KINDS);

export function decodeEvent(event: ProtoEvent, transport: TransportKind): Event {
  const common: EventCommon = {
    runId: event.runId,
    seq: event.seq,
    text: event.text,
    turn: event.turn,
    usage: event.usage,
  };
  if (!knownKinds.has(event.type)) {
    if (transport === "http") {
      return {
        ...common,
        kind: "unknown",
        rawData: getRawJson(event) ?? null,
        transport,
        wireKind: event.type,
      };
    }
    return {
      ...common,
      kind: "unknown",
      rawData: unknownFieldBytes(event.$unknown),
      transport,
      wireKind: event.type,
    };
  }

  const kind = event.type as KnownEventKind;
  return { ...common, kind, payload: payload(event, kind, transport) } as KnownEvent;
}

function payload(
  event: ProtoEvent,
  kind: KnownEventKind,
  transport: TransportKind,
): EventPayloads[KnownEventKind] {
  switch (kind) {
    case "approval":
      return required(event.approval, kind, transport);
    case "authorization.required":
    case "authorization.resolved":
      return required(event.authorization, kind, transport);
    case "compaction.archive":
      return required(event.compactionArchive, kind, transport);
    case "hook":
      return required(event.hook, kind, transport);
    case "model.retry":
      return required(event.modelRetry, kind, transport);
    case "parallel.branch":
    case "parallel.end":
    case "parallel.start":
      return required(event.parallel, kind, transport);
    case "permission.ask":
    case "permission.retract":
      return required(event.ask, kind, transport);
    case "result":
      return required(event.result, kind, transport);
    case "schedule.failed":
    case "schedule.fired":
    case "schedule.skipped":
      return required(event.schedule, kind, transport);
    case "session.title":
      return required(event.title, kind, transport);
    case "steer":
      return required(event.steer, kind, transport);
    case "steer.outcome":
      return required(event.steerOutcome, kind, transport);
    case "subagent.end":
    case "subagent.start":
    case "subagent.tool":
      return required(event.subagent, kind, transport);
    case "team.end":
    case "team.findings":
    case "team.member":
    case "team.start":
    case "team.tasks":
      return required(event.team, kind, transport);
    case "tool.call":
      return required(event.toolCall, kind, transport);
    case "tool.result":
      return required(event.toolResult, kind, transport);
    case "turn.end":
      return required(event.turnEnd, kind, transport);
    case "user_prompt":
      return required(event.userPrompt, kind, transport);
    case "compaction":
    case "message.delta":
    case "network.attempt":
    case "no_progress":
    case "provider.route":
    case "reasoning.delta":
    case "recover_notice":
    case "request.manifest":
    case "session.init":
    case "tool.progress":
    case "turn.start":
      return undefined;
  }
}

function required<T>(value: T | undefined, kind: KnownEventKind, transport: TransportKind): T {
  if (value !== undefined) return value;
  throw new ProtocolError(`The ${kind} event did not carry its required payload`, { transport });
}

function unknownFieldBytes(fields: readonly UnknownField[] | undefined): Uint8Array {
  if (fields === undefined || fields.length === 0) return new Uint8Array();
  const bytes: number[] = [];
  for (const field of fields) {
    writeVarint((field.no << 3) | field.wireType, bytes);
    bytes.push(...field.data);
  }
  return Uint8Array.from(bytes);
}

function writeVarint(value: number, output: number[]): void {
  let remaining = value >>> 0;
  while (remaining > 0x7f) {
    output.push((remaining & 0x7f) | 0x80);
    remaining >>>= 7;
  }
  output.push(remaining);
}
