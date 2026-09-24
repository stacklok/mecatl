import type {
  EventContent,
  EventContentBlock,
  EventUsage,
  ToolCallEventPayload,
  ToolResultEventPayload,
} from "./events.js";
import type {
  Content,
  ContentBlock,
  ConversationMessage,
  GetSessionTranscriptResponse,
  DreamTargetCapability as ProtoDreamTargetCapability,
  ManualDreamCapabilities as ProtoManualDreamCapabilities,
  ServerCapabilities as ProtoServerCapabilities,
  Session as ProtoSession,
  SessionRelationship as ProtoSessionRelationship,
  ToolCall,
  ToolResult,
  Usage,
} from "./gen/mecatl/v1/harness_pb.js";

/** SDK permission modes accepted by session creation and mutation operations. @public */
export const SessionMode = {
  Unspecified: 0,
  Default: 1,
  Plan: 2,
  AcceptEdits: 3,
} as const;

/** One SDK permission-mode value. @public */
export type SessionMode = (typeof SessionMode)[keyof typeof SessionMode];

/** Stop conditions reported by a session snapshot. @public */
export interface SessionSnapshotLimits {
  readonly maxTurns: number;
  readonly maxToolCalls: number;
  readonly maxConsecutiveFailures: number;
}

/** The effective provider and model reported for a session. @public */
export interface SessionResolvedModel {
  readonly providerId: string;
  readonly modelId: string;
  readonly contextWindow: bigint;
  readonly reasoningEffort?: string;
}

/** Media input support for the provider and model bound to a session. @public */
export interface SessionCapabilities {
  readonly image: boolean;
  readonly audio: boolean;
  readonly pdf: boolean;
}

/** Bounded display metadata for a session placement. @public */
export interface SessionPlacement {
  readonly kind: string;
  readonly label: string;
  readonly branch: string;
  readonly revision: string;
}

/** Durable links between a session and its parent resource. @public */
export interface SessionRelationship {
  readonly parentSessionId?: string;
  readonly callId?: string;
  readonly branchIndex?: number;
  readonly scheduleName?: string;
  readonly originSessionId?: string;
  readonly teamId?: string;
  readonly memberName?: string;
  readonly debugTargetSessionId?: string;
}

/** The latest bounded title-generation attempt. @public */
export interface SessionTitleAttempt {
  readonly id: string;
  readonly outcome: string;
}

/** Normalized session title metadata. @public */
export interface SessionTitle {
  readonly value: string;
  readonly provenance: string;
  readonly generationState?: string;
  readonly latestAttempt?: SessionTitleAttempt;
  readonly revision?: bigint;
}

/** One durable session usage bucket. @public */
export interface SessionTokenUsage {
  readonly total?: EventUsage;
  readonly models: Readonly<Record<string, EventUsage>>;
}

/** Manual dream operations available for one target. @public */
export interface DreamTargetCapability {
  readonly generate: boolean;
  readonly decide: boolean;
  readonly unavailableReason?: string;
}

/** Manual dream support for deployment-owned targets. @public */
export interface ManualDreamCapabilities {
  readonly projectMemory?: DreamTargetCapability;
  readonly userModel?: DreamTargetCapability;
}

/** Optional server features captured with a session snapshot. @public */
export interface ServerCapabilities {
  readonly pdfArtifacts: boolean;
  readonly mcp: boolean;
  readonly slashCommands: boolean;
  readonly memory: boolean;
  readonly skills: boolean;
  readonly teams: boolean;
  readonly shell: boolean;
  readonly image: boolean;
  readonly audio: boolean;
  readonly agents: boolean;
  readonly soul: boolean;
  readonly userModel: boolean;
  readonly modelSelection: boolean;
  readonly posture: string;
  readonly worktrees: boolean;
  readonly scheduling: boolean;
  readonly reflection: boolean;
  readonly learningProposals: boolean;
  readonly learnedSkills: boolean;
  readonly manualDream?: ManualDreamCapabilities;
  readonly storageCleanup: boolean;
  readonly storageHealth: boolean;
  readonly steer: boolean;
  readonly manualCompaction: boolean;
  readonly sessionDebug: boolean;
  readonly debugMcp: boolean;
  readonly workspaceEnrollment: boolean;
  readonly mcpConnectorStatus: boolean;
}

/** An authoritative, detached view of one durable session. @public */
export interface SessionSnapshot {
  readonly sessionId: string;
  readonly state: string;
  readonly mode: SessionMode;
  readonly limits?: SessionSnapshotLimits;
  readonly turns: number;
  readonly toolCalls: number;
  readonly createdAtUnix: bigint;
  readonly resolvedModel?: SessionResolvedModel;
  readonly kind: string;
  readonly relationship?: SessionRelationship;
  readonly debugMcpServers: readonly string[];
  readonly debugMcpTools: readonly string[];
  readonly placement?: SessionPlacement;
  readonly title?: SessionTitle;
  readonly tokenUsage: Readonly<Record<string, SessionTokenUsage>>;
  readonly sessionCapabilities?: SessionCapabilities;
}

/** One human-displayable message in the authoritative session transcript. @public */
export interface SessionTranscriptMessage {
  readonly role: string;
  readonly text: string;
  readonly toolCalls: readonly ToolCallEventPayload[];
  readonly toolResult?: ToolResultEventPayload;
  readonly parts: readonly EventContent[];
}

/** Availability and completeness of the separate activity replay plane. @public */
export interface SessionActivityReplayStatus {
  readonly available: boolean;
  readonly complete: boolean;
  readonly authoritative: boolean;
}

/** The authoritative, ordered conversation for one session. @public */
export interface SessionTranscript {
  readonly sessionId: string;
  readonly messages: readonly SessionTranscriptMessage[];
  readonly complete: boolean;
  readonly activity?: SessionActivityReplayStatus;
  readonly kind: string;
  readonly relationship?: SessionRelationship;
}

function present(value: string): string | undefined {
  return value === "" ? undefined : value;
}

function usage(value: Usage): EventUsage {
  return {
    cacheReadTokens: value.cacheReadTokens,
    cacheWriteTokens: value.cacheWriteTokens,
    inputTokens: value.inputTokens,
    outputTokens: value.outputTokens,
    reasoningTokens: value.reasoningTokens,
  };
}

function relationship(value: ProtoSessionRelationship): SessionRelationship {
  return {
    ...(present(value.parentSessionId) === undefined
      ? {}
      : { parentSessionId: value.parentSessionId }),
    ...(present(value.callId) === undefined ? {} : { callId: value.callId }),
    ...(value.branchIndex === undefined ? {} : { branchIndex: value.branchIndex }),
    ...(present(value.scheduleName) === undefined ? {} : { scheduleName: value.scheduleName }),
    ...(present(value.originSessionId) === undefined
      ? {}
      : { originSessionId: value.originSessionId }),
    ...(present(value.teamId) === undefined ? {} : { teamId: value.teamId }),
    ...(present(value.memberName) === undefined ? {} : { memberName: value.memberName }),
    ...(present(value.debugTargetSessionId) === undefined
      ? {}
      : { debugTargetSessionId: value.debugTargetSessionId }),
  };
}

function dreamTarget(value: ProtoDreamTargetCapability): DreamTargetCapability {
  return {
    decide: value.decide,
    generate: value.generate,
    ...(present(value.unavailableReason) === undefined
      ? {}
      : { unavailableReason: value.unavailableReason }),
  };
}

function manualDream(value: ProtoManualDreamCapabilities): ManualDreamCapabilities {
  return {
    ...(value.projectMemory === undefined
      ? {}
      : { projectMemory: dreamTarget(value.projectMemory) }),
    ...(value.userModel === undefined ? {} : { userModel: dreamTarget(value.userModel) }),
  };
}

/** Internal shared projection for server- and session-scoped capability responses. */
export function projectServerCapabilities(value: ProtoServerCapabilities): ServerCapabilities {
  return {
    pdfArtifacts: value.pdfArtifacts,
    agents: value.agents,
    audio: value.audio,
    shell: value.shell,
    debugMcp: value.debugMcp,
    image: value.image,
    learnedSkills: value.learnedSkills,
    learningProposals: value.learningProposals,
    manualCompaction: value.manualCompaction,
    ...(value.manualDream === undefined ? {} : { manualDream: manualDream(value.manualDream) }),
    mcp: value.mcp,
    mcpConnectorStatus: value.mcpConnectorStatus,
    memory: value.memory,
    modelSelection: value.modelSelection,
    posture: value.posture,
    reflection: value.reflection,
    scheduling: value.scheduling,
    sessionDebug: value.sessionDebug,
    skills: value.skills,
    slashCommands: value.slashCommands,
    soul: value.soul,
    steer: value.steer,
    storageCleanup: value.storageCleanup,
    storageHealth: value.storageHealth,
    teams: value.teams,
    userModel: value.userModel,
    workspaceEnrollment: value.workspaceEnrollment,
    worktrees: value.worktrees,
  };
}

function sessionMode(value: number): SessionMode | undefined {
  return value === SessionMode.Unspecified ||
    value === SessionMode.Default ||
    value === SessionMode.Plan ||
    value === SessionMode.AcceptEdits
    ? value
    : undefined;
}

function toolCall(value: ToolCall): ToolCallEventPayload {
  return { args: value.args, id: value.id, name: value.name };
}

function contentBlock(value: ContentBlock): EventContentBlock {
  return {
    artifactId: value.artifactId,
    audience: [...value.audience],
    data: value.data.slice(),
    description: value.description,
    kind: value.kind,
    lastModified: value.lastModified,
    mimeType: value.mimeType,
    name: value.name,
    priority: value.priority,
    size: value.size,
    sha256: value.sha256,
    text: value.text,
    title: value.title,
    url: value.url,
  };
}

function toolResult(value: ToolResult): ToolResultEventPayload {
  return {
    blocks: value.blocks.map(contentBlock),
    callId: value.callId,
    content: value.content,
    isError: value.isError,
    structuredContent: value.structuredContent,
  };
}

function content(value: Content): EventContent {
  return {
    artifactId: value.artifactId,
    data: value.data.slice(),
    kind: value.kind,
    mimeType: value.mimeType,
    name: value.name,
    size: value.size,
    sha256: value.sha256,
    url: value.url,
  };
}

function transcriptMessage(value: ConversationMessage): SessionTranscriptMessage {
  return {
    parts: value.parts.map(content),
    role: value.role,
    text: value.text,
    toolCalls: value.toolCalls.map(toolCall),
    ...(value.toolResult === undefined ? {} : { toolResult: toolResult(value.toolResult) }),
  };
}

/** Internal projection shared by GetSession and snapshot-returning operations. */
export function projectSessionSnapshot(
  value: ProtoSession,
  expectedSessionId: string,
): SessionSnapshot | undefined {
  const mode = sessionMode(value.mode);
  if (value.sessionId === "" || value.sessionId !== expectedSessionId || mode === undefined) {
    return undefined;
  }
  const tokenUsage: Record<string, SessionTokenUsage> = Object.create(null);
  for (const [key, bucket] of Object.entries(value.tokenUsage)) {
    const models: Record<string, EventUsage> = Object.create(null);
    for (const [model, modelUsage] of Object.entries(bucket.models)) {
      models[model] = usage(modelUsage);
    }
    tokenUsage[key] = {
      models,
      ...(bucket.total === undefined ? {} : { total: usage(bucket.total) }),
    };
  }
  const title =
    value.titleMetadata === undefined
      ? undefined
      : {
          ...(value.titleMetadata.generationState === ""
            ? {}
            : { generationState: value.titleMetadata.generationState }),
          ...(value.titleMetadata.latestAttempt === undefined
            ? {}
            : {
                latestAttempt: {
                  id: value.titleMetadata.latestAttempt.id,
                  outcome: value.titleMetadata.latestAttempt.outcome,
                },
              }),
          provenance: value.titleMetadata.provenance,
          ...(value.titleMetadata.revision === 0n
            ? {}
            : { revision: value.titleMetadata.revision }),
          value: value.titleMetadata.title,
        };
  return {
    createdAtUnix: value.createdAtUnix,
    debugMcpServers: [...value.debugMcpServers],
    debugMcpTools: [...value.debugMcpTools],
    kind: value.kind,
    ...(value.limits === undefined
      ? {}
      : {
          limits: {
            maxConsecutiveFailures: value.limits.maxConsecutiveFailures,
            maxToolCalls: value.limits.maxToolCalls,
            maxTurns: value.limits.maxTurns,
          },
        }),
    mode,
    ...(value.placement === undefined
      ? {}
      : {
          placement: {
            branch: value.placement.branch,
            kind: value.placement.kind,
            label: value.placement.label,
            revision: value.placement.revision,
          },
        }),
    ...(value.relationship === undefined ? {} : { relationship: relationship(value.relationship) }),
    ...(value.resolvedModel === undefined
      ? {}
      : {
          resolvedModel: {
            contextWindow: value.resolvedModel.contextWindow,
            modelId: value.resolvedModel.modelId,
            providerId: value.resolvedModel.providerId,
            ...(value.resolvedModel.reasoningEffort === ""
              ? {}
              : { reasoningEffort: value.resolvedModel.reasoningEffort }),
          },
        }),
    ...(value.sessionCapabilities === undefined
      ? {}
      : {
          sessionCapabilities: {
            audio: value.sessionCapabilities.audio,
            image: value.sessionCapabilities.image,
            pdf: value.sessionCapabilities.pdf,
          },
        }),
    sessionId: value.sessionId,
    state: value.state,
    ...(title === undefined ? {} : { title }),
    tokenUsage,
    toolCalls: value.toolCalls,
    turns: value.turns,
  };
}

/** Internal projection for the authoritative transcript RPC. */
export function projectSessionTranscript(
  value: GetSessionTranscriptResponse,
  expectedSessionId: string,
): SessionTranscript | undefined {
  if (value.sessionId === "" || value.sessionId !== expectedSessionId) return undefined;
  return {
    ...(value.activity === undefined
      ? {}
      : {
          activity: {
            authoritative: value.activity.authoritative,
            available: value.activity.available,
            complete: value.activity.complete,
          },
        }),
    complete: value.complete,
    kind: value.kind,
    messages: value.messages.map(transcriptMessage),
    ...(value.relationship === undefined ? {} : { relationship: relationship(value.relationship) }),
    sessionId: value.sessionId,
  };
}
