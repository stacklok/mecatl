/**
 * The daemon event seam: translation of one SDK-decoded `Event` (the mecatl
 * TypeScript SDK owns the wire — SSE framing, protojson decode, run/seq
 * bookkeeping) into the UI's StreamEvent union. This module is the ONLY place
 * that reads SDK event payloads; everything above it speaks StreamEvent.
 */

import type {
  EventContent,
  EventContentBlock,
  EventUsage,
  ParallelEventPayload,
  Event as SdkEvent,
  SubagentEventPayload,
  TeamEventPayload,
  TeamFindingEventPayload,
  TeamMemberDispositionEventPayload,
  TeamMemberSpecEventPayload,
  TeamTaskEventPayload,
} from "@stacklok-oss/mecatl-sdk";
import type {
  DelegationStopReason,
  HookDecision,
  RetryDisposition,
  SteerEchoPart,
  StreamEvent,
  StreamProgress,
  TeamFindingInfo,
  TeamMemberDispositionInfo,
  TeamTaskInfo,
  ToolResultPart,
} from "@/features/agent/types";
import { fileFromToolCall } from "@/lib/file-meta";
import { changedFileFromToolCall } from "@/lib/tool-summary";

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

// Tool-result content blocks (proto ContentBlock.Kind): IMAGE=2 and
// RESOURCE_LINK=4 render as artifact parts on the card; TEXT=1 is already
// folded into `content`, and every other kind (audio, embedded resource,
// unspecified) is not rendered.
const BLOCK_KIND_IMAGE = 2;
const BLOCK_KIND_RESOURCE_LINK = 4;

/**
 * Decodes a tool result's non-text blocks into the card's `parts`: an image
 * block's bytes as base64 (the renderer builds the data: URL, gated on a
 * raster MIME type) and a resource link's url/name/title. Shared by the live
 * `tool.result` translation and the transcript projection so history and
 * the stream agree. Undefined when nothing renderable is carried.
 */
export function decodeResultParts(
  blocks: readonly EventContentBlock[] | undefined,
): ToolResultPart[] | undefined {
  if (!blocks?.length) return undefined;
  const parts: ToolResultPart[] = [];
  for (const block of blocks) {
    if (block.kind === BLOCK_KIND_IMAGE) {
      if (!block.data?.length) continue;
      parts.push({
        kind: "image",
        mimeType: block.mimeType ?? "",
        data: bytesToBase64(block.data),
      });
    } else if (block.kind === BLOCK_KIND_RESOURCE_LINK) {
      if (!block.url) continue;
      parts.push({
        kind: "resource_link",
        url: block.url,
        name: block.name ?? "",
        title: block.title || undefined,
      });
    }
  }
  return parts.length > 0 ? parts : undefined;
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
 * A protobuf Timestamp (bigint seconds + nanos) as epoch milliseconds. The
 * zero timestamp is protobuf's "unset" and reads as absent, never as 1970.
 */
const timestampMs = (
  value: { seconds: bigint; nanos: number } | undefined,
): number | undefined => {
  if (!value) return undefined;
  const ms = Number(value.seconds) * 1000 + Math.floor(value.nanos / 1e6);
  return Number.isFinite(ms) && ms > 0 ? ms : undefined;
};

// proto TEAM_MEMBER_STOP_REASON_*: why the supervisor benched a member.
// UNSPECIFIED (0) and any unrecognized value read as "" — not stopped for a
// named reason — never as an invented one.
const TEAM_STOP_REASON: Record<number, DelegationStopReason> = {
  1: "error",
  2: "cancelled",
  3: "budget",
};

/**
 * The bounded child-activity preview fields shared by subagent.tool, a
 * parallel branch_tool, and team.member. `detail`/`text` are daemon-clamped,
 * model-influenced previews: consumers render them as plain text only.
 */
const activityPreview = (
  source: Pick<
    SubagentEventPayload | ParallelEventPayload | TeamEventPayload,
    "innerKind" | "isError" | "detail" | "text" | "toolName"
  >,
) => ({
  toolName: source.toolName || undefined,
  innerKind: source.innerKind || undefined,
  isError: source.isError || undefined,
  detail: source.detail || undefined,
  text: source.text || undefined,
});

const teamTasks = (
  tasks: readonly TeamTaskEventPayload[] | undefined,
): TeamTaskInfo[] =>
  (tasks ?? []).map((task) => ({
    id: task.id,
    state: task.state,
    assignee: task.assignee,
    deps: [...task.deps],
    description: task.description,
  }));

const teamFindings = (
  findings: readonly TeamFindingEventPayload[] | undefined,
): TeamFindingInfo[] =>
  (findings ?? []).map((finding) => ({
    member: finding.member,
    body: finding.body,
  }));

const teamDispositions = (
  dispositions: readonly TeamMemberDispositionEventPayload[] | undefined,
): TeamMemberDispositionInfo[] =>
  (dispositions ?? []).map((disposition) => ({
    name: disposition.name,
    stopped: disposition.stopped,
    errorRounds: disposition.errorRounds,
    reason: TEAM_STOP_REASON[disposition.reason] ?? "",
  }));

/**
 * Event kinds that deliberately have no visual surface in Studio: run
 * lifecycle markers, schedule lifecycle, and kinds that only ever appear in
 * durable-log replays. The other two log-only kinds (`user_prompt`,
 * `approval`) translate below — the durable watch (ADR 0250) replays them
 * and they must render, not vanish. Every delegation kind (subagent.*,
 * parallel.*, team.*) translates below too — the cards and the Agents panel
 * depend on all of them.
 */
const SILENT_EVENT_KINDS = new Set([
  "session.init",
  "turn.start",
  // The pre-compaction conversation archive: audit history for the durable
  // log, deliberately not re-rendered into the live transcript.
  "compaction.archive",
  "schedule.fired",
  "schedule.skipped",
  "schedule.failed",
]);

/** Advisory kinds whose `text` is worth a DURABLE one-line notice in the flow. */
const ADVISORY_EVENT_KINDS = new Set(["tool.progress", "compaction"]);

/**
 * Advisory kinds that are TRANSIENT status, not transcript: the no-progress
 * nudge and the pre-flight recover notice. They render on the status line
 * under the transcript until the next run and are never appended to a
 * message's notices — a durable-log replay must not resurrect them.
 */
const TRANSIENT_STATUS_KINDS = new Set(["no_progress", "recover_notice"]);

const unrenderedNotice = (kind: string): StreamEvent => ({
  type: "notice",
  text: `Mecatl sent an event this Studio version does not render yet: ${kind}`,
});

/**
 * The wire's HookDecision enum, labelled. UNSPECIFIED (0) and any value the
 * SDK has not named fall back to `info` — the daemon's own boundary default.
 */
const HOOK_DECISION: Record<number, HookDecision> = {
  1: "info",
  2: "blocked",
  3: "modified",
  4: "advisory",
};

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
          // The verbatim JSON rides alongside the flattened preview so the
          // drill-down panel can pretty-print it during a live run.
          rawArgs: call.args || undefined,
          file: fileFromToolCall(call.name, call.args || undefined),
          changedPath: changedFileFromToolCall(
            call.name,
            call.args || undefined,
          ),
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
          parts: decodeResultParts(result.blocks),
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
          // The raw tier, verbatim: the card decodes `args` per tool (Shell
          // command text, Edit diff) and offers the exact string as the
          // "Raw" view — what is actually being approved.
          reason: ask.reason,
          args: ask.args,
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
          parentCallId: subagent.parentCallId || undefined,
          model: subagent.model || undefined,
          background: subagent.background || undefined,
          routingReason: subagent.routingReason || undefined,
        },
      ];
    }
    case "subagent.tool": {
      // Live child activity (D1): cumulative counters plus the bounded
      // activity preview (inner kind, tool name, clamped arg/result/message
      // preview) for the card's trace. Redacted metadata — never child
      // content beyond the daemon-clamped preview.
      const subagent = event.payload;
      if (!subagent.childId) return [];
      return [
        {
          type: "delegation_progress",
          childId: subagent.childId,
          parentCallId: subagent.parentCallId || undefined,
          toolCount: subagent.toolCount,
          ...usageTokens(subagent.usage),
          ...activityPreview(subagent),
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
          parentCallId: subagent.parentCallId || undefined,
          stop: subagent.stop,
          toolCount: subagent.toolCount,
          ...usageTokens(subagent.usage),
          durationMs: tokens(subagent.durationMs),
          cause: subagent.cause || undefined,
        },
      ];
    }
    case "team.start": {
      // One card per roster member, LEAD FIRST (a stable sort keeps the
      // daemon's order among peers), carrying the handles the later
      // team.member / team.end frames key on (team id, call id, member name).
      const team = event.payload;
      const roster = [...team.roster].sort(
        (a, b) => Number(b.lead) - Number(a.lead),
      );
      return roster.map((member) => ({
        type: "delegation" as const,
        kind: "team" as const,
        label: [member.name, member.role && `(${member.role})`]
          .filter(Boolean)
          .join(" "),
        detail: routingDetail(member),
        teamId: team.teamId || undefined,
        parentCallId: team.parentCallId || undefined,
        memberName: member.name,
        lead: member.lead || undefined,
        mutating: member.mutating || undefined,
        routingReason: member.routingReason || undefined,
        model: member.model || undefined,
      }));
    }
    case "parallel.start": {
      // The fan-out group header: join strategy + branch tally.
      const parallel = event.payload;
      return [
        {
          type: "parallel_start",
          parentCallId: parallel.parentCallId,
          join: parallel.join,
          branchCount: parallel.branchCount,
        },
      ];
    }
    case "parallel.branch": {
      // Per-branch lifecycle, discriminated by `kind`. A branch_tool carries
      // NO child id (engine/session/event.go ParallelPayload), so progress is
      // keyed by (parentCallId, branchIndex); start/end carry the child id.
      const parallel = event.payload;
      switch (parallel.kind) {
        case "branch_start":
          return [
            {
              type: "delegation",
              kind: "parallel",
              label:
                parallel.branchLabel || `branch ${parallel.branchIndex + 1}`,
              detail: routingDetail(parallel),
              childId: parallel.childId || undefined,
              parentCallId: parallel.parentCallId || undefined,
              branchIndex: parallel.branchIndex,
              routingReason: parallel.routingReason || undefined,
              model: parallel.model || undefined,
            },
          ];
        case "branch_tool":
          return [
            {
              type: "delegation_progress",
              parentCallId: parallel.parentCallId || undefined,
              branchIndex: parallel.branchIndex,
              toolCount: parallel.toolCount,
              ...usageTokens(parallel.usage),
              ...activityPreview(parallel),
            },
          ];
        case "branch_end":
          return [
            {
              type: "delegation_end",
              childId: parallel.childId || undefined,
              parentCallId: parallel.parentCallId || undefined,
              branchIndex: parallel.branchIndex,
              stop: parallel.stop,
              failed: parallel.failed || undefined,
              toolCount: parallel.toolCount,
              ...usageTokens(parallel.usage),
              durationMs: tokens(parallel.durationMs),
            },
          ];
        default:
          return [];
      }
    }
    case "parallel.end": {
      // The group terminal: the winning branch (−1 = none / join=all), the
      // run's stop, and the group's usage.
      const parallel = event.payload;
      return [
        {
          type: "parallel_end",
          parentCallId: parallel.parentCallId,
          join: parallel.join,
          branchCount: parallel.branchCount,
          winner: parallel.winner,
          stop: parallel.stop,
          ...usageTokens(parallel.usage),
        },
      ];
    }
    case "team.member": {
      // One member's redacted activity, tagged with its roster name. The
      // inner kind routes it on the lane; usage is PER EVENT (the lane sums);
      // contextUsed/contextWindow feed the per-member context meter.
      const team = event.payload;
      return [
        {
          type: "team_member",
          teamId: team.teamId,
          parentCallId: team.parentCallId,
          member: team.member,
          memberSessionId: team.memberSessionId || undefined,
          ...activityPreview(team),
          // The inner kind and error flag are the ROUTING facts here, so
          // they stay exact (never collapsed to undefined).
          innerKind: team.innerKind,
          isError: team.isError,
          ...usageTokens(team.usage),
          contextUsed: tokens(team.contextUsed),
          contextWindow: tokens(team.contextWindow),
          cause: team.cause || undefined,
        },
      ];
    }
    case "team.tasks": {
      const team = event.payload;
      return [
        {
          type: "team_tasks",
          teamId: team.teamId,
          parentCallId: team.parentCallId,
          tasks: teamTasks(team.tasks),
        },
      ];
    }
    case "team.findings": {
      const team = event.payload;
      return [
        {
          type: "team_findings",
          teamId: team.teamId,
          parentCallId: team.parentCallId,
          findings: teamFindings(team.findings),
        },
      ];
    }
    case "team.end": {
      // The team terminal: rounds, stop, the TEAM-TOTAL usage, the final
      // snapshots, and each member's disposition (stopped + reason + retries).
      const team = event.payload;
      return [
        {
          type: "team_end",
          teamId: team.teamId,
          parentCallId: team.parentCallId,
          rounds: team.rounds,
          stop: team.stop,
          ...usageTokens(team.usage),
          tasks: teamTasks(team.tasks),
          findings: teamFindings(team.findings),
          dispositions: teamDispositions(team.dispositions),
        },
      ];
    }
    case "authorization.required": {
      // A tool call parked on a browser sign-in: the daemon closes the prompt
      // stream WITHOUT a result and the run continues over the authorization
      // control stream. The URL never rides an event — the client fetches it
      // from the presentation control when the operator opens it.
      const authorization = event.payload;
      return [
        {
          type: "authorization",
          authorizationId: authorization.authorizationId,
          callId: authorization.callId,
          displayName: authorization.displayName,
          status: authorization.status,
          expiresAt: timestampMs(authorization.expiresAt),
        },
      ];
    }
    case "authorization.resolved": {
      const authorization = event.payload;
      return [
        {
          type: "authorization_resolved",
          authorizationId: authorization.authorizationId,
          displayName: authorization.displayName,
          status: authorization.status,
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
    case "turn.end": {
      // One model exchange closed: the turn's own token figures + elapsed
      // model-call time (the per-turn stat line and the context meter's
      // occupancy read these; `result` below stays the per-RUN total).
      const turn = event.payload;
      const usage = turn.usage;
      return [
        {
          type: "turn_end",
          durationMs: tokens(turn.durationMs),
          inputTokens: tokens(usage?.inputTokens),
          outputTokens: tokens(usage?.outputTokens),
          cacheReadTokens: tokens(usage?.cacheReadTokens),
          cacheWriteTokens: tokens(usage?.cacheWriteTokens),
          reasoningTokens: tokens(usage?.reasoningTokens),
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
    case "provider.route":
      // The downstream provider the turn was routed to (ADR 0210). The Go
      // loop emits it as `Event{Type: EvProviderRoute, Text: route}`, so the
      // SDK payload is undefined and the label rides `text`. Metadata for the
      // status strip's model segment, not a transcript line; absent on a
      // cache hit, so an empty text is nothing to show — never fabricated.
      return event.text ? [{ type: "provider_route", label: event.text }] : [];
    case "session.title": {
      // The daemon's durable title lifecycle changed (first-prompt seed,
      // auto-title, a rename from any client). Session metadata the host
      // adopts onto the inventory row — never a transcript line.
      const title = event.payload;
      return [
        {
          type: "title",
          title: title.title || "",
          provenance: title.provenance || "",
          // uint64 crosses the SDK as bigint; the UI keeps a plain number.
          revision:
            typeof title.revision === "bigint" ? Number(title.revision) : null,
        },
      ];
    }
    case "hook": {
      // A hook fired: phase, tool and decision ride the payload, the
      // human-readable message rides `text`. It renders as a chip on the
      // tool call it names, or as a marked transcript notice for a
      // call-less lifecycle hook — never dropped.
      const hook = event.payload;
      return [
        {
          type: "hook",
          phase: hook.phase,
          tool: hook.tool,
          decision: HOOK_DECISION[hook.decision] ?? "info",
          callId: hook.callId,
          text: event.text,
        },
      ];
    }
    case "unknown":
      return [unrenderedNotice(event.wireKind)];
    default:
      if (SILENT_EVENT_KINDS.has(event.kind)) return [];
      if (ADVISORY_EVENT_KINDS.has(event.kind)) {
        return event.text ? [{ type: "notice", text: event.text }] : [];
      }
      if (TRANSIENT_STATUS_KINDS.has(event.kind)) {
        // Both are warning-coloured: the model stalled, or the session just
        // recovered from a permanent failure and may hit it again.
        return event.text
          ? [
              {
                type: "status",
                text: event.text,
                tone: "warn",
                kind:
                  event.kind === "no_progress"
                    ? "no_progress"
                    : "recover_notice",
              },
            ]
          : [];
      }
      return [unrenderedNotice(event.kind)];
  }
}
