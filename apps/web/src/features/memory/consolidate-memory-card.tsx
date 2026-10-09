// SPDX-License-Identifier: Apache-2.0

import type { MemoryConsolidationTarget } from "@mecatl-studio/contracts";
import type {
  DecideMemoryConsolidationPlanResponse,
  GenerateMemoryConsolidationPlanResponse,
  GetRuntimeResponse,
} from "@mecatl-studio/contracts/generated";
import {
  decideMemoryConsolidationPlanMutation,
  generateMemoryConsolidationPlanMutation,
  getRuntimeOptions,
  listUserMemoryQueryKey,
} from "@mecatl-studio/contracts/query";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "../../components/ui/alert-dialog";
import { Badge } from "../../components/ui/badge";
import { Button } from "../../components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../../components/ui/select";
import { errorMessage } from "../knowledge/format";
import { SettingsCard } from "../settings/settings-card";

/**
 * Consolidate memory ("dream" review): the agent suggests which of its
 * memories to merge and the person applies or dismisses the WHOLE
 * suggestion — this deployment never composes memory content, only approves
 * what the daemon curated. The card offers a `target` picker over the
 * memories the agent can consolidate.
 *
 * A decision whose outcome is still open locks the card to retrying that
 * SAME decision (`dream_in_progress`, or a dropped connection — the answer
 * may already have applied); a suggestion that is no longer valid clears
 * with a "consolidate again" notice (`dream_not_found`/`dream_conflict`); a
 * target the agent cannot consolidate is offered disabled, not hidden.
 */

type ManualDream = NonNullable<GetRuntimeResponse["capabilities"]["manualDream"]>;
type TargetCapability = NonNullable<ManualDream["userModel"]>;

const TARGET_LABELS: Record<MemoryConsolidationTarget, string> = {
  project_memory: "Project memory",
  user_model: "Facts about you",
};

/** The two targets, in the order the picker lists them. */
const TARGETS: readonly MemoryConsolidationTarget[] = ["user_model", "project_memory"];

const STILL_WORKING = "The agent is still working on that. Press Try again to see the result.";
const NO_LONGER_VALID =
  "That suggestion is no longer valid, so nothing changed. Consolidate again for a new one.";
const CANNOT_SUGGEST = "Consolidation isn't available for this memory right now.";
const CANNOT_APPLY = "Suggestions for this memory can be viewed but not applied.";
const DECISION_RUNNING = "Waiting for the other decision to finish.";

type Confirmation = "apply" | "generate";

const CONFIRMATIONS: Record<
  Confirmation,
  { confirmText: string; description: string; title: string }
> = {
  apply: {
    confirmText: "Apply",
    description:
      "The memories are merged exactly as shown. Anything that changed in the meantime is left alone.",
    title: "Apply these changes?",
  },
  generate: {
    confirmText: "Continue",
    description:
      "The agent reads everything it remembers and suggests which entries to merge. Nothing changes until you approve.",
    title: "Consolidate memory?",
  },
};

/** The targets the agent reports at all, whether or not it can generate —
 *  so a target that exists but is unavailable shows disabled with its
 *  reason rather than silently disappearing. Empty against an older daemon
 *  without the capability, which hides the whole card. */
function listConsolidationTargets(
  manualDream: ManualDream | undefined,
): MemoryConsolidationTarget[] {
  return TARGETS.filter((target) => targetEntry(manualDream, target));
}

function targetEntry(
  manualDream: ManualDream | undefined,
  target: MemoryConsolidationTarget,
): TargetCapability | undefined {
  return target === "project_memory" ? manualDream?.projectMemory : manualDream?.userModel;
}

function targetCapability(
  manualDream: ManualDream | undefined,
  target: MemoryConsolidationTarget,
): TargetCapability {
  return targetEntry(manualDream, target) ?? { decide: false, generate: false };
}

/** The result as one line: what merged, then anything left out. */
export function describeMemoryConsolidationReceipt(
  receipt: DecideMemoryConsolidationPlanResponse,
): string {
  const noun = receipt.planned === 1 ? "memory" : "memories";
  const parts = [`${receipt.applied} of ${receipt.planned} ${noun} merged`];
  if (receipt.conflicted > 0) parts.push(`${receipt.conflicted} left alone`);
  if (receipt.skipped > 0) parts.push(`${receipt.skipped} skipped`);
  if (receipt.failed > 0) parts.push(`${receipt.failed} failed`);
  return parts.join(" · ");
}

/** A dismissal changes nothing, so it carries no per-memory outcome. */
function isDismissed(receipt: DecideMemoryConsolidationPlanResponse) {
  return receipt.disposition === "dismiss" || receipt.disposition === "dismissed";
}

const plural = (count: number, one: string, many: string) => `${count} ${count === 1 ? one : many}`;

/**
 * How long until `ts` (epoch milliseconds), in the largest whole unit:
 * "<1m", "14m", "3h", "2d". Empty for a missing or nonsensical timestamp.
 */
export function formatUntilTime(ts: number, now = Date.now()): string {
  if (!ts || ts < 1000) return "";
  const diffMin = Math.floor((ts - now) / 60000);
  if (diffMin < 1) return "<1m";
  if (diffMin < 60) return `${diffMin}m`;
  const diffHr = Math.floor(diffMin / 60);
  if (diffHr < 24) return `${diffHr}h`;
  return `${Math.floor(diffHr / 24)}d`;
}

/**
 * True when the decision's outcome is still open: the daemon reports the
 * SAME decision is running (`dream_in_progress`), or the answer never
 * arrived at all (a thrown non-problem error — a dropped connection, so the
 * first request may already have applied). The only safe next step is to
 * retry the IDENTICAL decision; decide is idempotent and answers the
 * authoritative receipt.
 */
export function isDreamInProgress(error: unknown): boolean {
  if (typeof error !== "object" || error === null) return true;
  if ("code" in error) return error.code === "dream_in_progress";
  return true;
}

/**
 * True when the plan is no longer actionable for THIS decision — the id no
 * longer resolves (unknown, expired, or minted by a daemon process that has
 * since restarted, `dream_not_found`), or a conflicting/terminal decision
 * already won (`dream_conflict`/`dream_terminal_conflict`). The fix is
 * regenerate, not retry.
 */
export function isStaleMemoryPlan(error: unknown): boolean {
  if (typeof error !== "object" || error === null) return false;
  if (
    "code" in error &&
    (error.code === "dream_not_found" ||
      error.code === "dream_conflict" ||
      error.code === "dream_terminal_conflict")
  )
    return true;
  return "status" in error && (error.status === 404 || error.status === 410);
}

export function ConsolidateMemoryCard() {
  const queryClient = useQueryClient();
  const runtime = useQuery(getRuntimeOptions());
  const connected = runtime.data?.connection === "online";
  const manualDream = runtime.data?.capabilities.manualDream;
  const targets = useMemo(() => listConsolidationTargets(manualDream), [manualDream]);

  const generate = useMutation(generateMemoryConsolidationPlanMutation());
  const decide = useMutation(decideMemoryConsolidationPlanMutation());
  const [chosenTarget, setChosenTarget] = useState<MemoryConsolidationTarget | null>(null);
  const [plan, setPlan] = useState<GenerateMemoryConsolidationPlanResponse>();
  const [receipt, setReceipt] = useState<DecideMemoryConsolidationPlanResponse>();
  // The decision whose outcome is still open; only that decision may be
  // retried while it is set (never automatically).
  const [pendingDecision, setPendingDecision] = useState<"apply" | "dismiss" | null>(null);
  const [notice, setNotice] = useState<string>();
  const [confirming, setConfirming] = useState<Confirmation | null>(null);

  // Absent capability → the card hides entirely (an older agent).
  if (targets.length === 0) return null;

  const effectiveTarget: MemoryConsolidationTarget =
    chosenTarget && targets.includes(chosenTarget)
      ? chosenTarget
      : // `targets[0]` is safe: the early return above guarantees length > 0.
        (targets.find((candidate) => targetCapability(manualDream, candidate).generate) ??
        (targets[0] as MemoryConsolidationTarget));
  const capability = targetCapability(manualDream, effectiveTarget);

  async function generatePlan() {
    setPlan(undefined);
    setReceipt(undefined);
    setNotice(undefined);
    setPendingDecision(null);
    try {
      setPlan(await generate.mutateAsync({ body: { target: effectiveTarget } }));
    } catch {
      // The generated mutation error is rendered below.
    }
  }

  async function decidePlan(decision: "apply" | "dismiss") {
    if (!plan) return;
    setNotice(undefined);
    try {
      const result = await decide.mutateAsync({
        body: { decision },
        path: { planId: plan.id },
      });
      setReceipt(result);
      setPlan(undefined);
      setPendingDecision(null);
      if (decision === "apply")
        await queryClient.invalidateQueries({ queryKey: listUserMemoryQueryKey() });
    } catch (error) {
      if (isStaleMemoryPlan(error)) {
        setPlan(undefined);
        setPendingDecision(null);
        setNotice(NO_LONGER_VALID);
      } else if (isDreamInProgress(error)) {
        setPendingDecision(decision);
        setNotice(STILL_WORKING);
      }
      // An unclassified failure: the suggestion stays reviewable; an
      // already-open decision stays locked until a terminal answer arrives.
    }
  }

  function confirmed() {
    const action = confirming;
    setConfirming(null);
    if (action === "generate") void generatePlan();
    else if (action === "apply") void decidePlan("apply");
  }

  const generateBlocked = capability.generate ? undefined : CANNOT_SUGGEST;
  const applyBlocked =
    pendingDecision === "dismiss" ? DECISION_RUNNING : capability.decide ? undefined : CANNOT_APPLY;
  const dismissBlocked = pendingDecision === "apply" ? DECISION_RUNNING : undefined;

  const expiresIn =
    plan?.expiresAt && Date.parse(plan.expiresAt) > Date.now()
      ? formatUntilTime(Date.parse(plan.expiresAt))
      : "";

  const error = generate.error ?? decide.error;
  const confirmation = confirming ? CONFIRMATIONS[confirming] : undefined;

  return (
    <SettingsCard title="Consolidate memory">
      <div className="space-y-3">
        {!plan && (
          <div className="flex flex-wrap items-center gap-2">
            {targets.length > 1 ? (
              <Select
                onValueChange={(value) => setChosenTarget(value as MemoryConsolidationTarget)}
                value={effectiveTarget}
              >
                <SelectTrigger
                  aria-label="Memory to consolidate"
                  className="w-full min-w-56 flex-1"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {targets.map((value) => {
                    const canGenerate = targetCapability(manualDream, value).generate;
                    return (
                      <SelectItem disabled={!canGenerate} key={value} value={value}>
                        {TARGET_LABELS[value]}
                        {!canGenerate && " (unavailable)"}
                      </SelectItem>
                    );
                  })}
                </SelectContent>
              </Select>
            ) : (
              <span className="text-sm text-muted-foreground">
                {TARGET_LABELS[effectiveTarget]}
              </span>
            )}
            <Button
              disabled={
                !connected || generate.isPending || decide.isPending || Boolean(generateBlocked)
              }
              onClick={() => setConfirming("generate")}
              size="sm"
              title={generateBlocked}
            >
              {generate.isPending ? "Reviewing…" : "Consolidate memory"}
            </Button>
          </div>
        )}
        {(!capability.generate || !capability.decide) && (
          <p className="text-xs text-muted-foreground" data-testid="dream-unavailable">
            {capability.generate ? CANNOT_APPLY : CANNOT_SUGGEST}
          </p>
        )}
        {generate.isPending && (
          <p className="text-xs text-muted-foreground">
            The agent is reviewing its memory. This can take a minute.
          </p>
        )}
        {notice && (
          <p
            className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-400"
            role="status"
          >
            {notice}
          </p>
        )}
        {error && !notice && (
          <p className="text-sm text-destructive" role="alert">
            {errorMessage(error)}
          </p>
        )}

        {plan && plan.operations.length === 0 && (
          <div className="flex flex-wrap items-center gap-3 rounded-lg border border-dashed px-4 py-3">
            <p className="text-sm text-muted-foreground">
              Nothing to merge. Memory is already tidy.
            </p>
            <Button
              disabled={decide.isPending}
              onClick={() => void decidePlan("dismiss")}
              size="sm"
              variant="outline"
            >
              {pendingDecision === "dismiss" ? "Try again" : "OK"}
            </Button>
          </div>
        )}

        {plan && plan.operations.length > 0 && (
          <div className="space-y-3">
            <p className="text-sm" data-testid="dream-plan-summary">
              {plural(plan.plannedOperationCount, "suggested change", "suggested changes")} across{" "}
              {plural(plan.plannedSourceCount, "memory", "memories")}. Apply or dismiss them all
              together.
              {expiresIn && <span className="text-muted-foreground"> Expires in {expiresIn}.</span>}
            </p>
            <ul className="space-y-2">
              {plan.operations.map((operation) => (
                <li
                  className="space-y-2 rounded-lg border p-3"
                  key={`${operation.kind}:${operation.survivor.key}:${operation.sources
                    .map((source) => source.key)
                    .join(",")}`}
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant="muted">{operation.kind.replaceAll("_", " ") || "merge"}</Badge>
                    {operation.exactDuplicateEligible && (
                      <Badge variant="outline">exact duplicate</Badge>
                    )}
                    {operation.reason && (
                      <span className="text-xs text-muted-foreground">{operation.reason}</span>
                    )}
                  </div>
                  <div className="space-y-1 text-xs">
                    <p className="font-medium">
                      Keeps <span className="font-mono">{operation.survivor.key}</span>
                      {operation.sources.length > 0 && (
                        <>
                          {" "}
                          and merges in{" "}
                          {operation.sources.map((source, i) => (
                            <span className="font-mono" key={source.key}>
                              {i > 0 && ", "}
                              {source.key}
                            </span>
                          ))}
                        </>
                      )}
                    </p>
                    {operation.replacement.value && (
                      <pre className="max-h-32 overflow-y-auto rounded-md bg-muted px-3 py-2 font-mono whitespace-pre-wrap text-muted-foreground">
                        {operation.replacement.value}
                      </pre>
                    )}
                    {operation.replacement.description && (
                      <p className="text-muted-foreground">{operation.replacement.description}</p>
                    )}
                    <details className="rounded-md border px-2 py-1">
                      <summary className="cursor-pointer select-none text-muted-foreground">
                        Show current values
                      </summary>
                      <dl className="mt-2 space-y-2">
                        <ParticipantDetail label="Keeps" participant={operation.survivor} />
                        {operation.sources.map((source) => (
                          <ParticipantDetail
                            key={source.key}
                            label="Merges in"
                            participant={source}
                          />
                        ))}
                      </dl>
                    </details>
                  </div>
                </li>
              ))}
            </ul>
            <div className="flex flex-wrap items-center gap-2">
              <Button
                disabled={decide.isPending || Boolean(applyBlocked)}
                onClick={() =>
                  pendingDecision === "apply" ? void decidePlan("apply") : setConfirming("apply")
                }
                size="sm"
                title={applyBlocked}
              >
                {pendingDecision === "apply" ? "Try again" : "Apply"}
              </Button>
              <Button
                disabled={decide.isPending || Boolean(dismissBlocked)}
                onClick={() => void decidePlan("dismiss")}
                size="sm"
                title={dismissBlocked}
                variant="outline"
              >
                {pendingDecision === "dismiss" ? "Try again" : "Dismiss"}
              </Button>
            </div>
          </div>
        )}

        {receipt && (
          <div className="space-y-1 text-sm text-muted-foreground" data-testid="dream-receipt">
            <p>
              {isDismissed(receipt)
                ? "Dismissed. Nothing changed."
                : `Done: ${describeMemoryConsolidationReceipt(receipt)}.`}
            </p>
            {!isDismissed(receipt) && receipt.conflicted > 0 && (
              <p>
                Some memories changed while you were reviewing, so they were left alone. Consolidate
                again to review them.
              </p>
            )}
            {!isDismissed(receipt) && receipt.failed > 0 && (
              <p>Some memories couldn&rsquo;t be merged. The counts above are the final result.</p>
            )}
          </div>
        )}
      </div>

      <AlertDialog onOpenChange={(open) => !open && setConfirming(null)} open={Boolean(confirming)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{confirmation?.title}</AlertDialogTitle>
            <AlertDialogDescription>{confirmation?.description}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={confirmed}>{confirmation?.confirmText}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsCard>
  );
}

/** One memory's current stored value and description — what the change
 *  keeps or merges in. */
function ParticipantDetail({
  label,
  participant,
}: {
  label: string;
  participant: { description: string; key: string; value: string };
}) {
  return (
    <div className="space-y-1">
      <dt className="font-medium">
        <span className="text-muted-foreground">{label} </span>
        <span className="font-mono">{participant.key || "(unnamed)"}</span>
      </dt>
      <dd className="space-y-1">
        <pre className="max-h-32 overflow-y-auto rounded-md bg-muted px-3 py-2 font-mono whitespace-pre-wrap text-muted-foreground">
          {participant.value || "(empty)"}
        </pre>
        {participant.description && (
          <p className="text-muted-foreground">{participant.description}</p>
        )}
      </dd>
    </div>
  );
}
