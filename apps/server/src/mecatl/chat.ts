// SPDX-License-Identifier: Apache-2.0

import type {
  CreateSessionRequest,
  ForkSessionRequest,
  ListSessionsResponse,
  RunStreamEvent,
  SessionDetailResponse,
  SessionSummaryResponse,
  SessionTranscriptResponse,
  SessionUsageResponse,
  StartRunRequest,
} from "@mecatl-studio/contracts";
import {
  type Client,
  type Event,
  type EventUsage,
  imagePart,
  MecatlError,
  type PermissionVerdict,
  type Run,
  SessionMode,
  textPart,
} from "@stacklok-oss/mecatl-sdk";
import { sanitizeUpstreamDetail } from "../http/problem.js";

const maximumInventoryPages = 20;
const inventoryPageSize = 100;

/** How one activity request reads durable history. */
export interface ActivityRequest {
  /** Resume exactly after this durable cursor; absent starts a bounded replay from the beginning. */
  readonly cursor?: string;
  /** Durable REPLAY events this request forwards before reporting truncation. */
  readonly replayMax: number;
  readonly signal: AbortSignal;
}

/**
 * One frame plus the durable cursor it projects. The route puts that cursor in
 * the SSE `id` field so a client resumes exactly after the frame it last saw;
 * frames with no durable position (`run.started`, `run.truncated`) carry none.
 * An item with no `delivery` marks the replay→live boundary: nothing is sent,
 * but the stream is known to be attached, so the route may commit its headers
 * without waiting for a live event that may never come.
 */
export interface ActivityDelivery {
  readonly cursor?: string;
  readonly delivery?: RunStreamEvent;
}

export interface ChatService {
  activity(sessionId: string, request: ActivityRequest): AsyncIterable<ActivityDelivery>;
  cancelRun(sessionId: string, runId: string): Promise<boolean>;
  /**
   * Creates an empty-history successor session, replacing this one's
   * conversation without touching its settings — the SDK's `clear()`,
   * distinct from `fork()` (which copies history). This handle keeps
   * existing (it is not mutated); the caller navigates to the new id.
   */
  clearSession(sessionId: string): Promise<{ id: string }>;
  compactSession(sessionId: string): Promise<{ compacted: boolean }>;
  createSession(request: CreateSessionRequest): Promise<{ id: string }>;
  deleteSession(sessionId: string): Promise<void>;
  detail(sessionId: string, signal?: AbortSignal): Promise<SessionDetailResponse>;
  forkSession(sessionId: string, request: ForkSessionRequest): Promise<{ id: string }>;
  listSessions(): Promise<ListSessionsResponse>;
  renameSession(sessionId: string, title: string): Promise<{ title: string }>;
  retry(sessionId: string, signal: AbortSignal): AsyncIterable<RunStreamEvent>;
  resolvePermission(
    sessionId: string,
    runId: string,
    askId: string,
    verdict: PermissionVerdict,
  ): Promise<boolean>;
  run(
    sessionId: string,
    request: StartRunRequest,
    signal: AbortSignal,
  ): AsyncIterable<RunStreamEvent>;
  /** Steers the exact durable run without depending on this BFF process owning its stream. */
  steer(sessionId: string, runId: string, text: string): Promise<boolean>;
  setMode(
    sessionId: string,
    mode: CreateSessionRequest["mode"],
  ): Promise<{ mode: CreateSessionRequest["mode"] }>;
  transcript(sessionId: string, signal?: AbortSignal): Promise<SessionTranscriptResponse>;
}

export function createMecatlChatService(client: Client): ChatService {
  return {
    async *activity(sessionId, request) {
      const { cursor, replayMax, signal } = request;
      const session = await client.sessions.get(sessionId, { signal });
      // A cursor resumes exactly after itself; without one the replay starts at
      // the beginning of durable history and is bounded below.
      const activity = await session.activity({
        from: cursor ?? "start",
        includeLogOnly: true,
        signal,
      });
      let latestRunId = "";
      // A resumed stream's first frame is the next durable event itself: the
      // client already follows that event's run, so no run.started precedes
      // it. A later run still gets its own run.started.
      let resumed = cursor !== undefined;
      let replayed = 0;
      const terminalRuns = new Set<string>();

      try {
        for await (const envelope of activity) {
          if (envelope.kind === "boundary") {
            if (latestRunId && terminalRuns.has(latestRunId)) return;
            // No run was seen in this request — a resume from the last event
            // of a finished run, or a session with no history — so the replay
            // alone cannot tell whether one is live. Ask the session; an idle
            // or finished one has nothing to follow.
            if (!latestRunId && !(await sessionIsActive(session, signal))) return;
            yield {};
            continue;
          }
          // A gap exposes no cursor by contract, so there is nothing to resume
          // from: report it and stop rather than stream across a hole.
          if (envelope.kind === "gap") {
            yield { delivery: { cursor: "", reason: "gap", type: "run.truncated" } };
            return;
          }
          if (envelope.event === undefined) continue;

          const event = envelope.event;
          if (event.runId && event.runId !== latestRunId) {
            latestRunId = event.runId;
            if (!resumed) {
              yield { delivery: { runId: event.runId, sessionId, type: "run.started" } };
            }
          }
          resumed = false;
          yield {
            cursor: envelope.cursor,
            delivery: { event: serializeEvent(event), type: "run.event" },
          };

          // Only REPLAY is bounded; a live turn streams in full. The check
          // follows the delivery so the bound-th event ends the request
          // without reading one more envelope of history.
          if (envelope.phase === "replay") {
            replayed += 1;
            if (replayed >= replayMax) {
              yield {
                delivery: { cursor: envelope.cursor, reason: "bound", type: "run.truncated" },
              };
              return;
            }
          }

          if (event.kind === "result" && event.runId) {
            terminalRuns.add(event.runId);
            if (envelope.phase === "live") return;
          }
        }
      } catch (error) {
        // The daemon may report a durable hole as an error rather than as a
        // gap envelope; either way there is no cursor to resume from.
        if (error instanceof MecatlError && error.code === "activity_gap") {
          yield { delivery: { cursor: "", reason: "gap", type: "run.truncated" } };
          return;
        }
        throw error;
      } finally {
        await activity.close().catch(() => undefined);
      }
    },

    async cancelRun(sessionId, runId) {
      try {
        const session = await client.sessions.get(sessionId);
        await session.controls(runId).cancel();
        return true;
      } catch (error) {
        if (isStaleRunControl(error)) return false;
        throw error;
      }
    },

    async steer(sessionId, runId, text) {
      try {
        const session = await client.sessions.get(sessionId);
        await session.controls(runId).steer(text);
        return true;
      } catch (error) {
        if (isStaleRunControl(error)) return false;
        throw error;
      }
    },

    async clearSession(sessionId) {
      const session = await client.sessions.get(sessionId);
      const cleared = await session.clear();
      return { id: cleared.id };
    },

    async compactSession(sessionId) {
      const session = await client.sessions.get(sessionId);
      return { compacted: await session.compact() };
    },

    async createSession(request) {
      const session = await client.sessions.create({
        mode: toSessionMode(request.mode),
        ...(request.model === undefined
          ? {}
          : { modelId: request.model.id, providerId: request.model.providerId }),
        ...(request.reasoningEffort === "default"
          ? {}
          : { reasoningEffort: request.reasoningEffort }),
        // An AI-debug session is always no-fs (the daemon requires it — its
        // engine has exactly one read-only tool and never touches a
        // filesystem), independent of the ordinary toolAccess choice.
        ...(request.toolAccess === "noFilesystem" || request.debugTargetSessionId
          ? { profile: "no-fs" }
          : {}),
        ...(request.debugTargetSessionId === undefined
          ? {}
          : { debugTargetSessionId: request.debugTargetSessionId }),
      });
      return { id: session.id };
    },

    async deleteSession(sessionId) {
      const session = await client.sessions.get(sessionId);
      await session.delete();
    },

    async detail(sessionId, signal) {
      const session = await client.sessions.get(sessionId, { signal });
      const snapshot = await session.snapshot({ signal });
      const resolvedModel = snapshot.resolvedModel;
      return {
        capabilities: {
          image: snapshot.sessionCapabilities?.image === true,
          manualCompaction: snapshot.capabilities?.manualCompaction === true,
          modelSelection: snapshot.capabilities?.modelSelection === true,
        },
        id: snapshot.sessionId,
        mode: fromSessionMode(snapshot.mode),
        ...(resolvedModel === undefined
          ? {}
          : {
              model: {
                contextWindow: resolvedModel.contextWindow.toString(),
                id: resolvedModel.modelId,
                providerId: resolvedModel.providerId,
                reasoningEffort: toReasoningEffort(resolvedModel.reasoningEffort),
              },
            }),
        state: snapshot.state,
        usage: aggregateUsage(snapshot.tokenUsage),
      };
    },

    async forkSession(sessionId, request) {
      const session = await client.sessions.fork(sessionId, {
        modelId: request.model.id,
        providerId: request.model.providerId,
        ...(request.reasoningEffort === "default"
          ? {}
          : { reasoningEffort: request.reasoningEffort }),
      });
      return { id: session.id };
    },

    async listSessions() {
      const items: SessionSummaryResponse[] = [];
      const seenCursors = new Set<string>();
      let cursor = "";

      for (let page = 0; page < maximumInventoryPages; page += 1) {
        const response = await client.sessions.list({
          $typeName: "mecatl.v1.ListSessionsRequest",
          cursor,
          pageSize: inventoryPageSize,
        });

        for (const session of response.sessions) {
          const publicChatReason = session.capabilities?.reasons?.publicChat ?? "";
          const debugSession = Boolean(session.relationship?.debugTargetSessionId);
          if (!session.sessionId || (publicChatReason === "inspect_only_kind" && !debugSession)) {
            continue;
          }

          items.push({
            capabilities: {
              delete: session.capabilities?.delete === true,
              deleteReason: session.capabilities?.reasons?.delete ?? "",
              rename: session.capabilities?.rename === true,
              renameReason: session.capabilities?.reasons?.rename ?? "",
            },
            createdAt: unixSecondsToIso(session.createdAtUnix),
            debugTargetSessionId: session.relationship?.debugTargetSessionId ?? "",
            id: session.sessionId,
            modelId: session.modelId,
            state: session.state,
            title: session.titleMetadata?.title || session.title || "Untitled chat",
            turns: session.turns,
            updatedAt: unixSecondsToIso(session.modifiedAtUnix),
          });
        }

        const nextCursor = response.nextCursor;
        if (!nextCursor) {
          return { complete: true, items };
        }
        if (seenCursors.has(nextCursor)) {
          return { complete: false, items };
        }
        seenCursors.add(nextCursor);
        cursor = nextCursor;
      }

      return { complete: false, items };
    },

    async renameSession(sessionId, title) {
      const session = await client.sessions.get(sessionId);
      const snapshot = await session.rename(title);
      return { title: snapshot.title?.value || title };
    },

    async *retry(sessionId, signal) {
      const session = await client.sessions.get(sessionId, { signal });
      const run = await session.retry({}, { signal });
      yield* streamRun(sessionId, run);
    },

    async resolvePermission(sessionId, runId, askId, verdict) {
      try {
        const session = await client.sessions.get(sessionId);
        await session.controls(runId).resolveAsk(askId, verdict);
        return true;
      } catch (error) {
        if (isStaleRunControl(error)) return false;
        throw error;
      }
    },

    async *run(sessionId, request, signal) {
      const session = await client.sessions.get(sessionId, { signal });
      const prompt = request.images.length
        ? [
            ...(request.prompt ? [textPart(request.prompt)] : []),
            ...request.images.map((image) =>
              imagePart({
                bytes: Uint8Array.from(Buffer.from(image.data, "base64")),
                mimeType: image.mimeType,
              }),
            ),
          ]
        : request.prompt;
      const run = await session.run(prompt, {}, { signal });
      yield* streamRun(sessionId, run);
    },

    async setMode(sessionId, mode) {
      const session = await client.sessions.get(sessionId);
      const snapshot = await session.setMode(toSessionMode(mode));
      return { mode: fromSessionMode(snapshot.mode) };
    },

    async transcript(sessionId, signal) {
      const session = await client.sessions.get(sessionId, { signal });
      const transcript = await session.transcript({ signal });
      return {
        complete: transcript.complete,
        messages: transcript.messages.map((message) => ({
          images: message.parts.flatMap((part, index) => {
            if (part.kind !== 1) return [];
            const data = part.data.byteLength
              ? Buffer.from(part.data).toString("base64")
              : undefined;
            if (data === undefined && !part.url) return [];
            return [
              {
                ...(data === undefined ? {} : { data }),
                mimeType: part.mimeType,
                name: `Image ${index + 1}`,
                ...(part.url ? { url: part.url } : {}),
              },
            ];
          }),
          role: message.role,
          text: message.text,
          toolCalls: message.toolCalls.map((call) => ({
            args: call.args,
            id: call.id,
            name: call.name,
          })),
          ...(message.toolResult === undefined
            ? {}
            : {
                toolResult: {
                  callId: message.toolResult.callId,
                  content: message.toolResult.content,
                  isError: message.toolResult.isError,
                },
              }),
        })),
        sessionId: transcript.sessionId,
      };
    },
  };
}

async function* streamRun(sessionId: string, run: Run): AsyncIterable<RunStreamEvent> {
  yield { runId: run.id, sessionId, type: "run.started" };

  for await (const event of run) {
    yield { event: serializeEvent(event), type: "run.event" };
  }
}

/**
 * The daemon refused the resume position: the cursor is malformed, expired, or
 * scoped to another filter. Routes answer 400 rather than silently replaying
 * the whole history.
 */
export function isUnusableCursor(error: unknown) {
  return (
    error instanceof MecatlError &&
    ["cursor_expired", "cursor_malformed", "cursor_scope"].includes(error.code)
  );
}

function isStaleRunControl(error: unknown) {
  return (
    error instanceof MecatlError &&
    ["no_active_run", "not_found", "session_not_found", "stale_run_control"].includes(error.code)
  );
}

function toSessionMode(mode: CreateSessionRequest["mode"]) {
  if (mode === "plan") {
    return SessionMode.Plan;
  }
  if (mode === "acceptEdits") {
    return SessionMode.AcceptEdits;
  }
  return SessionMode.Default;
}

function fromSessionMode(mode: SessionMode): CreateSessionRequest["mode"] {
  if (mode === SessionMode.Plan) return "plan";
  if (mode === SessionMode.AcceptEdits) return "acceptEdits";
  return "default";
}

function toReasoningEffort(value: string | undefined): ForkSessionRequest["reasoningEffort"] {
  if (
    value === "low" ||
    value === "medium" ||
    value === "high" ||
    value === "xhigh" ||
    value === "max"
  ) {
    return value;
  }
  return "default";
}

function aggregateUsage(
  tokenUsage: Readonly<
    Record<
      string,
      { readonly models: Readonly<Record<string, EventUsage>>; readonly total?: EventUsage }
    >
  >,
): SessionUsageResponse {
  const total = emptyUsage();
  const main = tokenUsage.main;
  if (main?.total) {
    addUsage(total, main.total);
  } else if (main) {
    for (const usage of Object.values(main.models)) addUsage(total, usage);
  }
  return {
    cacheReadTokens: total.cacheReadTokens.toString(),
    cacheWriteTokens: total.cacheWriteTokens.toString(),
    inputTokens: total.inputTokens.toString(),
    outputTokens: total.outputTokens.toString(),
    reasoningTokens: total.reasoningTokens.toString(),
  };
}

function emptyUsage(): Record<keyof EventUsage, bigint> {
  return {
    cacheReadTokens: 0n,
    cacheWriteTokens: 0n,
    inputTokens: 0n,
    outputTokens: 0n,
    reasoningTokens: 0n,
  };
}

function addUsage(target: Record<keyof EventUsage, bigint>, usage: EventUsage) {
  target.cacheReadTokens += usage.cacheReadTokens;
  target.cacheWriteTokens += usage.cacheWriteTokens;
  target.inputTokens += usage.inputTokens;
  target.outputTokens += usage.outputTokens;
  target.reasoningTokens += usage.reasoningTokens;
}

function unixSecondsToIso(seconds: bigint): string {
  if (seconds <= 0n) {
    return "";
  }
  return new Date(Number(seconds) * 1_000).toISOString();
}

export function serializeEvent(
  event: Event,
): Extract<RunStreamEvent, { type: "run.event" }>["event"] {
  const usage =
    event.usage === undefined
      ? undefined
      : {
          cacheReadTokens: event.usage.cacheReadTokens.toString(),
          cacheWriteTokens: event.usage.cacheWriteTokens.toString(),
          inputTokens: event.usage.inputTokens.toString(),
          outputTokens: event.usage.outputTokens.toString(),
          reasoningTokens: event.usage.reasoningTokens.toString(),
        };

  if (event.kind === "unknown") {
    return {
      kind: event.wireKind,
      raw: jsonSafe(event.rawData),
      runId: event.runId,
      seq: event.seq.toString(),
      text: event.text,
      turn: event.turn,
      unknown: true,
      ...(usage === undefined ? {} : { usage }),
    };
  }

  return {
    kind: event.kind,
    ...(event.payload === undefined ? {} : { payload: jsonSafe(event.payload) }),
    runId: event.runId,
    seq: event.seq.toString(),
    text: event.text,
    turn: event.turn,
    unknown: false,
    ...(usage === undefined ? {} : { usage }),
  };
}

type JsonSafe = null | boolean | number | string | JsonSafe[] | { [key: string]: JsonSafe };

function jsonSafe(value: unknown): JsonSafe {
  if (value === null || typeof value === "string" || typeof value === "boolean") {
    return value;
  }
  if (typeof value === "number") {
    return Number.isFinite(value) ? value : String(value);
  }
  if (typeof value === "bigint") {
    return value.toString();
  }
  if (value instanceof Uint8Array) {
    return {
      data: Buffer.from(value).toString("base64"),
      encoding: "base64",
    };
  }
  if (Array.isArray(value)) {
    return value.map(jsonSafe);
  }
  if (typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value).flatMap(([key, entry]) =>
        entry === undefined ? [] : [[key, jsonSafe(entry)]],
      ),
    );
  }
  return String(value);
}

/**
 * Whether the session is driving or parked on a run. An unreadable state
 * counts as active, so the stream keeps waiting rather than closing early.
 */
async function sessionIsActive(
  session: { snapshot?: (options?: { signal?: AbortSignal }) => Promise<{ state: string }> },
  signal: AbortSignal,
): Promise<boolean> {
  try {
    if (typeof session.snapshot !== "function") return true;
    const { state } = await session.snapshot({ signal });
    return state === "running" || state === "awaiting" || state === "authorizing";
  } catch {
    return true;
  }
}

/**
 * The `run.error` frame for a failed stream. Its message reaches the browser,
 * so it gets the same treatment as a problem-details `detail`: bounded, with
 * URLs and bearer-shaped material redacted.
 */
export function runError(error: unknown): Extract<RunStreamEvent, { type: "run.error" }> {
  return {
    code: error instanceof MecatlError ? error.code : "run_failed",
    message:
      error instanceof Error && error.message !== ""
        ? sanitizeUpstreamDetail(error.message)
        : "Mecatl could not complete the run.",
    type: "run.error",
  };
}
