/**
 * Plan-mode review: the pure half of the PresentPlan ask surface.
 *
 * A plan ask is an ordinary `permission.ask` whose tool is the engine's
 * PresentPlan signalling tool (engine/agent/presentplan.go) with `{plan,
 * note}` args. The engine keys the plan outcome on the bare verdict
 * (engine/agent/dispatch.go `surfacePlanAsk`): allow_once = approve & run
 * (mode → default), allow_always = auto-accept edits (mode → acceptEdits),
 * deny = iterate. The wire mapping in `respondToApproval` is unchanged; this
 * module only names what each verdict DOES for a plan.
 *
 * On the interactive resumeApproval path (the SDK's `controls(runId)
 * .resolveAsk`, which Studio uses) the daemon does NOT start the execution
 * run — `Service.ApprovePlan` is the parked-plan path and refuses a session
 * with a live run — so, like mecatui (cmd/mecatui/ui/update.go, the
 * `plan_approved` arm), the client that resolved the ask sends the
 * harness-framed proceed prompt itself once the plan run ends.
 */

import {
  PLAN_APPROVAL_TOOL,
  PLAN_APPROVED_PROCEED_TEXT,
} from "@stacklok-oss/mecatl-sdk";

import type { AgentMessage } from "./types";

/**
 * The engine's plan-approval signalling tool and the harness-framed prompt
 * that starts the execution run after an interactively approved plan. Both
 * are the SDK's own constants (sdk/typescript/src/plan.ts), re-exported so
 * the feature keeps one import path; the proceed text is byte-identical to
 * mecatui's `planApprovedProceedText`, so every client starts execution with
 * the same recorded user turn.
 */
export { PLAN_APPROVAL_TOOL, PLAN_APPROVED_PROCEED_TEXT };

/** The run's terminal stop after an approved plan (session.StopPlanApproved). */
export const STOP_PLAN_APPROVED = "plan_approved";

/** The run's terminal stop after an iterate verdict (session.StopPlanIterate). */
export const STOP_PLAN_ITERATE = "plan_iterate";

/** True when the ask names the PresentPlan tool (the TUI's `isPlanAsk`). */
export function isPlanAsk(toolName: string | undefined | null): boolean {
  return (toolName ?? "").trim() === PLAN_APPROVAL_TOOL;
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

/**
 * The plan body to review from the ask's verbatim `args` JSON: the `plan`
 * text, falling back to the one-line `note`; "" for empty, malformed, or
 * non-object args (the TUI's `planBodyFromArgs`). Trailing newlines are
 * trimmed so the rendered markdown ends where the plan does.
 */
export function planBodyFromArgs(args: string | undefined | null): string {
  const raw = (args ?? "").trim();
  if (raw === "") return "";
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return "";
  }
  if (!isRecord(parsed)) return "";
  const plan = typeof parsed.plan === "string" ? parsed.plan : "";
  const note = typeof parsed.note === "string" ? parsed.note : "";
  return (plan || note).replace(/\n+$/, "");
}

/**
 * The verdict wire strings that APPROVE a plan (approve & run, auto-accept
 * edits). Only an approval arms the auto-proceed: an iterate verdict ends
 * the run cleanly for the operator's next typed prompt.
 */
export function isPlanApprovalVerdict(
  verdict: "allow_once" | "allow_always" | "deny",
): boolean {
  return verdict !== "deny";
}

/**
 * The auto-proceed gate, pure. Fires exactly when the run ended on
 * `plan_approved`, THIS client armed it by approving the plan ask (a tab
 * merely watching the session — or mecatui, which proceeds on its own —
 * must not race a second proceed prompt into the daemon), and no ask is
 * still waiting on the operator. Never on `plan_iterate`, never twice for
 * one arm (the caller clears the arm when it consumes it).
 */
export function shouldAutoProceed(input: {
  stop: string | undefined;
  armed: boolean;
  queueLength: number;
}): boolean {
  return (
    input.stop === STOP_PLAN_APPROVED && input.armed && input.queueLength === 0
  );
}

/**
 * A harness-authored user turn: the synthetic proceed prompt this tab sent
 * (flagged at send time), or the same recorded text coming back from the
 * transcript / durable-log replay, which carries no flag. Renders as a muted
 * harness note rather than a user bubble. A scheduled delivery note is its
 * own card and never matches.
 */
export function isHarnessProceedMessage(
  message: Pick<AgentMessage, "role" | "content" | "synthetic" | "delivery">,
): boolean {
  if (message.role !== "user" || message.delivery) return false;
  return (
    message.synthetic === true ||
    message.content.trim() === PLAN_APPROVED_PROCEED_TEXT
  );
}
