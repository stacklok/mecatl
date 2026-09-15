"use client";

import { GraduationCap } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
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
import { formatRelativeTime } from "@/lib/formatters";
import { fetchAllSessions } from "@/lib/harness/client";
import {
  decideLearningProposal,
  isProposalConflict,
  type LearningProposal,
  listLearningProposals,
  type ReflectionReceipt,
  reflectHarnessSession,
  undoLearningPromotion,
} from "@/lib/harness/learning";
import { cn } from "@/lib/utils";
import { Note, SettingsCard } from "../_components/settings-card";

/**
 * Settings → Learning: the human half of the daemon's reflection loop
 * (ADR 0109). Pending proposals are reviewed here — approve promotes the
 * daemon-curated digest into memory, reject retires it, undo reverts a
 * promotion. Studio never composes memory content; it only decides on what
 * the daemon staged (the same posture as the read-only Memory panel).
 */

/** Status pills over the daemon's proposal vocabulary ("staged" = pending). */
const PROPOSAL_FILTERS = [
  { value: "staged", label: "Pending" },
  { value: "promoted", label: "Promoted" },
  { value: "rejected", label: "Rejected" },
] as const;
type ProposalFilterValue = (typeof PROPOSAL_FILTERS)[number]["value"];

export default function LearningSettingsPage() {
  const runtime = useRuntimeStatus();
  const proposalsSupported =
    runtime.serverCapabilities.learning_proposals === true;
  const reflectionSupported = runtime.serverCapabilities.reflection === true;

  if (!proposalsSupported && !reflectionSupported) {
    return (
      <div className="rounded-xl border bg-card p-5">
        <div className="flex items-start gap-3">
          <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-muted">
            <GraduationCap className="size-5 text-muted-foreground" />
          </div>
          <div className="min-w-0 space-y-1">
            <h2 className="text-sm font-semibold">
              Learning is not supported by this daemon
            </h2>
            <p className="text-sm text-muted-foreground">
              This daemon reports neither learning proposals nor reflection in
              its capabilities. Enable learning in the daemon&rsquo;s settings
              (learning.mode) to review what the agent wants to remember.
            </p>
          </div>
        </div>
      </div>
    );
  }

  return (
    <>
      {proposalsSupported ? (
        <ProposalQueueCard connected={runtime.connected} />
      ) : (
        <SettingsCard title="Review queue">
          <Note>Learning proposals are not enabled on this daemon.</Note>
        </SettingsCard>
      )}
      {reflectionSupported && <ReflectionCard connected={runtime.connected} />}
    </>
  );
}

/** Approve/undo eligibility is the daemon's call, threaded per proposal. */
function proposalActionTitle(proposal: LearningProposal): string | undefined {
  if (proposal.promotionAvailable) return undefined;
  return (
    proposal.promotionUnavailableReason ||
    "This partition has no trusted memory target."
  );
}

function ProposalQueueCard({ connected }: { connected: boolean }) {
  const [filter, setFilter] = useState<ProposalFilterValue>("staged");
  const [proposals, setProposals] = useState<LearningProposal[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);

  const load = useCallback(
    async (signal?: AbortSignal) => {
      try {
        const page = await listLearningProposals({ status: filter }, signal);
        if (signal?.aborted) return;
        setProposals(page.proposals);
        setError(null);
      } catch (caught) {
        if (signal?.aborted) return;
        setProposals([]);
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
          "That proposal changed since it was loaded — the queue was refreshed. Review it again before deciding.",
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
      title="Review queue"
      description="What the agent wants to remember. Approving promotes the daemon-curated digest into memory; nothing here is free-text."
    >
      <div className="space-y-4">
        <div className="inline-flex items-center gap-0.5 rounded-full bg-muted p-1">
          {PROPOSAL_FILTERS.map((f) => (
            <button
              key={f.value}
              type="button"
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

        {notice && (
          <p className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-400">
            {notice}
          </p>
        )}
        {error && <p className="text-sm text-destructive">{error}</p>}

        {isLoading && connected ? (
          <p className="py-6 text-center text-sm text-muted-foreground">
            Loading proposals…
          </p>
        ) : proposals.length === 0 ? (
          <p className="rounded-lg border border-dashed py-8 text-center text-sm text-muted-foreground">
            {filter === "staged"
              ? "Nothing waiting for review."
              : `No ${filter} proposals.`}
          </p>
        ) : (
          <ul className="space-y-3">
            {proposals.map((proposal) => (
              <ProposalRow
                key={proposal.id}
                proposal={proposal}
                busy={busyId === proposal.id}
                onApprove={() =>
                  act(
                    proposal,
                    () =>
                      decideLearningProposal(
                        proposal.id,
                        "approve",
                        proposal.version,
                      ),
                    "Proposal approved",
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
                    "Proposal rejected",
                  )
                }
                onUndo={() =>
                  act(
                    proposal,
                    () => undoLearningPromotion(proposal.id, proposal.version),
                    "Promotion undone",
                  )
                }
              />
            ))}
          </ul>
        )}
      </div>
    </SettingsCard>
  );
}

/** One proposal: the bounded digest detail plus the status-appropriate actions. */
function ProposalRow({
  proposal,
  busy,
  onApprove,
  onReject,
  onUndo,
}: {
  proposal: LearningProposal;
  busy: boolean;
  onApprove: () => void;
  onReject: () => void;
  onUndo: () => void;
}) {
  const updated = formatRelativeTime(proposal.updatedAtUnix * 1000);
  const digest = proposal.value || proposal.body;
  const title = proposal.title || proposal.key || proposal.id;

  return (
    <li className="space-y-2 rounded-lg border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="min-w-0 flex-1 truncate text-sm font-medium">
          {title}
        </span>
        {proposal.kind && <Badge variant="muted">{proposal.kind}</Badge>}
        {proposal.projectScoped && <Badge variant="outline">project</Badge>}
        {updated && (
          <span className="text-xs text-muted-foreground">{updated} ago</span>
        )}
      </div>
      {proposal.key && proposal.key !== title && (
        <p className="truncate font-mono text-xs text-muted-foreground">
          {proposal.key}
        </p>
      )}
      {proposal.description && (
        <p className="text-xs text-muted-foreground">{proposal.description}</p>
      )}
      {digest && (
        <pre className="max-h-48 overflow-y-auto rounded-md bg-muted px-3 py-2 font-mono text-xs whitespace-pre-wrap text-muted-foreground">
          {digest}
        </pre>
      )}
      <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
        {proposal.evidenceCount > 0 && (
          <span>
            {proposal.evidenceCount} evidence ref
            {proposal.evidenceCount === 1 ? "" : "s"}
          </span>
        )}
        {proposal.triggers.slice(0, 4).map((trigger) => (
          <Badge key={trigger} variant="outline">
            {trigger}
          </Badge>
        ))}
      </div>
      <div className="flex items-center gap-2 pt-1">
        {proposal.status === "staged" && (
          <>
            <Button
              size="sm"
              disabled={busy || !proposal.promotionAvailable}
              title={proposalActionTitle(proposal)}
              onClick={onApprove}
            >
              Approve
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={onReject}
            >
              Reject
            </Button>
          </>
        )}
        {proposal.status === "promoted" && (
          <Button
            size="sm"
            variant="outline"
            disabled={busy || !proposal.promotionAvailable}
            title={proposalActionTitle(proposal)}
            onClick={onUndo}
          >
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

/**
 * Explicit reflection over one completed chat: the daemon re-reads the
 * session and stages proposals from it (which then land in the queue above).
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
      return "The daemon abstained — nothing in that session was worth remembering.";
    }
    const parts = [
      `${receipt.staged} staged`,
      `${receipt.promoted} promoted`,
      `${receipt.conflicted} conflicted`,
    ];
    if (receipt.queued > 0) parts.push(`${receipt.queued} queued`);
    return parts.join(" · ");
  }, [receipt]);

  return (
    <SettingsCard
      title="Reflect on a session"
      description="Asks the daemon to re-read a completed chat and stage anything worth remembering into the review queue."
    >
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <Select value={selected} onValueChange={setSelected}>
            <SelectTrigger className="w-full min-w-0 sm:w-96">
              <SelectValue placeholder="Pick a completed chat…" />
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
            {isReflecting ? "Reflecting…" : "Reflect"}
          </Button>
        </div>
        {sessions.length === 0 && (
          <Note>No completed chats to reflect on yet.</Note>
        )}
        {isReflecting && (
          <p className="text-xs text-muted-foreground">
            Reflection is model-driven and can take a minute — leave this page
            open.
          </p>
        )}
        {summary && (
          <p className="text-sm text-muted-foreground">
            Reflection {receipt?.disposition || "finished"}: {summary}
          </p>
        )}
        {error && <p className="text-sm text-destructive">{error}</p>}
      </div>
    </SettingsCard>
  );
}
