// SPDX-License-Identifier: Apache-2.0

import type {
  RunStreamEvent,
  SessionTranscriptResponse,
  SessionUsageResponse,
} from "@mecatl-studio/contracts";
import type { ApprovalRequest } from "./approval-panel";
import type { AuthorizationHandoff } from "./authorization-review";
import type { ChatImage } from "./local-file-preview";
import type { ToolActivity } from "./tool-activity";
import { formatTurnStat } from "./turn-stats";

export interface RunFailure {
  message: string;
  permanent: boolean;
  prompt: string;
}

export interface ChatMessage {
  authorizations?: AuthorizationHandoff[];
  content: string;
  delivery?: SessionTranscriptResponse["messages"][number]["delivery"];
  failure?: { detail: string; message: string; permanent: boolean };
  id: string;
  images?: ChatImage[];
  /** Zero-based index in an authoritative transcript, including non-rendered tool results. */
  recordedOrdinal?: number;
  reasoning?: string;
  role: string;
  stopReason?: string;
  tools?: ToolActivity[];
  turnStat?: string;
}

export function enqueueApproval(
  approvals: ApprovalRequest[],
  approval: ApprovalRequest,
): ApprovalRequest[] {
  if (!approval.askId) return approvals;
  if (approvals.some((candidate) => candidate.askId === approval.askId)) return approvals;
  return [...approvals, approval];
}

export function retractApproval(approvals: ApprovalRequest[], askId: string): ApprovalRequest[] {
  if (!approvals.some((approval) => approval.askId === askId)) return approvals;
  return approvals.filter((approval) => approval.askId !== askId);
}

export function failureFromResult(payload: unknown, prompt: string): RunFailure | undefined {
  if (!isRecord(payload)) return undefined;

  const error = stringValue(payload.error);
  const stop = stringValue(payload.stop).toLowerCase();
  if (!error && stop !== "error" && stop !== "failed" && stop !== "failure") {
    return undefined;
  }

  return {
    message: error || "The agent could not complete this request.",
    permanent: payload.permanent === true,
    prompt,
  };
}

export function permissionAskId(payload: unknown): string {
  return isRecord(payload) ? stringValue(payload.askId) : "";
}

/** The result event's raw `stop` reason string (e.g. "budget", "cancelled", "end_turn"), or "". */
export function stopReasonFromResult(payload: unknown): string {
  return isRecord(payload) ? stringValue(payload.stop) : "";
}

/** The result event's payload, stringified for a failed-turn's "Show details" view. */
export function rawResultPayload(payload: unknown): string {
  try {
    return JSON.stringify(payload, null, 2);
  } catch {
    return "";
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

export function messagesFromTranscript(
  entries: Array<{
    delivery?: SessionTranscriptResponse["messages"][number]["delivery"];
    images?: ChatImage[];
    role: string;
    text: string;
    toolCalls: Array<{ args: string; id: string; name: string }>;
    toolResult?: { callId: string; content: string; isError: boolean };
  }>,
): ChatMessage[] {
  const messages: ChatMessage[] = [];

  for (const [index, entry] of entries.entries()) {
    if (entry.toolResult) {
      const result = entry.toolResult;
      for (let messageIndex = messages.length - 1; messageIndex >= 0; messageIndex -= 1) {
        const message = messages[messageIndex];
        if (!message?.tools?.some((tool) => tool.id === result.callId)) {
          continue;
        }
        messages[messageIndex] = {
          ...message,
          tools: message.tools.map((tool) =>
            tool.id === result.callId
              ? {
                  ...tool,
                  isError: result.isError,
                  output: result.content,
                }
              : tool,
          ),
        };
        break;
      }
      continue;
    }

    messages.push({
      content: entry.text,
      delivery: entry.delivery,
      id: `transcript-${index}`,
      recordedOrdinal: index,
      images: entry.images?.length
        ? entry.images.map((image, imageIndex) => ({
            ...image,
            id: `transcript-${index}-image-${imageIndex}`,
          }))
        : undefined,
      role: entry.role,
      tools: entry.toolCalls.length ? entry.toolCalls.map((tool) => ({ ...tool })) : undefined,
    });
  }
  return messages;
}

export function payloadText(payload: unknown): string {
  if (typeof payload !== "object" || payload === null || !("text" in payload)) {
    return "";
  }
  return typeof payload.text === "string" ? payload.text : "";
}

export function payloadImages(payload: unknown): ChatImage[] {
  const value = payloadRecord(payload);
  if (!Array.isArray(value?.parts)) return [];

  return value.parts.flatMap((part, index) => {
    const record = payloadRecord(part);
    if (record?.kind !== 1 || typeof record.mimeType !== "string") return [];
    const encoded = payloadRecord(record.data);
    const data =
      encoded?.encoding === "base64" && typeof encoded.data === "string" ? encoded.data : undefined;
    const url = typeof record.url === "string" && record.url ? record.url : undefined;
    if (!data && !url) return [];
    return [
      {
        ...(data ? { data } : {}),
        id: `replay-image-${index}`,
        mimeType: record.mimeType,
        name: `Image ${index + 1}`,
        ...(url ? { url } : {}),
      },
    ];
  });
}

export function payloadRecord(payload: unknown): Record<string, unknown> | undefined {
  return typeof payload === "object" && payload !== null
    ? (payload as Record<string, unknown>)
    : undefined;
}

export function toolCall(payload: unknown): ToolActivity | undefined {
  const value = payloadRecord(payload);
  if (!value) {
    return undefined;
  }
  const id = typeof value.id === "string" ? value.id : "";
  const name = typeof value.name === "string" ? value.name : "Tool";
  const args = typeof value.args === "string" ? value.args : "";
  return id ? { args, id, name } : undefined;
}

export function toolResult(payload: unknown) {
  const value = payloadRecord(payload);
  if (!value || typeof value.callId !== "string") {
    return undefined;
  }
  return {
    callId: value.callId,
    content: typeof value.content === "string" ? value.content : "",
    isError: value.isError === true,
  };
}

export function permissionAsk(payload: unknown): ApprovalRequest | undefined {
  const value = payloadRecord(payload);
  if (!value || typeof value.askId !== "string") {
    return undefined;
  }
  return {
    args: typeof value.args === "string" ? value.args : "",
    askId: value.askId,
    reason: typeof value.reason === "string" ? value.reason : "",
    tool: typeof value.tool === "string" ? value.tool : "Tool",
  };
}

export function errorMessage(error: unknown): string {
  if (typeof error === "object" && error !== null && "detail" in error) {
    return String(error.detail);
  }
  return error instanceof Error ? error.message : "The request could not be completed.";
}

/**
 * The accumulating state one run's SSE deliveries fold into — the pure half
 * of `consumeRun`-shaped stream handling, factored out so both the main chat
 * pane and the side-thread panel can drive independent streams through the
 * same tested transform instead of duplicating the delivery switch.
 */
export interface RunDeliveryState {
  activeAssistantId: string;
  activePrompt: string;
  approvals: ApprovalRequest[];
  failure?: RunFailure;
  liveUsage?: SessionUsageResponse;
  messages: ChatMessage[];
  runId?: string;
  sawResult: boolean;
  stopReason?: string;
  turnStartedAt: number;
}

export function initialRunDeliveryState(
  assistantId: string,
  prompt: string,
  messages: ChatMessage[] = [],
): RunDeliveryState {
  return {
    activeAssistantId: assistantId,
    activePrompt: prompt,
    approvals: [],
    messages,
    sawResult: false,
    turnStartedAt: Date.now(),
  };
}

/**
 * True when a `run.started` for `startedRunId` begins a run other than the one
 * already being followed. A stream resumed from a cursor carries no
 * `run.started` for the cursor's own run, and a repeated one for the followed
 * run is a no-op: neither may reset the active assistant message, its prompt,
 * or the turn's progress. A later run in the same stream still starts afresh.
 */
export function startsNewRun(followedRunId: string | undefined, startedRunId: string): boolean {
  return followedRunId !== startedRunId;
}

/** Folds one `RunStreamEvent` delivery into the running state. Pure — `now` and any new ids are supplied by the caller so this stays testable without faking `Date`/`crypto`. */
export function applyRunDelivery(
  state: RunDeliveryState,
  delivery: RunStreamEvent,
  options: { newId: () => string; now: number; replay: boolean },
): RunDeliveryState {
  if (delivery.type === "run.error") {
    return {
      ...state,
      failure: { message: delivery.message, permanent: false, prompt: state.activePrompt },
    };
  }
  if (delivery.type === "run.started") {
    if (!startsNewRun(state.runId, delivery.runId)) return state;
    return {
      ...state,
      activeAssistantId: options.replay ? options.newId() : state.activeAssistantId,
      activePrompt: options.replay ? "" : state.activePrompt,
      failure: undefined,
      runId: delivery.runId,
      sawResult: false,
      stopReason: undefined,
      turnStartedAt: options.now,
    };
  }

  // A truncation frame is a stream boundary, not transcript content: it leaves
  // the reducer's state untouched. The workspace reacts to it by loading the
  // authoritative transcript.
  if (delivery.type === "run.truncated") return state;

  const event = delivery.event;
  let next = state;
  if (event.runId && startsNewRun(state.runId, event.runId)) {
    next = {
      ...next,
      activeAssistantId: state.runId && options.replay ? options.newId() : next.activeAssistantId,
      activePrompt: state.runId && options.replay ? "" : next.activePrompt,
      failure: undefined,
      runId: event.runId,
      sawResult: false,
      stopReason: undefined,
      turnStartedAt: options.now,
    };
  }
  if (event.usage && !options.replay) next = { ...next, liveUsage: event.usage };

  if (event.kind === "user_prompt" && options.replay) {
    const activePrompt = payloadText(event.payload) || event.text;
    const images = payloadImages(event.payload);
    return {
      ...next,
      activeAssistantId: options.newId(),
      activePrompt,
      messages: [
        ...next.messages,
        {
          content: activePrompt,
          id: options.newId(),
          images: images.length ? images : undefined,
          role: "user",
        },
      ],
    };
  }
  if (event.kind === "message.delta") {
    return {
      ...next,
      messages: updateOrAppendMessage(next.messages, next.activeAssistantId, (message) => ({
        ...message,
        content: message.content + event.text,
      })),
    };
  }
  if (event.kind === "reasoning.delta") {
    return {
      ...next,
      messages: updateOrAppendMessage(next.messages, next.activeAssistantId, (message) => ({
        ...message,
        reasoning: (message.reasoning ?? "") + event.text,
      })),
    };
  }
  if (event.kind === "tool.call") {
    const tool = toolCall(event.payload);
    if (!tool) return next;
    return {
      ...next,
      messages: updateOrAppendMessage(next.messages, next.activeAssistantId, (message) => ({
        ...message,
        tools: [...(message.tools ?? []), { ...tool, runId: event.runId || undefined }],
      })),
    };
  }
  if (event.kind === "tool.result") {
    const result = toolResult(event.payload);
    if (!result) return next;
    return {
      ...next,
      messages: next.messages.map((message) =>
        message.id === next.activeAssistantId
          ? {
              ...message,
              tools: message.tools?.map((tool) =>
                tool.id === result.callId
                  ? { ...tool, isError: result.isError, output: result.content }
                  : tool,
              ),
            }
          : message,
      ),
    };
  }
  if (event.kind === "permission.ask") {
    const approval = permissionAsk(event.payload);
    return approval ? { ...next, approvals: enqueueApproval(next.approvals, approval) } : next;
  }
  if (event.kind === "permission.retract" || event.kind === "approval") {
    return { ...next, approvals: retractApproval(next.approvals, permissionAskId(event.payload)) };
  }
  if (event.kind === "result") {
    const failure = failureFromResult(event.payload, next.activePrompt);
    const resultText = payloadText(event.payload) || event.text;
    const stop = stopReasonFromResult(event.payload);
    const turnStat =
      !options.replay && event.usage
        ? formatTurnStat(event.usage, options.now - next.turnStartedAt)
        : undefined;
    return {
      ...next,
      failure,
      messages: updateOrAppendMessage(next.messages, next.activeAssistantId, (message) => ({
        ...message,
        content: message.content || resultText,
        failure: failure
          ? {
              detail: rawResultPayload(event.payload),
              message: failure.message,
              permanent: failure.permanent,
            }
          : message.failure,
        stopReason: stop,
        turnStat: turnStat ?? message.turnStat,
      })),
      sawResult: true,
      stopReason: stop,
    };
  }
  return next;
}

function updateOrAppendMessage(
  messages: ChatMessage[],
  id: string,
  update: (message: ChatMessage) => ChatMessage,
): ChatMessage[] {
  const withPlaceholder = messages.some((message) => message.id === id)
    ? messages
    : [...messages, { content: "", id, role: "assistant" }];
  return withPlaceholder.map((message) => (message.id === id ? update(message) : message));
}
