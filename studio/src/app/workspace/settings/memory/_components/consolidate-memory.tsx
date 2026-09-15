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
import {
  type DreamPlan,
  type DreamReceipt,
  type DreamTarget,
  decideDreamPlan,
  dreamTargetCapability,
  generateDreamPlan,
  isStaleDreamPlan,
} from "@/lib/harness/dream";
import { SettingsCard } from "../../_components/settings-card";

/**
 * Manual memory consolidation (ADR 0227): the daemon curates a bounded merge
 * plan over its own memory store; the human applies or dismisses the WHOLE
 * plan. This stays inside memory rule 8 — Studio never composes memory
 * content, it only decides on what the daemon proposed.
 *
 * Plan ids are process-local: a daemon restart answers dream_not_found, which
 * renders as "regenerate", never as a retry.
 */

const TARGET_LABELS: Record<DreamTarget, string> = {
  user_model: "User model (facts about you)",
  project_memory: "Project memory",
};

export function ConsolidateMemoryCard() {
  const { connected, serverCapabilities } = useRuntimeStatus();
  const manualDream = serverCapabilities.manual_dream;

  const targets = useMemo(
    () =>
      (Object.keys(TARGET_LABELS) as DreamTarget[]).filter(
        (target) => dreamTargetCapability(manualDream, target).generate,
      ),
    [manualDream],
  );

  const [target, setTarget] = useState<DreamTarget>("user_model");
  const [plan, setPlan] = useState<DreamPlan | null>(null);
  const [receipt, setReceipt] = useState<DreamReceipt | null>(null);
  const [isGenerating, setIsGenerating] = useState(false);
  const [isDeciding, setIsDeciding] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [confirmApply, setConfirmApply] = useState(false);

  // Absent capability → the surface hides entirely (older/leaner daemon).
  if (targets.length === 0) return null;

  const effectiveTarget = targets.includes(target) ? target : targets[0];
  const canDecide = dreamTargetCapability(manualDream, effectiveTarget).decide;

  const generate = async () => {
    setIsGenerating(true);
    setError(null);
    setNotice(null);
    setReceipt(null);
    setPlan(null);
    try {
      setPlan(await generateDreamPlan(effectiveTarget));
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setIsGenerating(false);
    }
  };

  const decide = async (decision: "apply" | "dismiss") => {
    if (!plan) return;
    setIsDeciding(true);
    setError(null);
    try {
      setReceipt(await decideDreamPlan(plan.id, decision));
      setPlan(null);
    } catch (caught) {
      if (isStaleDreamPlan(caught)) {
        // Process-local id: the daemon restarted or the plan aged out.
        setPlan(null);
        setNotice(
          "That plan is no longer valid (the daemon restarted or the plan expired) — generate a new one.",
        );
      } else {
        setError(caught instanceof Error ? caught.message : String(caught));
      }
    } finally {
      setIsDeciding(false);
    }
  };

  return (
    <SettingsCard
      title="Consolidate memory"
      description="The daemon proposes merging duplicate or overlapping memories; nothing changes until you apply its plan."
    >
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          {targets.length > 1 ? (
            <Select
              value={effectiveTarget}
              onValueChange={(value) => setTarget(value as DreamTarget)}
            >
              <SelectTrigger className="w-64">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {targets.map((value) => (
                  <SelectItem key={value} value={value}>
                    {TARGET_LABELS[value]}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          ) : (
            <span className="text-sm text-muted-foreground">
              {TARGET_LABELS[effectiveTarget]}
            </span>
          )}
          <Button
            size="sm"
            disabled={!connected || isGenerating || isDeciding}
            onClick={() => void generate()}
          >
            {isGenerating ? "Generating…" : "Generate plan"}
          </Button>
        </div>
        {isGenerating && (
          <p className="text-xs text-muted-foreground">
            The daemon is reviewing its memories — this can take a minute.
          </p>
        )}
        {notice && (
          <p className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-400">
            {notice}
          </p>
        )}
        {error && <p className="text-sm text-destructive">{error}</p>}

        {plan && plan.operations.length === 0 && (
          <div className="flex flex-wrap items-center gap-3 rounded-lg border border-dashed px-4 py-3">
            <p className="text-sm text-muted-foreground">
              Nothing to consolidate — this memory is already tidy.
            </p>
            <Button
              size="sm"
              variant="outline"
              disabled={isDeciding}
              onClick={() => void decide("dismiss")}
            >
              Done
            </Button>
          </div>
        )}

        {plan && plan.operations.length > 0 && (
          <div className="space-y-3">
            <p className="text-sm">
              {plan.plannedOperationCount} operation
              {plan.plannedOperationCount === 1 ? "" : "s"} over{" "}
              {plan.plannedSourceCount} memor
              {plan.plannedSourceCount === 1 ? "y" : "ies"}. Review, then apply
              or dismiss the whole plan.
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
                          — absorbs{" "}
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
                  </div>
                </li>
              ))}
            </ul>
            <div className="flex items-center gap-2">
              <Button
                size="sm"
                disabled={isDeciding || !canDecide}
                title={
                  canDecide
                    ? undefined
                    : "This daemon does not permit applying plans."
                }
                onClick={() => setConfirmApply(true)}
              >
                Apply plan
              </Button>
              <Button
                size="sm"
                variant="outline"
                disabled={isDeciding}
                onClick={() => void decide("dismiss")}
              >
                Dismiss
              </Button>
            </div>
          </div>
        )}

        {receipt && (
          <p className="text-sm text-muted-foreground">
            {receipt.disposition === "apply"
              ? `Applied: ${receipt.applied} merged, ${receipt.conflicted} conflicted, ${receipt.skipped} skipped, ${receipt.failed} failed.`
              : "Plan dismissed — nothing changed."}
          </p>
        )}
      </div>

      <AlertDialog open={confirmApply} onOpenChange={setConfirmApply}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Apply the consolidation plan?</AlertDialogTitle>
            <AlertDialogDescription>
              The daemon merges the listed memories exactly as shown. Absorbed
              entries are replaced by their survivor; a memory that changed
              since planning is skipped as conflicted, never overwritten.
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
