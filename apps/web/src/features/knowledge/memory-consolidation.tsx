// SPDX-License-Identifier: Apache-2.0

import type {
  DecideMemoryConsolidationPlanResponse,
  GenerateMemoryConsolidationPlanResponse,
} from "@mecatl-studio/contracts/generated";
import {
  decideMemoryConsolidationPlanMutation,
  generateMemoryConsolidationPlanMutation,
  getRuntimeOptions,
  listUserMemoryQueryKey,
} from "@mecatl-studio/contracts/query";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Sparkles } from "lucide-react";
import { useState } from "react";
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

export function MemoryConsolidation() {
  const queryClient = useQueryClient();
  const runtime = useQuery(getRuntimeOptions());
  const generate = useMutation(generateMemoryConsolidationPlanMutation());
  const decide = useMutation(decideMemoryConsolidationPlanMutation());
  const [plan, setPlan] = useState<GenerateMemoryConsolidationPlanResponse>();
  const [receipt, setReceipt] = useState<DecideMemoryConsolidationPlanResponse>();
  const [notice, setNotice] = useState<string>();
  const [confirmApply, setConfirmApply] = useState(false);
  const capability = runtime.data?.capabilities.manualDream?.userModel;

  if (!capability?.generate) return null;

  async function generatePlan() {
    setPlan(undefined);
    setReceipt(undefined);
    setNotice(undefined);
    try {
      setPlan(await generate.mutateAsync({}));
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
      if (decision === "apply")
        await queryClient.invalidateQueries({ queryKey: listUserMemoryQueryKey() });
    } catch (error) {
      if (isStaleMemoryPlan(error)) {
        setPlan(undefined);
        setNotice(
          "That plan expired or belonged to a previous daemon process. Generate a new plan instead of retrying it.",
        );
      }
    } finally {
      setConfirmApply(false);
    }
  }

  const error = generate.error ?? decide.error;
  return (
    <section className="mb-5 rounded-xl border bg-card p-5">
      <div className="flex flex-col justify-between gap-4 sm:flex-row sm:items-start">
        <div className="flex items-start gap-3">
          <span className="flex size-10 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground">
            <Sparkles className="size-5" />
          </span>
          <div>
            <h2 className="font-semibold">Consolidate memory</h2>
            <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
              Mecatl proposes merges for duplicate or overlapping facts. Nothing changes until you
              apply the whole plan.
            </p>
          </div>
        </div>
        <Button
          disabled={generate.isPending || decide.isPending}
          onClick={() => void generatePlan()}
          size="sm"
          variant="outline"
        >
          {generate.isPending ? "Generating…" : "Generate plan"}
        </Button>
      </div>

      {generate.isPending && (
        <p className="mt-4 text-xs text-muted-foreground">
          Mecatl is reviewing memory; this may take a minute.
        </p>
      )}
      {notice && <p className="mt-4 rounded-lg bg-warning/10 p-3 text-sm text-warning">{notice}</p>}
      {error && !notice && <p className="mt-4 text-sm text-destructive">{errorMessage(error)}</p>}

      {plan && plan.operations.length === 0 && (
        <div className="mt-4 flex flex-wrap items-center gap-3 rounded-lg border border-dashed p-4">
          <p className="text-sm text-muted-foreground">
            Nothing needs consolidation; this memory is already tidy.
          </p>
          <Button
            disabled={!capability.decide || decide.isPending}
            onClick={() => void decidePlan("dismiss")}
            size="sm"
            variant="outline"
          >
            Done
          </Button>
        </div>
      )}

      {plan && plan.operations.length > 0 && (
        <div className="mt-5">
          <p className="text-sm">
            {plan.plannedOperationCount} operation{plan.plannedOperationCount === 1 ? "" : "s"} over{" "}
            {plan.plannedSourceCount} memor{plan.plannedSourceCount === 1 ? "y" : "ies"}. Review
            every operation before deciding the whole plan.
          </p>
          {plan.expiresAt && (
            <p className="mt-1 text-xs text-muted-foreground">
              Expires {formatDate(plan.expiresAt)}
            </p>
          )}
          <ul className="mt-3 space-y-3">
            {plan.operations.map((operation) => (
              <li
                className="rounded-xl border bg-background p-4"
                key={`${operation.kind}:${operation.survivor.key}:${operation.sources.map((source) => source.key).join(",")}`}
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
                <p className="mt-3 text-sm font-medium">
                  Keep <span className="font-mono">{operation.survivor.key}</span>
                  {operation.sources.length > 0 && (
                    <> · absorb {operation.sources.map((source) => source.key).join(", ")}</>
                  )}
                </p>
                {operation.replacement.description && (
                  <p className="mt-2 text-xs text-muted-foreground">
                    {operation.replacement.description}
                  </p>
                )}
                {operation.replacement.value && (
                  <pre className="mt-2 max-h-36 overflow-y-auto whitespace-pre-wrap rounded-lg bg-muted p-3 font-mono text-xs leading-5">
                    {operation.replacement.value}
                  </pre>
                )}
              </li>
            ))}
          </ul>
          <div className="mt-4 flex flex-wrap gap-2">
            <Button
              disabled={!capability.decide || decide.isPending}
              onClick={() => setConfirmApply(true)}
              size="sm"
              title={capability.decide ? undefined : capability.unavailableReason}
              variant="action"
            >
              Apply plan
            </Button>
            <Button
              disabled={!capability.decide || decide.isPending}
              onClick={() => void decidePlan("dismiss")}
              size="sm"
              title={capability.decide ? undefined : capability.unavailableReason}
              variant="outline"
            >
              Dismiss
            </Button>
          </div>
        </div>
      )}

      {receipt && (
        <p className="mt-4 text-sm text-muted-foreground">{memoryConsolidationSummary(receipt)}</p>
      )}

      <AlertDialog onOpenChange={setConfirmApply} open={confirmApply}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Apply this consolidation plan?</AlertDialogTitle>
            <AlertDialogDescription>
              Mecatl will merge the listed memories exactly as shown; memories changed since
              planning will be skipped, not overwritten.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction disabled={decide.isPending} onClick={() => void decidePlan("apply")}>
              Apply
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}

export function memoryConsolidationSummary(receipt: DecideMemoryConsolidationPlanResponse) {
  return receipt.disposition === "apply"
    ? `Applied: ${receipt.applied} merged, ${receipt.conflicted} conflicted, ${receipt.skipped} skipped, ${receipt.failed} failed.`
    : "Plan dismissed; memory was not changed.";
}

export function isStaleMemoryPlan(error: unknown) {
  if (typeof error !== "object" || error === null) return false;
  if (
    "code" in error &&
    (error.code === "dream_not_found" || error.code === "dream_terminal_conflict")
  )
    return true;
  return "status" in error && (error.status === 404 || error.status === 410);
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(
    new Date(value),
  );
}

function errorMessage(error: unknown) {
  if (typeof error === "object" && error !== null && "detail" in error) return String(error.detail);
  return error instanceof Error ? error.message : "The request could not be completed.";
}
