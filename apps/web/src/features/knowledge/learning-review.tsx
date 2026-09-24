// SPDX-License-Identifier: Apache-2.0

import type {
  ListLearningProposalsResponse,
  ReflectSessionResponse,
} from "@mecatl-studio/contracts/generated";
import {
  decideLearningProposalMutation,
  getRuntimeOptions,
  listLearningProposalsOptions,
  listLearningProposalsQueryKey,
  listSessionsOptions,
  reflectSessionMutation,
  undoLearningPromotionMutation,
} from "@mecatl-studio/contracts/query";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GraduationCap, RotateCcw, Sparkles } from "lucide-react";
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

type Proposal = ListLearningProposalsResponse["items"][number];
type ProposalFilter = "promoted" | "rejected" | "staged";
type PendingProposalAction =
  | { kind: "review"; decision: "approve" | "reject"; proposal: Proposal }
  | { kind: "undo"; proposal: Proposal };

const filters: Array<{ label: string; value: ProposalFilter }> = [
  { label: "Pending", value: "staged" },
  { label: "Promoted", value: "promoted" },
  { label: "Rejected", value: "rejected" },
];

export function LearningReview() {
  const queryClient = useQueryClient();
  const runtime = useQuery(getRuntimeOptions());
  const sessions = useQuery(listSessionsOptions());
  const [filter, setFilter] = useState<ProposalFilter>("staged");
  const [selectedSession, setSelectedSession] = useState("");
  const [notice, setNotice] = useState<string>();
  const [pendingAction, setPendingAction] = useState<PendingProposalAction>();
  const proposals = useQuery(listLearningProposalsOptions({ query: { status: filter } }));
  const decide = useMutation(decideLearningProposalMutation());
  const undo = useMutation(undoLearningPromotionMutation());
  const reflection = useMutation(reflectSessionMutation());
  const completedSessions = useMemo(
    () =>
      (sessions.data?.items ?? []).filter((session) => session.state === "completed").slice(0, 20),
    [sessions.data?.items],
  );

  async function refreshProposals() {
    await queryClient.invalidateQueries({ queryKey: listLearningProposalsQueryKey() });
  }

  async function review(proposal: Proposal, decision: "approve" | "reject") {
    setNotice(undefined);
    try {
      await decide.mutateAsync({
        body: { decision, expectedVersion: proposal.version, reason: "" },
        path: { proposalId: proposal.id },
      });
      setNotice(decision === "approve" ? "Proposal approved." : "Proposal rejected.");
      await refreshProposals();
    } catch (error) {
      if (isProposalConflict(error)) {
        setNotice(
          "That proposal changed since it was loaded. The queue was refreshed; review it again before deciding.",
        );
        await refreshProposals();
      }
    } finally {
      setPendingAction(undefined);
    }
  }

  async function undoPromotion(proposal: Proposal) {
    setNotice(undefined);
    try {
      await undo.mutateAsync({
        body: { expectedVersion: proposal.version },
        path: { proposalId: proposal.id },
      });
      setNotice("Promotion undone.");
      await refreshProposals();
    } catch (error) {
      if (isProposalConflict(error)) {
        setNotice(
          "That proposal changed since it was loaded. The queue was refreshed; review it again before deciding.",
        );
        await refreshProposals();
      }
    } finally {
      setPendingAction(undefined);
    }
  }

  function confirmPendingAction() {
    if (!pendingAction) return;
    if (pendingAction.kind === "review")
      void review(pendingAction.proposal, pendingAction.decision);
    else void undoPromotion(pendingAction.proposal);
  }

  function pendingActionDescription(action: PendingProposalAction): string {
    return action.kind === "review"
      ? reviewConfirmation(action.proposal, action.decision)
      : `Undo the promotion for ${action.proposal.title || action.proposal.key || action.proposal.id}? The daemon will revert the associated memory write.`;
  }

  async function reflect() {
    if (!selectedSession) return;
    setNotice(undefined);
    try {
      const receipt = await reflection.mutateAsync({ path: { sessionId: selectedSession } });
      setNotice(reflectionSummary(receipt));
      await refreshProposals();
    } catch {
      // The generated mutation error is rendered below.
    }
  }

  return (
    <div className="space-y-6">
      <section className="rounded-xl border bg-card p-5">
        <div className="flex items-start gap-3">
          <span className="flex size-10 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground">
            <Sparkles className="size-5" />
          </span>
          <div className="min-w-0 flex-1">
            <h2 className="font-semibold">Learning review queue</h2>
            <p className="mt-1 text-sm text-muted-foreground">
              Review daemon-curated proposals before they become memory. This screen never composes
              memory content.
            </p>
          </div>
        </div>

        <div className="mt-5 inline-flex max-w-full overflow-x-auto rounded-full bg-muted p-1">
          {filters.map((item) => (
            <button
              className={`min-h-11 rounded-full px-4 text-sm focus-visible:outline-2 focus-visible:outline-brand ${filter === item.value ? "bg-background font-medium shadow-sm" : "text-muted-foreground"}`}
              key={item.value}
              onClick={() => {
                setFilter(item.value);
                setNotice(undefined);
              }}
              type="button"
            >
              {item.label}
            </button>
          ))}
        </div>

        {notice && (
          <p className="mt-4 rounded-lg bg-info/10 p-3 text-sm text-foreground">{notice}</p>
        )}
        {(decide.isError || undo.isError) && (
          <p className="mt-4 rounded-lg bg-destructive/10 p-3 text-sm text-destructive">
            {errorMessage(decide.error ?? undo.error)}
          </p>
        )}

        {proposals.isPending ? (
          <QueueState text="Loading proposals…" />
        ) : proposals.isError ? (
          <QueueState error text={errorMessage(proposals.error)} />
        ) : !proposals.data.supported ? (
          <QueueState text={proposals.data.reason} />
        ) : proposals.data.items.length === 0 ? (
          <QueueState
            text={
              filter === "staged" ? "Nothing is waiting for review." : `No ${filter} proposals.`
            }
          />
        ) : (
          <ul className="mt-4 space-y-3">
            {proposals.data.items.map((proposal) => (
              <ProposalCard
                busy={decide.isPending || undo.isPending}
                key={proposal.id}
                onApprove={() =>
                  setPendingAction({ decision: "approve", kind: "review", proposal })
                }
                onReject={() => setPendingAction({ decision: "reject", kind: "review", proposal })}
                onUndo={() => setPendingAction({ kind: "undo", proposal })}
                proposal={proposal}
              />
            ))}
          </ul>
        )}
        {proposals.data && !proposals.data.complete && (
          <p className="mt-3 text-xs text-warning">
            Showing the first 100 proposals in this state.
          </p>
        )}
      </section>

      <section className="rounded-xl border bg-card p-5">
        <div className="flex items-start gap-3">
          <span className="flex size-10 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground">
            <GraduationCap className="size-5" />
          </span>
          <div className="min-w-0 flex-1">
            <h2 className="font-semibold">Reflect on a completed session</h2>
            <p className="mt-1 text-sm text-muted-foreground">
              Ask Mecatl to re-read a finished chat and stage anything worth remembering into the
              review queue.
            </p>
          </div>
        </div>

        {runtime.data && !runtime.data.capabilities.reflection ? (
          <QueueState text="Session reflection is not enabled on this Mecatl deployment." />
        ) : (
          <div className="mt-5 flex flex-col gap-2 sm:flex-row">
            <select
              aria-label="Completed session"
              className="min-h-11 min-w-0 flex-1 rounded-lg border bg-background px-3 text-sm focus-visible:outline-2 focus-visible:outline-brand"
              onChange={(event) => setSelectedSession(event.target.value)}
              value={selectedSession}
            >
              <option value="">Pick a completed chat…</option>
              {completedSessions.map((session) => (
                <option key={session.id} value={session.id}>
                  {session.title || session.id}
                </option>
              ))}
            </select>
            <Button
              className="min-h-11"
              disabled={!selectedSession || reflection.isPending}
              onClick={() => void reflect()}
              variant="action"
            >
              {reflection.isPending ? "Reflecting…" : "Reflect"}
            </Button>
          </div>
        )}
        {runtime.data?.capabilities.reflection &&
          !sessions.isPending &&
          completedSessions.length === 0 && (
            <p className="mt-3 text-xs text-muted-foreground">
              No completed chats are available yet.
            </p>
          )}
        {reflection.isPending && (
          <p className="mt-3 text-xs text-muted-foreground">
            Reflection is model-driven and may take a minute.
          </p>
        )}
        {reflection.isError && (
          <p className="mt-3 text-sm text-destructive">{errorMessage(reflection.error)}</p>
        )}
      </section>

      <AlertDialog
        onOpenChange={(open) => !open && setPendingAction(undefined)}
        open={Boolean(pendingAction)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Confirm this action</AlertDialogTitle>
            <AlertDialogDescription>
              {pendingAction && pendingActionDescription(pendingAction)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="min-h-11">Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="min-h-11"
              disabled={decide.isPending || undo.isPending}
              onClick={confirmPendingAction}
            >
              Confirm
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function ProposalCard({
  busy,
  onApprove,
  onReject,
  onUndo,
  proposal,
}: {
  busy: boolean;
  onApprove: () => void;
  onReject: () => void;
  onUndo: () => void;
  proposal: Proposal;
}) {
  const title = proposal.title || proposal.key || proposal.id;
  const digest = proposal.value || proposal.body;
  const actionReason =
    proposal.promotionUnavailableReason || "This proposal has no trusted memory target.";
  return (
    <li className="rounded-xl border bg-background p-4">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="min-w-0 flex-1 font-medium">{title}</h3>
        {proposal.kind && <Badge variant="muted">{proposal.kind}</Badge>}
        {proposal.projectScoped && <Badge variant="outline">project</Badge>}
      </div>
      {proposal.key && proposal.key !== title && (
        <p className="mt-1 font-mono text-xs text-muted-foreground">{proposal.key}</p>
      )}
      {proposal.description && (
        <p className="mt-2 text-sm text-muted-foreground">{proposal.description}</p>
      )}
      {digest && (
        <pre className="mt-3 max-h-48 overflow-y-auto whitespace-pre-wrap rounded-lg bg-muted p-3 font-mono text-xs leading-5">
          {digest}
        </pre>
      )}
      <div className="mt-3 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
        {proposal.evidenceCount > 0 && <span>{proposal.evidenceCount} evidence references</span>}
        {proposal.triggers.slice(0, 4).map((trigger) => (
          <Badge key={trigger} variant="outline">
            {trigger}
          </Badge>
        ))}
        {proposal.updatedAt && (
          <span className="ml-auto">Updated {formatDate(proposal.updatedAt)}</span>
        )}
      </div>
      <div className="mt-4 flex flex-wrap gap-2">
        {proposal.status === "staged" && (
          <>
            <Button
              className="min-h-11"
              disabled={busy || !proposal.promotionAvailable}
              onClick={onApprove}
              size="sm"
              title={proposal.promotionAvailable ? undefined : actionReason}
              variant="action"
            >
              Approve
            </Button>
            <Button
              className="min-h-11"
              disabled={busy}
              onClick={onReject}
              size="sm"
              variant="outline"
            >
              Reject
            </Button>
          </>
        )}
        {proposal.status === "promoted" && (
          <Button
            className="min-h-11"
            disabled={busy || !proposal.promotionAvailable}
            onClick={onUndo}
            size="sm"
            title={proposal.promotionAvailable ? undefined : actionReason}
            variant="outline"
          >
            <RotateCcw />
            Undo promotion
          </Button>
        )}
        {proposal.status !== "staged" && proposal.status !== "promoted" && (
          <Badge variant="muted">{proposal.status.replaceAll("_", " ")}</Badge>
        )}
      </div>
    </li>
  );
}

function QueueState({ error, text }: { error?: boolean; text: string }) {
  return (
    <p
      className={`mt-4 rounded-xl border border-dashed p-6 text-center text-sm ${error ? "border-destructive/40 text-destructive" : "text-muted-foreground"}`}
    >
      {text}
    </p>
  );
}

export function reflectionSummary(receipt: ReflectSessionResponse) {
  if (receipt.abstained)
    return receipt.message || "Mecatl abstained; nothing in that session was worth remembering.";
  const counts = [
    `${receipt.staged} staged`,
    `${receipt.promoted} promoted`,
    `${receipt.conflicted} conflicted`,
  ];
  if (receipt.queued > 0) counts.push(`${receipt.queued} queued`);
  return `Reflection ${receipt.disposition || "completed"}: ${counts.join(" · ")}.`;
}

function reviewConfirmation(proposal: Proposal, decision: "approve" | "reject") {
  const title = proposal.title || proposal.key || proposal.id;
  return decision === "approve"
    ? `Approve ${title}? Mecatl will promote this daemon-curated digest into memory.`
    : `Reject ${title}? The proposal will be retired without changing memory.`;
}

function isProposalConflict(error: unknown) {
  return (
    typeof error === "object" &&
    error !== null &&
    "code" in error &&
    error.code === "proposal_conflict"
  );
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
