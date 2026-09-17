"use client";

import { ChevronRight } from "lucide-react";
import Link from "next/link";
import { useId, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { formatRelativeTime } from "@/lib/formatters";
import {
  approvalBlockedReason,
  getLearningProposal,
  isProposalApprovable,
  type LearningEvidence,
  type LearningProposal,
  PROPOSAL_STATUS_DEFERRED,
  PROPOSAL_STATUS_PROMOTED,
  PROPOSAL_STATUS_REJECTED,
  PROPOSAL_STATUS_STAGED,
} from "@/lib/harness/learning";
import { cn } from "@/lib/utils";

/**
 * One suggestion in the review list: what the agent wants to remember, the
 * status-appropriate actions, and a Details disclosure showing where it
 * came from — a link to each source chat, whether that source is still
 * there, and the redacted excerpt the agent read — plus the learned skill a
 * procedure became.
 *
 * Approve is offered for staged AND deferred proposals (approving a deferred
 * procedure materializes its learned-skill draft), enabled only when every
 * evidence handle still resolves (`isProposalApprovable`); Reject is
 * staged-only, as the agent's store refuses it elsewhere. Opening Details
 * re-reads the proposal so what it shows is current, and says so when the
 * version moved underneath the list.
 */

/** Plain labels over the agent's status tokens; unknown ones read as-is. */
const STATUS_LABELS: Record<string, string> = {
  [PROPOSAL_STATUS_STAGED]: "Pending",
  [PROPOSAL_STATUS_DEFERRED]: "Deferred",
  [PROPOSAL_STATUS_PROMOTED]: "Approved",
  [PROPOSAL_STATUS_REJECTED]: "Rejected",
  undone: "Undone",
};

const statusLabel = (status: string): string =>
  STATUS_LABELS[status] ?? status.replaceAll("_", " ");

export function ProposalRow({
  proposal,
  busy,
  onApprove,
  onReject,
  onUndo,
  onReplace,
}: {
  proposal: LearningProposal;
  busy: boolean;
  onApprove: () => void;
  onReject: () => void;
  onUndo: () => void;
  /** The list swaps in the re-read proposal when Details finds a newer one. */
  onReplace: (updated: LearningProposal) => void;
}) {
  const detailsId = useId();
  const [open, setOpen] = useState(false);
  const [refreshed, setRefreshed] = useState(false);
  const [detailNotice, setDetailNotice] = useState<string | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);

  const updated = formatRelativeTime(proposal.updatedAtUnix * 1000);
  const digest = proposal.value || proposal.body;
  const title = proposal.title || proposal.key || proposal.id;
  const decidable =
    proposal.status === PROPOSAL_STATUS_STAGED ||
    proposal.status === PROPOSAL_STATUS_DEFERRED;
  const approvable = isProposalApprovable(proposal);
  const blockedReason = approvalBlockedReason(proposal);
  const undoTitle = proposal.promotionAvailable
    ? undefined
    : proposal.promotionUnavailableReason ||
      "Undo is not available for this suggestion.";

  const toggle = () => {
    const next = !open;
    setOpen(next);
    if (!next || refreshed) return;
    setRefreshed(true);
    // First open: re-read so the availability flags and version are live.
    getLearningProposal(proposal.id)
      .then((current) => {
        setDetailError(null);
        if (current.version !== proposal.version) {
          setDetailNotice(
            "This suggestion changed since the list was loaded. Review it again before deciding.",
          );
          onReplace(current);
        }
      })
      .catch((caught) => {
        setDetailError(
          `Could not refresh this suggestion: ${
            caught instanceof Error ? caught.message : String(caught)
          }`,
        );
      });
  };

  return (
    <li
      className="space-y-2 rounded-lg border p-3"
      data-proposal-id={proposal.id}
    >
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
      {proposal.description && (
        <p className="text-xs text-muted-foreground">{proposal.description}</p>
      )}
      {digest && (
        <p className="max-h-48 overflow-y-auto rounded-md bg-muted px-3 py-2 text-sm whitespace-pre-wrap text-muted-foreground">
          {digest}
        </p>
      )}
      <div className="flex flex-wrap items-center gap-2 pt-1">
        {decidable && (
          <Button
            size="sm"
            disabled={busy || !approvable}
            title={blockedReason}
            onClick={onApprove}
          >
            Approve
          </Button>
        )}
        {proposal.status === PROPOSAL_STATUS_STAGED && (
          <Button
            size="sm"
            variant="outline"
            disabled={busy}
            onClick={onReject}
          >
            Reject
          </Button>
        )}
        {proposal.status === PROPOSAL_STATUS_PROMOTED && (
          <Button
            size="sm"
            variant="outline"
            disabled={busy || !proposal.promotionAvailable}
            title={undoTitle}
            onClick={onUndo}
          >
            Undo approval
          </Button>
        )}
        {proposal.status !== PROPOSAL_STATUS_STAGED &&
          proposal.status !== PROPOSAL_STATUS_PROMOTED && (
            <Badge variant="muted">{statusLabel(proposal.status)}</Badge>
          )}
        {decidable && !approvable && blockedReason && (
          <span className="text-xs text-muted-foreground">{blockedReason}</span>
        )}
        <button
          type="button"
          aria-expanded={open}
          aria-controls={detailsId}
          onClick={toggle}
          className="ml-auto inline-flex h-8 items-center gap-1 rounded-md px-2 text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <ChevronRight
            className={cn("size-3.5 transition-transform", open && "rotate-90")}
            aria-hidden
          />
          Details
        </button>
      </div>
      {open && (
        <div
          id={detailsId}
          className="space-y-3 border-t pt-3 text-xs"
          data-testid="proposal-details"
        >
          {detailNotice && (
            <p className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-amber-700 dark:text-amber-400">
              {detailNotice}
            </p>
          )}
          {detailError && <p className="text-destructive">{detailError}</p>}
          <section className="space-y-1.5">
            <h4 className="font-medium text-foreground">
              Where this came from
            </h4>
            {proposal.evidence.length === 0 ? (
              <p className="text-muted-foreground">
                No source recorded, so this suggestion can&rsquo;t be approved.
              </p>
            ) : (
              <ul className="space-y-2">
                {proposal.evidence.map((ref) => (
                  <EvidenceRow
                    key={`${ref.sessionId}:${ref.locator}:${ref.ordinal}:${ref.eventSeq}:${ref.digest}`}
                    evidence={ref}
                  />
                ))}
              </ul>
            )}
          </section>
          {proposal.learnedSkillId && (
            <section className="space-y-1">
              <h4 className="font-medium text-foreground">Learned skill</h4>
              <p className="text-muted-foreground">
                <Link
                  href="/workspace/skills?view=learned"
                  className="text-foreground underline-offset-4 hover:underline"
                >
                  View learned skill
                </Link>
              </p>
            </section>
          )}
        </div>
      )}
    </li>
  );
}

/**
 * One source: a link to the chat it came from, whether that source is still
 * available (with the agent's reason when it gives one), and the redacted
 * excerpt the agent read.
 */
function EvidenceRow({ evidence }: { evidence: LearningEvidence }) {
  return (
    <li className="space-y-1 rounded-md border px-2.5 py-2">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-muted-foreground">
        {evidence.sessionId ? (
          <Link
            href={`/workspace/chat/${encodeURIComponent(evidence.sessionId)}`}
            className="text-foreground underline-offset-4 hover:underline"
          >
            View chat
          </Link>
        ) : (
          <span>From a chat</span>
        )}
        <Badge variant={evidence.available ? "success" : "warning"}>
          {evidence.available ? "Available" : "Unavailable"}
          {!evidence.available && evidence.availability
            ? ` (${evidence.availability})`
            : ""}
        </Badge>
      </div>
      {evidence.preview && (
        <pre className="max-h-40 overflow-y-auto rounded-md bg-muted px-2.5 py-1.5 font-mono whitespace-pre-wrap text-muted-foreground">
          {evidence.preview}
        </pre>
      )}
    </li>
  );
}
