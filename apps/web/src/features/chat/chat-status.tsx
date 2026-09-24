// SPDX-License-Identifier: Apache-2.0

import type { RunFailure } from "./chat-state";
import { stopReasonDescription } from "./stop-reason-chip";

export interface ChatStatusFacts {
  approvals?: readonly { askId: string }[];
  failure?: RunFailure;
  phase: "idle" | "sending" | "following" | "closed";
  runId?: string;
  sawResult?: boolean;
  sessionState?: string;
  stopReason?: string;
}

export interface ChatRunStatus {
  kind:
    | "sending"
    | "working"
    | "waiting"
    | "completed"
    | "cancelled"
    | "stopped"
    | "failed"
    | "uncertain";
  label: string;
}

/** Derive one stable announcement from run facts, never from stream silence. */
export function deriveChatStatus(facts: ChatStatusFacts): ChatRunStatus | undefined {
  if (facts.failure) return { kind: "failed", label: "Failed" };
  if (facts.sawResult) {
    const reason = (facts.stopReason ?? "").trim().toLowerCase();
    if (reason === "cancelled") return { kind: "cancelled", label: "Cancelled" };
    if (reason === "error" || reason === "failed" || reason === "failure") {
      return { kind: "failed", label: "Failed" };
    }
    if (!reason || reason === "end_turn") return { kind: "completed", label: "Completed" };
    return {
      kind: "stopped",
      label: stopReasonDescription(reason) ?? "Stopped",
    };
  }
  if (facts.phase === "closed") {
    return { kind: "uncertain", label: "Unknown outcome — reconnect to check this run" };
  }
  if (
    facts.phase === "following" &&
    (facts.approvals?.length || facts.sessionState === "awaiting")
  ) {
    return { kind: "waiting", label: "Waiting for approval" };
  }
  if (facts.phase === "following" || facts.runId) return { kind: "working", label: "Working" };
  if (facts.phase === "sending") return { kind: "sending", label: "Sending" };
  return undefined;
}

/** Text changes only at state boundaries, so streamed tokens do not repeat announcements. */
export function ChatStatus({ status }: { status?: ChatRunStatus }) {
  if (!status) return null;
  return (
    <p
      aria-atomic="true"
      aria-live="polite"
      className="min-w-0 break-words text-xs text-muted-foreground"
      data-status={status.kind}
      role="status"
    >
      {status.label}
    </p>
  );
}
