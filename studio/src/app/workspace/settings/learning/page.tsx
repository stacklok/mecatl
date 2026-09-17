"use client";

import { GraduationCap, RotateCw } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import { fetchAllSessions } from "@/lib/harness/client";
import {
  decideLearningProposal,
  isProposalConflict,
  type LearningProposal,
  listLearningProposals,
  PROPOSAL_STATUS_DEFERRED,
  PROPOSAL_STATUS_PROMOTED,
  PROPOSAL_STATUS_REJECTED,
  PROPOSAL_STATUS_STAGED,
  type ReflectionReceipt,
  reflectHarnessSession,
  undoLearningPromotion,
} from "@/lib/harness/learning";
import { cn } from "@/lib/utils";
import { LearningModeSection } from "../_components/learning-mode-section";
import { Note, SettingsCard } from "../_components/settings-card";
import { ProposalRow } from "./_components/proposal-row";

/**
 * Settings → Learning: the human half of the agent's reflection loop
 * (ADR 0109). Pending suggestions are reviewed here — approve promotes the
 * agent-curated digest into memory, reject retires it, undo reverts a
 * promotion. Studio never composes memory content; it only decides on what
 * the agent staged (the same posture as the read-only Memory panel).
 */

/**
 * Status pills over the agent's proposal vocabulary ("staged" = pending;
 * "deferred_unsupported" = a procedure the agent could not save when it was
 * suggested, approvable now as a learned-skill draft; "promoted" = approved
 * and remembered). The value is the exact token the list filter sends.
 */
const PROPOSAL_FILTERS = [
  {
    value: PROPOSAL_STATUS_STAGED,
    label: "Pending",
    empty: "Nothing waiting for review.",
  },
  {
    value: PROPOSAL_STATUS_DEFERRED,
    label: "Deferred",
    empty: "No deferred suggestions.",
  },
  {
    value: PROPOSAL_STATUS_PROMOTED,
    label: "Approved",
    empty: "No approved suggestions.",
  },
  {
    value: PROPOSAL_STATUS_REJECTED,
    label: "Rejected",
    empty: "No rejected suggestions.",
  },
] as const;
type ProposalFilterValue = (typeof PROPOSAL_FILTERS)[number]["value"];

/** One page of the queue per request; Load more appends the next. */
const PROPOSAL_PAGE_SIZE = 50;

export default function LearningSettingsPage() {
  const runtime = useRuntimeStatus();
  const proposalsSupported =
    runtime.serverCapabilities.learning_proposals === true;
  const reflectionSupported = runtime.serverCapabilities.reflection === true;

  const learningSupported = proposalsSupported || reflectionSupported;

  // The mode control renders FIRST whatever the capabilities say: with
  // learning off the agent advertises neither proposals nor reflection, and
  // this card is how the user turns it on.
  return (
    <>
      <LearningModeSection />
      {!learningSupported ? (
        <div className="rounded-xl border bg-card p-5">
          <div className="flex items-start gap-3">
            <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-muted">
              <GraduationCap className="size-5 text-muted-foreground" />
            </div>
            <div className="min-w-0 space-y-1">
              <h2 className="text-sm font-semibold">Learning is off</h2>
              <p className="text-sm text-muted-foreground">
                Turn learning on above, then review what the agent wants to
                remember here.
              </p>
            </div>
          </div>
        </div>
      ) : (
        <>
          {proposalsSupported ? (
            <ProposalQueueCard connected={runtime.connected} />
          ) : (
            <SettingsCard title="Suggestions">
              <Note>Reviewing suggestions is not available right now.</Note>
            </SettingsCard>
          )}
          {reflectionSupported && (
            <ReflectionCard connected={runtime.connected} />
          )}
        </>
      )}
    </>
  );
}

function ProposalQueueCard({ connected }: { connected: boolean }) {
  const [filter, setFilter] = useState<ProposalFilterValue>(
    PROPOSAL_STATUS_STAGED,
  );
  const [proposals, setProposals] = useState<LearningProposal[]>([]);
  /** The agent's cursor for the page after the last one shown; "" = end. */
  const [nextCursor, setNextCursor] = useState("");
  const [isLoading, setIsLoading] = useState(true);
  const [isLoadingMore, setIsLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);

  /** (Re)loads the FIRST page of the current filter, dropping any pages
   *  appended after it — a refresh restarts the walk from the agent's head. */
  const load = useCallback(
    async (signal?: AbortSignal) => {
      try {
        const page = await listLearningProposals(
          { status: filter, limit: PROPOSAL_PAGE_SIZE },
          signal,
        );
        if (signal?.aborted) return;
        setProposals(page.proposals);
        setNextCursor(page.nextCursor);
        setError(null);
      } catch (caught) {
        if (signal?.aborted) return;
        setProposals([]);
        setNextCursor("");
        setError(caught instanceof Error ? caught.message : String(caught));
      } finally {
        if (!signal?.aborted) setIsLoading(false);
      }
    },
    [filter],
  );

  useEffect(() => {
    if (!connected) return;
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [connected, load]);

  const refresh = () => {
    setNotice(null);
    setIsLoading(true);
    void load();
  };

  /** Appends the next page (the agent's cursor); ids already shown are
   *  skipped so a queue that moved between pages never lists one twice. */
  const loadMore = async () => {
    if (!nextCursor || isLoadingMore) return;
    setIsLoadingMore(true);
    try {
      const page = await listLearningProposals({
        status: filter,
        cursor: nextCursor,
        limit: PROPOSAL_PAGE_SIZE,
      });
      setProposals((current) => {
        const seen = new Set(current.map((proposal) => proposal.id));
        return [
          ...current,
          ...page.proposals.filter((proposal) => !seen.has(proposal.id)),
        ];
      });
      setNextCursor(page.nextCursor);
      setError(null);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setIsLoadingMore(false);
    }
  };

  /** Details re-read a proposal; the list shows that current version. */
  const replaceProposal = (updated: LearningProposal) => {
    setProposals((current) =>
      current.map((proposal) =>
        proposal.id === updated.id ? updated : proposal,
      ),
    );
  };

  /**
   * Runs one decision/undo. A 409 proposal_conflict means the proposal
   * changed underneath the review — the honest move is refresh-and-re-review,
   * never a blind retry with the new version.
   */
  const act = async (
    proposal: LearningProposal,
    run: () => Promise<LearningProposal>,
    done: string,
  ) => {
    setBusyId(proposal.id);
    setNotice(null);
    try {
      await run();
      toast.success(done);
      await load();
    } catch (caught) {
      if (isProposalConflict(caught)) {
        setNotice(
          "That suggestion changed since it was loaded, so the list was refreshed. Review it again before deciding.",
        );
        await load();
      } else {
        setError(caught instanceof Error ? caught.message : String(caught));
      }
    } finally {
      setBusyId(null);
    }
  };

  return (
    <SettingsCard
      title="Suggestions"
      description="Things the agent would like to remember."
    >
      <div className="space-y-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="inline-flex items-center gap-0.5 rounded-full bg-muted p-1">
            {PROPOSAL_FILTERS.map((f) => (
              <button
                key={f.value}
                type="button"
                aria-pressed={filter === f.value}
                onClick={() => {
                  setFilter(f.value);
                  setNotice(null);
                }}
                className={cn(
                  "h-7 rounded-full px-3.5 text-sm transition-colors",
                  filter === f.value
                    ? "bg-background font-medium text-foreground shadow-sm"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                {f.label}
              </button>
            ))}
          </div>
          <Button
            type="button"
            variant="ghost"
            size="icon"
            aria-label="Refresh suggestions"
            title="Refresh suggestions"
            disabled={!connected || isLoading}
            onClick={refresh}
          >
            <RotateCw className={cn(isLoading && "animate-spin")} />
          </Button>
        </div>

        {notice && (
          <p className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-400">
            {notice}
          </p>
        )}
        {error && <p className="text-sm text-destructive">{error}</p>}

        {isLoading && connected ? (
          <p className="py-6 text-center text-sm text-muted-foreground">
            Loading suggestions…
          </p>
        ) : proposals.length === 0 ? (
          <p className="rounded-lg border border-dashed py-8 text-center text-sm text-muted-foreground">
            {PROPOSAL_FILTERS.find((f) => f.value === filter)?.empty ??
              "Nothing here."}
          </p>
        ) : (
          <ul className="space-y-3">
            {proposals.map((proposal) => (
              <ProposalRow
                key={proposal.id}
                proposal={proposal}
                busy={busyId === proposal.id}
                onReplace={replaceProposal}
                onApprove={() =>
                  act(
                    proposal,
                    () =>
                      decideLearningProposal(
                        proposal.id,
                        "approve",
                        proposal.version,
                      ),
                    "Suggestion approved",
                  )
                }
                onReject={() =>
                  act(
                    proposal,
                    () =>
                      decideLearningProposal(
                        proposal.id,
                        "reject",
                        proposal.version,
                      ),
                    "Suggestion rejected",
                  )
                }
                onUndo={() =>
                  act(
                    proposal,
                    () => undoLearningPromotion(proposal.id, proposal.version),
                    "Approval undone",
                  )
                }
              />
            ))}
          </ul>
        )}
        {!isLoading && nextCursor !== "" && (
          <div className="flex justify-center">
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={isLoadingMore || !connected}
              onClick={() => void loadMore()}
            >
              {isLoadingMore ? "Loading more…" : "Load more"}
            </Button>
          </div>
        )}
      </div>
    </SettingsCard>
  );
}

/**
 * Explicit reflection over one completed chat: the agent re-reads the
 * session and stages proposals from it (which then land in the list above).
 * Synchronous and model-driven — it can take a minute.
 */
function ReflectionCard({ connected }: { connected: boolean }) {
  const [sessions, setSessions] = useState<{ id: string; title: string }[]>([]);
  const [selected, setSelected] = useState("");
  const [isReflecting, setIsReflecting] = useState(false);
  const [receipt, setReceipt] = useState<ReflectionReceipt | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!connected) return;
    const controller = new AbortController();
    // One inventory page is plenty for a picker; rows arrive newest-first.
    fetchAllSessions(controller.signal, 1)
      .then(({ sessions: rows }) => {
        if (controller.signal.aborted) return;
        setSessions(
          rows
            .filter((row) => row.isChat && row.state === "completed")
            .slice(0, 20)
            .map((row) => ({
              id: row.sessionId,
              title: row.title || row.sessionId,
            })),
        );
      })
      .catch(() => {
        if (!controller.signal.aborted) setSessions([]);
      });
    return () => controller.abort();
  }, [connected]);

  const reflect = async () => {
    if (!selected) return;
    setIsReflecting(true);
    setError(null);
    setReceipt(null);
    try {
      setReceipt(await reflectHarnessSession(selected));
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setIsReflecting(false);
    }
  };

  const summary = useMemo(() => {
    if (!receipt) return null;
    if (receipt.abstained) {
      return "Nothing in that chat was worth remembering.";
    }
    const parts = [`${receipt.staged} to review`];
    if (receipt.promoted > 0) parts.push(`${receipt.promoted} remembered`);
    if (receipt.conflicted > 0) {
      parts.push(`${receipt.conflicted} clashed with existing memory`);
    }
    if (receipt.queued > 0) parts.push(`${receipt.queued} still being checked`);
    return `Done: ${parts.join(" · ")}.`;
  }, [receipt]);

  return (
    <SettingsCard
      title="Learn from a chat"
      description="Pick a finished chat and the agent looks for things worth remembering."
    >
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <Select value={selected} onValueChange={setSelected}>
            <SelectTrigger className="w-full min-w-0 sm:w-96">
              <SelectValue placeholder="Choose a finished chat…" />
            </SelectTrigger>
            <SelectContent>
              {sessions.map((session) => (
                <SelectItem key={session.id} value={session.id}>
                  {session.title}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            size="sm"
            disabled={!selected || isReflecting || !connected}
            onClick={() => void reflect()}
          >
            {isReflecting ? "Looking…" : "Find suggestions"}
          </Button>
        </div>
        {sessions.length === 0 && <Note>No finished chats yet.</Note>}
        {isReflecting && (
          <p className="text-xs text-muted-foreground">
            This can take a minute. Keep this page open.
          </p>
        )}
        {summary && <p className="text-sm text-muted-foreground">{summary}</p>}
        {error && <p className="text-sm text-destructive">{error}</p>}
      </div>
    </SettingsCard>
  );
}
