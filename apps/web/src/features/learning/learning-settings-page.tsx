// SPDX-License-Identifier: Apache-2.0

/**
 * Settings → Learning, ported from the prototype's
 * `features/learning/learning-settings-page.tsx`: the read-only Learning card
 * first, then the Suggestions queue and the "Learn from a chat" card.
 *
 * The prototype restyled the review queue and reflection without changing what
 * they call, and Studio keeps its semantics under the new layout:
 *
 * - every Approve, Reject, and Undo approval is confirmed first and sends the
 *   version the row was loaded at;
 * - a version conflict refreshes the queue, says so, and is never retried;
 * - the queue pages with the daemon's cursor and keeps the chosen filter;
 * - reflection offers completed chats only, refreshes the queue it feeds, and
 *   explains when it is unsupported or fails.
 */

import type { ReflectSessionResponse } from "@mecatl-studio/contracts/generated";
import {
  decideLearningProposalMutation,
  getRuntimeOptions,
  listLearningProposalsInfiniteOptions,
  listLearningProposalsInfiniteQueryKey,
  listSessionsOptions,
  reflectSessionMutation,
  undoLearningPromotionMutation,
} from "@mecatl-studio/contracts/query";
import {
  type InfiniteData,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { GraduationCap, RotateCw } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
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
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { Note, SettingsCard } from "@/features/settings/settings-card";
import { errorMessage } from "@/lib/error-message";
import { cn } from "@/lib/utils";
import { LearningModeSection } from "./learning-mode-section";
import { isProposalConflict, type Proposal, ProposalRow, proposalTitle } from "./proposal-row";

/**
 * Status pills over the daemon's proposal vocabulary: "staged" is pending,
 * "deferred_unsupported" is a suggestion the daemon could not apply when it
 * was made, and "promoted" is approved and remembered. The value is the exact
 * token the list request sends.
 */
const PROPOSAL_FILTERS = [
  { empty: "Nothing waiting for review.", label: "Pending", value: "staged" },
  { empty: "No deferred suggestions.", label: "Deferred", value: "deferred_unsupported" },
  { empty: "No approved suggestions.", label: "Approved", value: "promoted" },
  { empty: "No rejected suggestions.", label: "Rejected", value: "rejected" },
] as const;
type ProposalFilterValue = (typeof PROPOSAL_FILTERS)[number]["value"];

/** One page of the queue per request; Load more appends the next. */
const PROPOSAL_PAGE_SIZE = 50;

const CONFLICT_NOTICE =
  "That suggestion changed since it was loaded, so the list was refreshed. Review it again before deciding.";

type PendingProposalAction =
  | { kind: "review"; decision: "approve" | "reject"; proposal: Proposal }
  | { kind: "undo"; proposal: Proposal };

export function LearningSettingsPage() {
  const runtime = useQuery(getRuntimeOptions());
  const proposalsSupported = runtime.data?.capabilities.learningProposals === true;
  const reflectionSupported = runtime.data?.capabilities.reflection === true;
  const learningSupported = proposalsSupported || reflectionSupported;

  return (
    <div className="space-y-6">
      <LearningModeSection />
      {!learningSupported ? (
        <div className="rounded-xl border bg-card p-5" role="status">
          <div className="flex items-start gap-3">
            <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-muted">
              <GraduationCap aria-hidden="true" className="size-5 text-muted-foreground" />
            </div>
            <div className="min-w-0 space-y-1">
              <h2 className="text-sm font-semibold">Learning is off</h2>
              <p className="text-sm text-muted-foreground">Learning is off for this agent.</p>
            </div>
          </div>
        </div>
      ) : (
        <>
          {proposalsSupported ? (
            <ProposalQueueCard />
          ) : (
            <SettingsCard title="Suggestions">
              <Note role="status">Reviewing suggestions is not available right now.</Note>
            </SettingsCard>
          )}
          {reflectionSupported ? (
            <ReflectionCard />
          ) : (
            <SettingsCard title="Learn from a chat">
              <Note role="status">Learning from a chat is not available right now.</Note>
            </SettingsCard>
          )}
        </>
      )}
    </div>
  );
}

function ProposalQueueCard() {
  const queryClient = useQueryClient();
  const [filter, setFilter] = useState<ProposalFilterValue>("staged");
  const [notice, setNotice] = useState<string>();
  const [error, setError] = useState<string>();
  const [pendingAction, setPendingAction] = useState<PendingProposalAction>();
  const listOptions = { query: { limit: PROPOSAL_PAGE_SIZE, status: filter } };
  const proposals = useInfiniteQuery({
    ...listLearningProposalsInfiniteOptions(listOptions),
    getNextPageParam: (page) => (page.supported && page.nextCursor ? page.nextCursor : undefined),
    initialPageParam: "",
  });
  const decide = useMutation(decideLearningProposalMutation());
  const undo = useMutation(undoLearningPromotionMutation());
  const busy = decide.isPending || undo.isPending;

  const pages = proposals.data?.pages ?? [];
  const unsupported = pages.find((page) => !page.supported);
  /** Every page in order; an id a moving queue already showed is not listed twice. */
  const items = useMemo(() => {
    const seen = new Set<string>();
    return (proposals.data?.pages ?? [])
      .flatMap((page) => (page.supported ? page.items : []))
      .filter((proposal) => {
        if (seen.has(proposal.id)) return false;
        seen.add(proposal.id);
        return true;
      });
  }, [proposals.data?.pages]);

  async function refreshProposals() {
    await queryClient.invalidateQueries({ queryKey: listLearningProposalsInfiniteQueryKey() });
  }

  /** Refresh restarts the walk from the daemon's first page. */
  function restart() {
    setNotice(undefined);
    setError(undefined);
    void queryClient.resetQueries({
      queryKey: listLearningProposalsInfiniteQueryKey(listOptions),
    });
  }

  /** Details re-read a proposal; the list shows that current version. */
  function replaceProposal(current: Proposal) {
    queryClient.setQueryData<InfiniteData<(typeof pages)[number]>>(
      listLearningProposalsInfiniteQueryKey(listOptions),
      (data) =>
        data && {
          ...data,
          pages: data.pages.map((page) => ({
            ...page,
            items: page.items.map((item) => (item.id === current.id ? current : item)),
          })),
        },
    );
  }

  /**
   * Runs one confirmed decision or undo. A 409 proposal_conflict means the
   * proposal changed underneath the review: refresh and re-review, never a
   * blind retry with the new version.
   */
  async function act(run: () => Promise<unknown>, done: string) {
    setNotice(undefined);
    setError(undefined);
    try {
      await run();
      toast.success(done);
      await refreshProposals();
    } catch (caught) {
      if (isProposalConflict(caught)) {
        setNotice(CONFLICT_NOTICE);
        await refreshProposals();
      } else {
        setError(errorMessage(caught));
      }
    } finally {
      setPendingAction(undefined);
    }
  }

  function confirmPendingAction() {
    if (!pendingAction) return;
    const { proposal } = pendingAction;
    if (pendingAction.kind === "undo") {
      void act(
        () =>
          undo.mutateAsync({
            body: { expectedVersion: proposal.version },
            path: { proposalId: proposal.id },
          }),
        "Approval undone",
      );
      return;
    }
    const { decision } = pendingAction;
    void act(
      () =>
        decide.mutateAsync({
          body: { decision, expectedVersion: proposal.version, reason: "" },
          path: { proposalId: proposal.id },
        }),
      decision === "approve" ? "Suggestion approved" : "Suggestion rejected",
    );
  }

  const emptyText =
    PROPOSAL_FILTERS.find((item) => item.value === filter)?.empty ?? "Nothing here.";

  return (
    <SettingsCard title="Suggestions">
      <div className="space-y-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          {/* Four 44px pills do not fit one phone row at the 18px root size, so on phones
              they share the row in equal columns instead of scrolling sideways. */}
          <ToggleGroup
            aria-label="Suggestion status"
            className="inline-flex max-w-full items-center gap-0.5 rounded-full bg-muted p-1 max-[499px]:grid max-[499px]:w-full max-[499px]:grid-cols-4"
            onValueChange={(next) => {
              // Choosing the selected pill again clears a single toggle group; the filter stays.
              if (!next) return;
              setFilter(next as ProposalFilterValue);
              setNotice(undefined);
              setError(undefined);
            }}
            type="single"
            value={filter}
          >
            {PROPOSAL_FILTERS.map((item) => (
              <ToggleGroupItem
                className="h-auto min-h-11 min-w-0 shrink-0 whitespace-normal rounded-full px-3.5 font-normal text-muted-foreground hover:bg-transparent hover:text-foreground focus-visible:outline-2 focus-visible:outline-brand data-[state=on]:bg-background data-[state=on]:font-medium data-[state=on]:text-foreground data-[state=on]:shadow-sm max-[499px]:px-1"
                key={item.value}
                value={item.value}
              >
                {item.label}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
          <Button
            aria-label="Refresh suggestions"
            className="ml-auto min-h-11 min-w-11"
            disabled={proposals.isFetching}
            onClick={restart}
            size="icon"
            title="Refresh suggestions"
            type="button"
            variant="ghost"
          >
            <RotateCw className={cn(proposals.isFetching && "animate-spin")} />
          </Button>
        </div>

        {notice && (
          <p
            className="rounded-lg border border-warning/40 bg-warning/10 px-3 py-2 text-sm text-foreground"
            role="status"
          >
            {notice}
          </p>
        )}
        {error && (
          <p className="text-sm text-destructive" role="alert">
            {error}
          </p>
        )}

        {proposals.isPending ? (
          <p className="py-6 text-center text-sm text-muted-foreground" role="status">
            Loading suggestions…
          </p>
        ) : proposals.isError && items.length === 0 ? (
          <p className="text-sm text-destructive" role="alert">
            {errorMessage(proposals.error)}
          </p>
        ) : unsupported && items.length === 0 ? (
          <p
            className="rounded-lg border border-dashed py-8 text-center text-sm text-muted-foreground"
            role="status"
          >
            {unsupported.reason}
          </p>
        ) : items.length === 0 ? (
          <p
            className="rounded-lg border border-dashed py-8 text-center text-sm text-muted-foreground"
            role="status"
          >
            {emptyText}
          </p>
        ) : (
          <ul className="space-y-3" data-testid="learning-pending">
            {items.map((proposal) => (
              <ProposalRow
                busy={busy}
                key={proposal.id}
                onApprove={() =>
                  setPendingAction({ decision: "approve", kind: "review", proposal })
                }
                onReject={() => setPendingAction({ decision: "reject", kind: "review", proposal })}
                onReplace={replaceProposal}
                onUndo={() => setPendingAction({ kind: "undo", proposal })}
                proposal={proposal}
              />
            ))}
          </ul>
        )}
        {proposals.isFetchNextPageError && (
          <p className="text-sm text-destructive" role="alert">
            {errorMessage(proposals.error)}
          </p>
        )}
        {proposals.hasNextPage && (
          <div className="flex justify-center">
            <Button
              className="min-h-11"
              disabled={proposals.isFetchingNextPage}
              onClick={() => void proposals.fetchNextPage()}
              size="sm"
              type="button"
              variant="outline"
            >
              {proposals.isFetchingNextPage ? "Loading more…" : "Load more"}
            </Button>
          </div>
        )}
      </div>

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
            <AlertDialogAction className="min-h-11" disabled={busy} onClick={confirmPendingAction}>
              Confirm
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsCard>
  );
}

/**
 * Explicit reflection over one completed chat: the daemon re-reads the session
 * and stages suggestions from it, which then land in the queue above. It is
 * synchronous and model-driven, so it can take a minute.
 */
function ReflectionCard() {
  const queryClient = useQueryClient();
  const sessions = useQuery(listSessionsOptions());
  const [selected, setSelected] = useState("");
  const reflection = useMutation(reflectSessionMutation());
  const [receipt, setReceipt] = useState<ReflectSessionResponse>();
  const [error, setError] = useState<string>();

  const completedSessions = useMemo(
    () =>
      (sessions.data?.items ?? []).filter((session) => session.state === "completed").slice(0, 20),
    [sessions.data?.items],
  );

  async function reflect() {
    if (!selected) return;
    setError(undefined);
    setReceipt(undefined);
    try {
      setReceipt(await reflection.mutateAsync({ path: { sessionId: selected } }));
      await queryClient.invalidateQueries({ queryKey: listLearningProposalsInfiniteQueryKey() });
    } catch (caught) {
      setError(errorMessage(caught));
    }
  }

  const summary = receipt ? reflectionSummary(receipt) : undefined;

  return (
    <SettingsCard title="Learn from a chat">
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <Select onValueChange={setSelected} value={selected}>
            <SelectTrigger
              aria-label="Finished chat"
              className="min-h-11 w-full min-w-0 min-[500px]:w-96"
            >
              <SelectValue placeholder="Choose a finished chat…" />
            </SelectTrigger>
            <SelectContent>
              {completedSessions.map((session) => (
                <SelectItem key={session.id} value={session.id}>
                  {session.title || session.id}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            className="min-h-11"
            disabled={!selected || reflection.isPending}
            onClick={() => void reflect()}
            size="sm"
          >
            {reflection.isPending ? "Looking…" : "Find suggestions"}
          </Button>
        </div>
        {!sessions.isPending && completedSessions.length === 0 && (
          <Note role="status">No finished chats yet.</Note>
        )}
        {reflection.isPending && (
          <p className="text-xs text-muted-foreground" role="status">
            This can take a minute. Keep this page open.
          </p>
        )}
        {summary && (
          <p className="text-sm text-muted-foreground" role="status">
            {summary}
          </p>
        )}
        {error && (
          <p className="text-sm text-destructive" role="alert">
            {error}
          </p>
        )}
      </div>
    </SettingsCard>
  );
}

/**
 * The receipt in plain words; the daemon's own abstention message wins. A
 * receipt that did not complete (queued, duplicate, rate limited) leads with
 * that disposition instead of "Done".
 */
export function reflectionSummary(receipt: ReflectSessionResponse): string {
  if (receipt.abstained) {
    return receipt.message || "Nothing in that chat was worth remembering.";
  }
  const parts = [`${receipt.staged} to review`];
  if (receipt.promoted > 0) parts.push(`${receipt.promoted} remembered`);
  if (receipt.conflicted > 0) parts.push(`${receipt.conflicted} clashed with existing memory`);
  if (receipt.queued > 0) parts.push(`${receipt.queued} still being checked`);
  const disposition = receipt.disposition.replaceAll("_", " ");
  const lead =
    disposition === "" || disposition === "completed"
      ? "Done"
      : `${disposition.charAt(0).toUpperCase()}${disposition.slice(1)}`;
  return `${lead}: ${parts.join(" · ")}.`;
}

function pendingActionDescription(action: PendingProposalAction): string {
  const title = proposalTitle(action.proposal);
  if (action.kind === "undo") {
    return `Undo the approval for ${title}? Mecatl will revert the memory change it made.`;
  }
  if (action.decision === "reject") {
    return `Reject ${title}? The suggestion will be retired without changing memory.`;
  }
  return action.proposal.kind === "procedure"
    ? `Approve ${title}? Mecatl will draft a learned skill from it.`
    : `Approve ${title}? Mecatl will remember this suggestion.`;
}
