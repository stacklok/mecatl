import { approvalKey } from "./chat-state";

type AskTarget = { askId: string; runId: string; sessionId: string };

/**
 * TERM: VerdictLedger — the one record, per surface, of what this client has
 * done about each pending ask's verdict: nothing, sending it, delivered, or
 * sent with an unknown outcome.
 * Avoid: "in-flight set", "uncertain set", "acknowledged set" (the three
 * parallel Sets this replaces), "verdict state".
 *
 * TERM: uncertain — a verdict POST failed ambiguously, so the server may or
 * may not hold it. The ask stays visible and blocked until a stream event
 * settles it or the ledger is cleared; a blind retry could double-resolve.
 * Avoid: "failed", "errored" (a definite failure leaves the ask answerable).
 *
 * DECISION: one phase per ask key, not three membership sets. The sets
 * could hold contradictory states (in flight AND uncertain) and the
 * workspace and side panel disagreed on which combinations block. A single
 * phase makes contradiction unrepresentable. Rejected: keeping the Sets and
 * sharing a helper that reads all three.
 *
 * DECISION: plan and ordinary asks share one ledger per surface. Their
 * blocking rules are identical; only the delivery call differs. Rejected:
 * separate plan/ordinary ledgers (the workspace's current shape), which is
 * why retry and Escape could be allowed for one kind and blocked for the
 * other after the same ambiguous failure.
 *
 * SPEC: begin() on an ask that is not idle returns false and changes nothing
 * (double-click sends one POST; uncertain asks cannot be retried).
 * SPEC: settle by stream event (reset) returns the ask to idle from any phase.
 * SPEC: blocks() is true for every phase except idle.
 *
 * DECISION: the ledger is a plain object held in a ref; callers keep their
 * own re-render epoch. Rejected: an external store / useSyncExternalStore,
 * which adds subscription machinery for one component's re-render.
 *
 * Only the side panel uses it today; the workspace still holds four Sets.
 */
export type VerdictPhase = "idle" | "inFlight" | "acknowledged" | "uncertain";

export class VerdictLedger {
  private readonly phases = new Map<string, Exclude<VerdictPhase, "idle">>();

  phase(target: AskTarget): VerdictPhase {
    return this.phases.get(approvalKey(target)) ?? "idle";
  }

  blocks(target: AskTarget): boolean {
    return this.phase(target) !== "idle";
  }

  begin(target: AskTarget): boolean {
    if (this.blocks(target)) return false;
    this.phases.set(approvalKey(target), "inFlight");
    return true;
  }

  acknowledge(target: AskTarget): void {
    this.phases.set(approvalKey(target), "acknowledged");
  }

  markUncertain(target: AskTarget): void {
    this.phases.set(approvalKey(target), "uncertain");
  }

  reset(target: AskTarget): void {
    this.phases.delete(approvalKey(target));
  }

  clear(): void {
    this.phases.clear();
  }
}
