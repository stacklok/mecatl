/**
 * Manual memory consolidation ("dream") review — ADR 0227, over the SDK's
 * `client.dreamPlans` namespace.
 *
 * Wire: `POST /v1/dream/plans {target}` generates a bounded, daemon-curated
 * consolidation plan; `POST /v1/dream/plans/{plan_id}/decision {decision}`
 * applies or dismisses the WHOLE plan. Studio never composes memory content —
 * the user only approves what the daemon curated (memory rule 8).
 *
 * Plan ids are process-local: a daemon restart (or the retention window
 * expiring) answers `dream_not_found` (404) — regenerate, never retry. Gated
 * by `capabilities.manual_dream.{project_memory,user_model}.{generate,decide}`
 * on GET /v1/compatibility.
 */

import type {
  DreamReviewPlan,
  DreamParticipant as ProtoDreamParticipant,
  DreamReceipt as ProtoDreamReceipt,
} from "@stacklok-oss/mecatl-sdk/gen";

import { HarnessApiError } from "./errors";
import { getHarnessClient, harness } from "./sdk";
import { timestampUnix } from "./time";

export type DreamTarget = "project_memory" | "user_model";
export type DreamDecision = "apply" | "dismiss";

interface DreamParticipant {
  key: string;
  value: string;
  description: string;
}

interface DreamOperation {
  kind: string;
  survivor: DreamParticipant;
  sources: DreamParticipant[];
  replacement: { value: string; description: string };
  reason: string;
  exactDuplicateEligible: boolean;
}

export interface DreamPlan {
  id: string;
  target: string;
  expiresAtUnix: number;
  plannedOperationCount: number;
  plannedSourceCount: number;
  operations: DreamOperation[];
}

export interface DreamReceipt {
  id: string;
  target: string;
  disposition: string;
  planned: number;
  applied: number;
  conflicted: number;
  skipped: number;
  failed: number;
}

function decodeParticipant(
  participant: ProtoDreamParticipant | undefined,
): DreamParticipant {
  return {
    key: participant?.key ?? "",
    value: participant?.value ?? "",
    description: participant?.description ?? "",
  };
}

function decodeDreamPlan(plan: DreamReviewPlan | undefined): DreamPlan {
  return {
    id: plan?.id ?? "",
    target: plan?.target ?? "",
    expiresAtUnix: timestampUnix(plan?.expiresAt),
    plannedOperationCount: plan?.plannedOperationCount ?? 0,
    plannedSourceCount: plan?.plannedSourceCount ?? 0,
    operations: (plan?.operations ?? []).map((operation) => ({
      kind: operation.kind,
      survivor: decodeParticipant(operation.survivor),
      sources: operation.sources.map(decodeParticipant),
      replacement: {
        value: operation.replacement?.value ?? "",
        description: operation.replacement?.description ?? "",
      },
      reason: operation.reason,
      exactDuplicateEligible: operation.exactDuplicateEligible,
    })),
  };
}

function decodeDreamReceipt(
  receipt: ProtoDreamReceipt | undefined,
): DreamReceipt {
  return {
    id: receipt?.id ?? "",
    target: receipt?.target ?? "",
    disposition: receipt?.disposition ?? "",
    planned: receipt?.plannedSourceCount ?? 0,
    applied: receipt?.appliedSourceCount ?? 0,
    conflicted: receipt?.conflictedSourceCount ?? 0,
    skipped: receipt?.skippedSourceCount ?? 0,
    failed: receipt?.failedSourceCount ?? 0,
  };
}

/** Generates a consolidation plan. Synchronous and potentially slow. */
export async function generateDreamPlan(
  target: DreamTarget,
  signal?: AbortSignal,
): Promise<DreamPlan> {
  const response = await harness(() =>
    getHarnessClient().dreamPlans.generate(
      { $typeName: "mecatl.v1.GenerateDreamPlanRequest", target },
      { signal },
    ),
  );
  return decodeDreamPlan(response.plan);
}

/** Applies or dismisses the whole retained plan. */
export async function decideDreamPlan(
  planId: string,
  decision: DreamDecision,
): Promise<DreamReceipt> {
  const response = await harness(() =>
    getHarnessClient().dreamPlans.decide({
      $typeName: "mecatl.v1.DecideDreamPlanRequest",
      planId,
      decision,
    }),
  );
  return decodeDreamReceipt(response.receipt);
}

/**
 * True when the plan id no longer resolves — unknown, expired, or minted by a
 * daemon process that has since restarted. The fix is regenerate, not retry.
 */
export function isStaleDreamPlan(error: unknown): boolean {
  return (
    error instanceof HarnessApiError &&
    (error.code === "dream_not_found" ||
      error.code === "dream_terminal_conflict" ||
      (error.code === "" && (error.status === 404 || error.status === 410)))
  );
}

/** The per-target capability object off `capabilities.manual_dream`. */
export interface DreamTargetCapability {
  generate: boolean;
  decide: boolean;
  unavailableReason: string;
}

/**
 * Reads one target's capability out of the compatibility document's
 * `manual_dream` object — the wire-keyed projection `fetchHarnessCompatibility`
 * returns (`{project_memory: {generate, decide, unavailable_reason}, …}`).
 * Absent daemon/target → all-false.
 */
export function dreamTargetCapability(
  manualDream: unknown,
  target: DreamTarget,
): DreamTargetCapability {
  const entry = asRecord(asRecord(manualDream)[target]);
  return {
    generate: entry.generate === true,
    decide: entry.decide === true,
    unavailableReason:
      typeof entry.unavailable_reason === "string"
        ? entry.unavailable_reason
        : "",
  };
}

function asRecord(raw: unknown): Record<string, unknown> {
  return typeof raw === "object" && raw !== null && !Array.isArray(raw)
    ? (raw as Record<string, unknown>)
    : {};
}
