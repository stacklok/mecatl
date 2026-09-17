"use client";

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
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import { formatUntilTime } from "@/lib/formatters";
import {
  type DreamDecision,
  type DreamParticipant,
  type DreamPlan,
  type DreamReceipt,
  type DreamTarget,
  decideDreamPlan,
  dreamTargetCapability,
  generateDreamPlan,
  isDreamInProgress,
  isStaleDreamPlan,
  listDreamTargets,
} from "@/lib/harness/dream";
import { SettingsCard } from "../../_components/settings-card";

/**
 * Consolidate memory: the agent suggests which of its memories to merge and
 * the person applies or dismisses the WHOLE suggestion — Studio never
 * composes memory content (memory rule 8). The calls are unchanged: generate
 * a plan (confirmed first, because it runs the model), then decide on it
 * once. A decision whose outcome is still open locks the card to retrying
 * that SAME decision; a suggestion that is no longer valid clears with a
 * "consolidate again" notice; a memory the agent cannot consolidate is
 * offered disabled, not hidden. The agent's own reason categories are
 * operator-facing and stay off the page.
 */

const TARGET_LABELS: Record<DreamTarget, string> = {
  user_model: "Facts about you",
  project_memory: "Project memory",
};

const STILL_WORKING =
  "The agent is still working on that. Press Try again to see the result.";
const NO_LONGER_VALID =
  "That suggestion is no longer valid, so nothing changed. Consolidate again for a new one.";
const CANNOT_SUGGEST =
  "Consolidation isn't available for this memory right now.";
const CANNOT_APPLY =
  "Suggestions for this memory can be viewed but not applied.";
const DECISION_RUNNING = "Waiting for the other decision to finish.";

/** The result as one line: what merged, then anything left out. */
export function describeDreamReceipt(receipt: DreamReceipt): string {
  const noun = receipt.planned === 1 ? "memory" : "memories";
  const parts = [`${receipt.applied} of ${receipt.planned} ${noun} merged`];
  if (receipt.conflicted > 0) parts.push(`${receipt.conflicted} left alone`);
  if (receipt.skipped > 0) parts.push(`${receipt.skipped} skipped`);
  if (receipt.failed > 0) parts.push(`${receipt.failed} failed`);
  return parts.join(" · ");
}

const errorMessage = (caught: unknown) =>
  caught instanceof Error ? caught.message : String(caught);

/** A dismissal changes nothing, so it carries no per-memory outcome. */
const isDismissed = (receipt: DreamReceipt) =>
  receipt.disposition === "dismiss" || receipt.disposition === "dismissed";

const plural = (count: number, one: string, many: string) =>
  `${count} ${count === 1 ? one : many}`;

export function ConsolidateMemoryCard() {
  const { connected, serverCapabilities } = useRuntimeStatus();
  const manualDream = serverCapabilities.manual_dream;

  const targets = useMemo(() => listDreamTargets(manualDream), [manualDream]);

  // null = "not chosen": the first memory that can be consolidated, else the first.
  const [chosenTarget, setChosenTarget] = useState<DreamTarget | null>(null);
  const [plan, setPlan] = useState<DreamPlan | null>(null);
  const [receipt, setReceipt] = useState<DreamReceipt | null>(null);
  const [isGenerating, setIsGenerating] = useState(false);
  const [isDeciding, setIsDeciding] = useState(false);
  // The decision whose outcome is still open; only that decision may be
  // retried while it is set (never automatically).
  const [pendingDecision, setPendingDecision] = useState<DreamDecision | null>(
    null,
  );
  const [notice, setNotice] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [confirmGenerate, setConfirmGenerate] = useState(false);
  const [confirmApply, setConfirmApply] = useState(false);

  // Absent capability → the card hides entirely (an older agent).
  if (targets.length === 0) return null;

  const effectiveTarget: DreamTarget =
    chosenTarget && targets.includes(chosenTarget)
      ? chosenTarget
      : (targets.find(
          (candidate) => dreamTargetCapability(manualDream, candidate).generate,
        ) ?? targets[0]);
  const capability = dreamTargetCapability(manualDream, effectiveTarget);

  const generate = async () => {
    setConfirmGenerate(false);
    setIsGenerating(true);
    setError(null);
    setNotice(null);
    setReceipt(null);
    setPlan(null);
    setPendingDecision(null);
    try {
      setPlan(await generateDreamPlan(effectiveTarget));
    } catch (caught) {
      setError(errorMessage(caught));
    } finally {
      setIsGenerating(false);
    }
  };

  const decide = async (decision: DreamDecision) => {
    if (!plan) return;
    setIsDeciding(true);
    setError(null);
    setNotice(null);
    try {
      setReceipt(await decideDreamPlan(plan.id, decision));
      setPlan(null);
      setPendingDecision(null);
    } catch (caught) {
      if (isDreamInProgress(caught)) {
        // Outcome still open: keep the suggestion, lock to this same decision.
        setPendingDecision(decision);
        setNotice(STILL_WORKING);
      } else if (isStaleDreamPlan(caught)) {
        // Vanished, expired, or a conflicting/terminal decision won.
        setPlan(null);
        setPendingDecision(null);
        setNotice(NO_LONGER_VALID);
      } else {
        // Unknown failure: the suggestion stays reviewable; an already-open
        // decision stays locked until a terminal answer arrives.
        setError(errorMessage(caught));
      }
    } finally {
      setIsDeciding(false);
    }
  };

  const generateBlocked = capability.generate ? undefined : CANNOT_SUGGEST;
  const applyBlocked =
    pendingDecision === "dismiss"
      ? DECISION_RUNNING
      : capability.decide
        ? undefined
        : CANNOT_APPLY;
  const dismissBlocked =
    pendingDecision === "apply" ? DECISION_RUNNING : undefined;

  const expiresIn =
    plan && plan.expiresAtUnix > 0
      ? formatUntilTime(plan.expiresAtUnix * 1000)
      : "";

  return (
    <SettingsCard
      title="Consolidate memory"
      description="Merge duplicate or overlapping memories. Nothing changes until you approve."
    >
      <div className="space-y-3">
        {!plan && (
          <div className="flex flex-wrap items-center gap-2">
            {targets.length > 1 ? (
              <Select
                value={effectiveTarget}
                onValueChange={(value) => setChosenTarget(value as DreamTarget)}
              >
                <SelectTrigger
                  className="w-56"
                  aria-label="Memory to consolidate"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {targets.map((value) => {
                    const canGenerate = dreamTargetCapability(
                      manualDream,
                      value,
                    ).generate;
                    return (
                      <SelectItem
                        key={value}
                        value={value}
                        disabled={!canGenerate}
                      >
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
              size="sm"
              disabled={
                !connected ||
                isGenerating ||
                isDeciding ||
                Boolean(generateBlocked)
              }
              title={generateBlocked}
              onClick={() => setConfirmGenerate(true)}
            >
              {isGenerating ? "Reviewing…" : "Consolidate memory"}
            </Button>
          </div>
        )}
        {(!capability.generate || !capability.decide) && (
          <p
            className="text-xs text-muted-foreground"
            data-testid="dream-unavailable"
          >
            {capability.generate ? CANNOT_APPLY : CANNOT_SUGGEST}
          </p>
        )}
        {isGenerating && (
          <p className="text-xs text-muted-foreground">
            The agent is reviewing its memory. This can take a minute.
          </p>
        )}
        {notice && (
          <p
            role="status"
            className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-400"
          >
            {notice}
          </p>
        )}
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}

        {plan && plan.operations.length === 0 && (
          <div className="flex flex-wrap items-center gap-3 rounded-lg border border-dashed px-4 py-3">
            <p className="text-sm text-muted-foreground">
              Nothing to merge. Memory is already tidy.
            </p>
            <Button
              size="sm"
              variant="outline"
              disabled={isDeciding}
              onClick={() => void decide("dismiss")}
            >
              {pendingDecision === "dismiss" ? "Try again" : "OK"}
            </Button>
          </div>
        )}

        {plan && plan.operations.length > 0 && (
          <div className="space-y-3">
            <p className="text-sm" data-testid="dream-plan-summary">
              {plural(
                plan.plannedOperationCount,
                "suggested change",
                "suggested changes",
              )}{" "}
              across {plural(plan.plannedSourceCount, "memory", "memories")}.
              Apply or dismiss them all together.
              {expiresIn && (
                <span className="text-muted-foreground">
                  {" "}
                  Expires in {expiresIn}.
                </span>
              )}
            </p>
            <ul className="space-y-2">
              {plan.operations.map((operation) => (
                <li
                  // A plan is immutable once generated; kind + survivor +
                  // sources identify an operation within it.
                  key={`${operation.kind}:${operation.survivor.key}:${operation.sources
                    .map((source) => source.key)
                    .join(",")}`}
                  className="space-y-2 rounded-lg border p-3"
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant="muted">
                      {operation.kind.replaceAll("_", " ") || "merge"}
                    </Badge>
                    {operation.exactDuplicateEligible && (
                      <Badge variant="outline">exact duplicate</Badge>
                    )}
                    {operation.reason && (
                      <span className="text-xs text-muted-foreground">
                        {operation.reason}
                      </span>
                    )}
                  </div>
                  <div className="space-y-1 text-xs">
                    <p className="font-medium">
                      Keeps{" "}
                      <span className="font-mono">
                        {operation.survivor.key}
                      </span>
                      {operation.sources.length > 0 && (
                        <>
                          {" "}
                          and merges in{" "}
                          {operation.sources.map((source, i) => (
                            <span key={source.key} className="font-mono">
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
                      <p className="text-muted-foreground">
                        {operation.replacement.description}
                      </p>
                    )}
                    <details className="rounded-md border px-2 py-1">
                      <summary className="cursor-pointer select-none text-muted-foreground">
                        Show current values
                      </summary>
                      <dl className="mt-2 space-y-2">
                        <DreamParticipantDetail
                          label="Keeps"
                          participant={operation.survivor}
                        />
                        {operation.sources.map((source) => (
                          <DreamParticipantDetail
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
                size="sm"
                disabled={isDeciding || Boolean(applyBlocked)}
                title={applyBlocked}
                onClick={() =>
                  pendingDecision === "apply"
                    ? void decide("apply")
                    : setConfirmApply(true)
                }
              >
                {pendingDecision === "apply" ? "Try again" : "Apply"}
              </Button>
              <Button
                size="sm"
                variant="outline"
                disabled={isDeciding || Boolean(dismissBlocked)}
                title={dismissBlocked}
                onClick={() => void decide("dismiss")}
              >
                {pendingDecision === "dismiss" ? "Try again" : "Dismiss"}
              </Button>
            </div>
          </div>
        )}

        {receipt && (
          <div
            className="space-y-1 text-sm text-muted-foreground"
            data-testid="dream-receipt"
          >
            <p>
              {isDismissed(receipt)
                ? "Dismissed. Nothing changed."
                : `Done: ${describeDreamReceipt(receipt)}.`}
            </p>
            {!isDismissed(receipt) && receipt.conflicted > 0 && (
              <p>
                Some memories changed while you were reviewing, so they were
                left alone. Consolidate again to review them.
              </p>
            )}
            {!isDismissed(receipt) && receipt.failed > 0 && (
              <p>
                Some memories couldn&apos;t be merged. The counts above are the
                final result.
              </p>
            )}
          </div>
        )}
      </div>

      <AlertDialog open={confirmGenerate} onOpenChange={setConfirmGenerate}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Consolidate memory?</AlertDialogTitle>
            <AlertDialogDescription>
              The agent reads everything it remembers and suggests which entries
              to merge. Nothing changes until you approve.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => void generate()}>
              Continue
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={confirmApply} onOpenChange={setConfirmApply}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Apply these changes?</AlertDialogTitle>
            <AlertDialogDescription>
              The memories are merged exactly as shown. Anything that changed in
              the meantime is left alone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                setConfirmApply(false);
                void decide("apply");
              }}
            >
              Apply
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsCard>
  );
}

/** One memory's current stored value and description — what the change
 *  keeps or merges in. */
function DreamParticipantDetail({
  label,
  participant,
}: {
  label: string;
  participant: DreamParticipant;
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
