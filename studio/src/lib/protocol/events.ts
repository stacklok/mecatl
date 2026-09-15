/**
 * The daemon event seam: translation of one SDK-decoded `Event` (the mecatl
 * TypeScript SDK owns the wire — SSE framing, protojson decode, run/seq
 * bookkeeping) into the UI's StreamEvent union. This module is the ONLY place
 * that reads SDK event payloads; everything above it speaks StreamEvent.
 */

import type {
  EventContent,
  EventUsage,
  ParallelEventPayload,
  Event as SdkEvent,
  SubagentEventPayload,
  TeamMemberSpecEventPayload,
} from "@stacklok-oss/mecatl-sdk";
import type {
  RetryDisposition,
  SteerEchoPart,
  StreamEvent,
  StreamProgress,
} from "@/features/agent/types";
import { fileFromToolCall } from "@/lib/file-meta";

/** Renders a tool's JSON args as a compact `key: value · key: value` line. */
function prettyArgs(raw?: string): string {
  if (!raw) return "";
  try {
    const parsed = JSON.parse(raw) as Record<string, unknown>;
    return Object.entries(parsed)
      .map(
        ([key, value]) =>
          `${key}: ${typeof value === "string" ? value : JSON.stringify(value)}`,
      )
      .join(" · ");
  } catch {
    return raw;
  }
}

/** SDK token counts are bigint; the UI sums plain numbers. */
const tokens = (value: bigint | number | undefined): number => {
  if (value === undefined) return 0;
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
};

/** Standard base64 of inline bytes, browser-safe (no Buffer). */
function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  const chunk = 0x8000;
  for (let offset = 0; offset < bytes.length; offset += chunk) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + chunk));
  }
  return btoa(binary);
}

// The ADR-0239 failed-terminal enums. The Go mapper always stamps both on new
// servers (UNKNOWN=1 is a real value, distinct from ABSENT = old server), so
// the decode is presence-aware and an unrecognized value degrades to
// "unknown" — never to "retryable".
const RETRY_DISPOSITION_LABELS: Record<number, RetryDisposition> = {
  1: "unknown",
  2: "retryable",
  3: "permanent",
};
const STREAM_PROGRESS_LABELS: Record<number, StreamProgress> = {
  1: "unknown",
  2: "precommit",
  3: "visible",
  4: "complete",
};

const retryDispositionLabel = (
  value: number | undefined,
): RetryDisposition | undefined =>
  value === undefined
    ? undefined
    : (RETRY_DISPOSITION_LABELS[value] ?? "unknown");

const streamProgressLabel = (
  value: number | undefined,
): StreamProgress | undefined =>
  value === undefined
    ? undefined
    : (STREAM_PROGRESS_LABELS[value] ?? "unknown");

// Steer-echo media parts (proto Content). KIND_UNSPECIFIED and unknown kinds
// are dropped — a part Studio cannot classify cannot be rendered either.
const CONTENT_KIND_LABELS: Record<number, SteerEchoPart["kind"]> = {
  1: "image",
  2: "audio",
};

function decodeSteerParts(
  parts: readonly EventContent[] | undefined,
): SteerEchoPart[] | undefined {
  if (!parts?.length) return undefined;
  const decoded: SteerEchoPart[] = [];
  for (const part of parts) {
    const kind = CONTENT_KIND_LABELS[part.kind];
    if (!kind) continue;
    decoded.push({
      kind,
      mimeType: part.mimeType ?? "",
      data: part.data?.length ? bytesToBase64(part.data) : undefined,
      url: part.url || undefined,
    });
  }
  return decoded.length > 0 ? decoded : undefined;
}

const routingDetail = (
  source: Pick<
    SubagentEventPayload | ParallelEventPayload | TeamMemberSpecEventPayload,
    "routedCategory" | "routedModel" | "model"
  >,
) =>
  [source.routedCategory, source.routedModel || source.model]
    .filter(Boolean)
    .join(" → ");

const usageTokens = (usage: EventUsage | undefined) =>
  usage
    ? {
        inputTokens: tokens(usage.inputTokens),
        outputTokens: tokens(usage.outputTokens),
      }
    : { inputTokens: undefined, outputTokens: undefined };

/**
 * Event kinds that deliberately have no visual surface in Studio: run
 * lifecycle markers, redacted child-activity detail beyond the start badge,
 * and kinds that only ever appear in durable-log replays. The other two
 * log-only kinds (`user_prompt`, `approval`) translate below — the durable
 * watch (ADR 0250) replays them and they must render, not vanish.
 */
const SILENT_EVENT_KINDS = new Set([
  "session.init",
  "turn.start",
  "turn.end",
  "hook",
  // The pre-compaction conversation archive: audit history for the durable
  // log, deliberately not re-rendered into the live transcript.
  "compaction.archive",
  "team.member",
  "team.tasks",
  "team.findings",
  "team.end",
  "parallel.start",
  "parallel.end",
  "schedule.fired",
  "schedule.skipped",
  "schedule.failed",
  // The provider/model a prompt was routed to is an implementation detail,
  // not something the operator asked to see under every turn.
  "provider.route",
]);

/** Advisory kinds whose `text` is worth a one-line notice in the flow. */
const ADVISORY_EVENT_KINDS = new Set([
  "tool.progress",
  "compaction",
  "no_progress",
  "recover_notice",
]);

const unrenderedNotice = (kind: string): StreamEvent => ({
  type: "notice",
  text: `Mecatl sent an event this Studio version does not render yet: ${kind}`,
});

/**
 * Translates one SDK-decoded event into zero or more StreamEvents.
 *
 * Unknown event kinds become a visible notice, never a silent drop — a new
 * daemon capability must show up as "not rendered yet", not vanish.
 *
 * Every translated event is stamped with the event's `runId` (when the
 * daemon sent one), so consumers can capture the active run's identity from
 * the first run-bearing event and scope controls to it (ADR 0249).
 */
export function translateEvent(
  event: SdkEvent,
  sessionId: string,
): StreamEvent[] {
  const translated = translateEventBody(event, sessionId);
  if (event.runId) {
    for (const item of translated) item.runId = event.runId;
  }
  return translated;
}

function translateEventBody(event: SdkEvent, sessionId: string): StreamEvent[] {
  switch (event.kind) {
    case "message.delta":
      return event.text ? [{ type: "token", text: event.text }] : [];
    case "reasoning.delta":
      return event.text ? [{ type: "reasoning", text: event.text }] : [];
    case "tool.call": {
      const call = event.payload;
      const name = call.name || "Tool";
      return [
        {
          type: "tool_call",
          callId: call.id || `call-${event.seq}`,
          name,
          input: call.args ? prettyArgs(call.args) : "",
          file: fileFromToolCall(call.name, call.args || undefined),
        },
      ];
    }
    case "tool.result": {
      const result = event.payload;
      return [
        {
          type: "tool_result",
          callId: result.callId,
          output: result.content,
          isError: result.isError,
        },
      ];
    }
    case "permission.ask": {
      const ask = event.payload;
      return [
        {
          type: "approval",
          approvalId: ask.askId,
          sessionId,
          toolName: ask.tool,
          description: `${ask.tool || "A tool"} needs your approval.`,
          details: [ask.reason, prettyArgs(ask.args)]
            .filter(Boolean)
            .join("\n\n"),
        },
      ];
    }
    case "permission.retract":
      return event.payload.askId
        ? [{ type: "retract", approvalId: event.payload.askId }]
        : [];
    case "steer":
      // The daemon drained the pending steer bundle into the run. The echo
      // carries the merged text, the watermark id of the last message it
      // absorbed — the hook splits its pending list on that id — and the
      // committed media bundle (ADR 0251).
      return [
        {
          type: "steer",
          text: event.payload.text,
          messageId: event.payload.messageId,
          parts: decodeSteerParts(event.payload.parts),
        },
      ];
    case "model.retry":
      // The daemon is re-driving the failed step (ADR 0239): a quiet system
      // line, mirroring the other advisory kinds.
      return [{ type: "notice", text: "Retrying the failed step…" }];
    case "subagent.start": {
      const subagent = event.payload;
      return [
        {
          type: "delegation",
          kind: "subagent",
          label: subagent.goal || "subagent",
          detail: routingDetail(subagent),
          childId: subagent.childId || undefined,
          background: subagent.background || undefined,
          routingReason: subagent.routingReason || undefined,
        },
      ];
    }
    case "subagent.tool": {
      // Live child activity (D1): cumulative counters for the delegation
      // card. Only redacted metadata crosses — never child content.
      const subagent = event.payload;
      if (!subagent.childId) return [];
      return [
        {
          type: "delegation_progress",
          childId: subagent.childId,
          toolCount: subagent.toolCount,
          ...usageTokens(subagent.usage),
          toolName: subagent.toolName || undefined,
        },
      ];
    }
    case "subagent.end": {
      // Child terminal (D1): the stop reason, duration, final counters, and
      // the failure cause — a failed child must render, never vanish.
      const subagent = event.payload;
      if (!subagent.childId) return [];
      return [
        {
          type: "delegation_end",
          childId: subagent.childId,
          stop: subagent.stop,
          toolCount: subagent.toolCount,
          ...usageTokens(subagent.usage),
          durationMs: tokens(subagent.durationMs),
          cause: subagent.cause || undefined,
        },
      ];
    }
    case "team.start":
      return event.payload.roster.map((member) => ({
        type: "delegation" as const,
        kind: "team" as const,
        label: [member.name, member.role && `(${member.role})`]
          .filter(Boolean)
          .join(" "),
        detail: routingDetail(member),
      }));
    case "parallel.branch": {
      const parallel = event.payload;
      if (parallel.kind !== "branch_start") return [];
      return [
        {
          type: "delegation",
          kind: "parallel",
          label: parallel.branchLabel || `branch ${parallel.branchIndex + 1}`,
          detail: routingDetail(parallel),
        },
      ];
    }
    case "user_prompt":
      // The durable log's record of what the user asked (EvUserPrompt).
      // Only seen on watch/replay streams — the live prompt path never
      // carries it. Empty text (a media-only prompt) stays quiet.
      return event.payload.text
        ? [{ type: "user_prompt", text: event.payload.text }]
        : [];
    case "approval": {
      // The verdict half of a permission ask (EvApproval), from the durable
      // log. Metadata only: the tool's NAME and the verdict string.
      const approval = event.payload;
      return [
        {
          type: "approval_verdict",
          approvalId: approval.askId,
          toolName: approval.tool,
          verdict: approval.verdict,
        },
      ];
    }
    case "result": {
      const result = event.payload;
      const events: StreamEvent[] = [];
      const usage = result.usage;
      if (usage) {
        events.push({
          type: "usage",
          inputTokens: tokens(usage.inputTokens),
          outputTokens: tokens(usage.outputTokens),
          cacheReadTokens: tokens(usage.cacheReadTokens),
          cacheWriteTokens: tokens(usage.cacheWriteTokens),
          reasoningTokens: tokens(usage.reasoningTokens),
          estimatedCost: null,
        });
      }
      // A well-formed result with stop === "error" is a FAILED turn and must
      // render as one — the terminal frame always reaches the hook, even when
      // it carries no usage and no text.
      events.push({
        type: "run_result",
        stop: result.stop,
        text: result.text,
        errorText: result.error,
        permanent: result.permanent === true,
        retryDisposition: retryDispositionLabel(result.retryDisposition),
        streamProgress: streamProgressLabel(result.streamProgress),
      });
      return events;
    }
    case "unknown":
      return [unrenderedNotice(event.wireKind)];
    default:
      if (SILENT_EVENT_KINDS.has(event.kind)) return [];
      if (ADVISORY_EVENT_KINDS.has(event.kind)) {
        return event.text ? [{ type: "notice", text: event.text }] : [];
      }
      return [unrenderedNotice(event.kind)];
  }
}
