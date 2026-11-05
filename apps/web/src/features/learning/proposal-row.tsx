// SPDX-License-Identifier: Apache-2.0

/**
 * One suggestion in the review list: what the agent wants to remember, the
 * actions its status allows, and a Details disclosure showing where it came
 * from. Ported from the prototype's `features/learning/proposal-row.tsx`.
 *
 * Studio keeps its own semantics under that layout:
 *
 * - Approve, Reject, and Undo approval ask the list to confirm first; the list
 *   sends the version this row was loaded at.
 * - Approve is offered for a pending suggestion, and for a deferred procedure
 *   (approving it drafts a learned skill), and enabled only when the daemon says
 *   this partition can promote. Sources are shown, never used to gate: only the
 *   daemon's own re-check at decision time is authoritative.
 * - Sources load on demand. Opening Details re-reads the proposal by id, never
 *   from cache, because only that read checks whether each source still
 *   resolves; when the version moved, the row says so and the list takes the
 *   current proposal. A read that the list overtook while it was in flight
 *   never replaces the newer row: the row reads again instead.
 */

import type {
  GetLearningProposalResponse,
  ListLearningProposalsResponse,
} from "@mecatl-studio/contracts/generated";
import { getLearningProposalOptions } from "@mecatl-studio/contracts/query";
import { useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { errorMessage } from "@/lib/error-message";
import { formatRelativeTime } from "@/lib/formatters";
import { cn } from "@/lib/utils";

export type Proposal = ListLearningProposalsResponse["items"][number];
type ProposalDetail = GetLearningProposalResponse;
type Evidence = ProposalDetail["evidence"][number];

/** Plain labels over the daemon's status tokens; unknown ones read as-is. */
const STATUS_LABELS: Record<string, string> = {
  deferred_unsupported: "Deferred",
  promoted: "Approved",
  rejected: "Rejected",
  staged: "Pending",
  undone: "Undone",
};

function statusLabel(status: string): string {
  return STATUS_LABELS[status] ?? status.replaceAll("_", " ");
}

export function proposalTitle(proposal: Proposal): string {
  return proposal.title || proposal.key || proposal.id;
}

/**
 * Whether the daemon accepts an approval in this status: a pending suggestion,
 * or a deferred procedure, which approval turns into a learned-skill draft. A
 * deferred fact cannot be approved; the daemon refuses it.
 */
function isProposalDecidable(proposal: Proposal): boolean {
  return (
    proposal.status === "staged" ||
    (proposal.status === "deferred_unsupported" && proposal.kind === "procedure")
  );
}

/**
 * The plain reason Approve is disabled, or undefined when it is offered. The
 * daemon's own `promotionUnavailableReason` wins when it gives one.
 */
function approvalBlockedReason(proposal: Proposal): string | undefined {
  if (!isProposalDecidable(proposal)) {
    return `A ${statusLabel(proposal.status).toLowerCase()} suggestion cannot be approved.`;
  }
  if (!proposal.promotionAvailable) {
    return proposal.promotionUnavailableReason || "This suggestion has no trusted memory target.";
  }
  return undefined;
}

/**
 * Whether `read` is provably newer than `listed`. Versions are opaque tokens,
 * so only the daemon's update times can order two different versions.
 */
function isNewerProposal(read: Proposal, listed: Proposal): boolean {
  const readAt = Date.parse(read.updatedAt);
  const listedAt = Date.parse(listed.updatedAt);
  return Number.isFinite(readAt) && Number.isFinite(listedAt) && readAt > listedAt;
}

/** True when a decision or undo lost the daemon's version check. */
export function isProposalConflict(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    "code" in error &&
    error.code === "proposal_conflict"
  );
}

export function ProposalRow({
  busy,
  onApprove,
  onReject,
  onReplace,
  onUndo,
  proposal,
}: {
  busy: boolean;
  onApprove: () => void;
  onReject: () => void;
  /** The list swaps in the re-read proposal when Details finds a newer version. */
  onReplace: (current: Proposal) => void;
  onUndo: () => void;
  proposal: Proposal;
}) {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [detail, setDetail] = useState<ProposalDetail>();
  const [loading, setLoading] = useState(false);
  const [detailNotice, setDetailNotice] = useState<string>();
  const [detailError, setDetailError] = useState<string>();
  // The latest listed proposal, for a read that resolves after the list moved.
  const listed = useRef(proposal);
  listed.current = proposal;
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const updatedAt = proposal.updatedAt ? Date.parse(proposal.updatedAt) : Number.NaN;
  const updated = Number.isNaN(updatedAt) ? "" : formatRelativeTime(updatedAt);
  const digest = proposal.value || proposal.body;
  const title = proposalTitle(proposal);
  const decidable = isProposalDecidable(proposal);
  const blockedReason = approvalBlockedReason(proposal);
  const undoReason = proposal.promotionAvailable
    ? undefined
    : proposal.promotionUnavailableReason || "Undo is not available for this suggestion.";

  async function loadDetail(retried: boolean): Promise<void> {
    const base = listed.current;
    try {
      // Never from cache: the app keeps reads fresh for 30 seconds, and an older
      // read would show stale sources and hand the list an older version. Nor
      // from a read still in flight: a remounted row would adopt it, and it
      // started before the list it is checked against.
      const options = getLearningProposalOptions({ path: { proposalId: base.id } });
      await queryClient.cancelQueries({ exact: true, queryKey: options.queryKey });
      const current = await queryClient.fetchQuery({ ...options, staleTime: 0 });
      // An unmounted row's list may have moved on; only a mounted row can check.
      if (!mounted.current) return;
      const latest = listed.current;
      if (
        current.version !== latest.version &&
        latest.version !== base.version &&
        !isNewerProposal(current, latest)
      ) {
        // The list moved while this read was in flight, and the read is not
        // newer: read again rather than show or apply an older version.
        if (!retried) return loadDetail(true);
        setDetailError(
          "This suggestion changed while its sources were loading. Close and reopen Details to load them again.",
        );
        return;
      }
      setDetail(current);
      setDetailError(undefined);
      if (current.version !== latest.version) {
        setDetailNotice(
          "This suggestion changed since the list was loaded. Review it again before deciding.",
        );
        // The list keeps list-shaped items; the sources stay with this row.
        const { evidence: _evidence, ...summary } = current;
        onReplace(summary);
      }
    } catch (caught: unknown) {
      if (mounted.current) {
        setDetailError(`Could not load where this came from: ${errorMessage(caught)}`);
      }
    }
  }

  function toggle() {
    const next = !open;
    setOpen(next);
    if (!next || detail || loading) return;
    // First open: re-read, so the sources' availability and the version are current.
    setLoading(true);
    void loadDetail(false).finally(() => {
      if (mounted.current) setLoading(false);
    });
  }

  return (
    <Collapsible asChild onOpenChange={toggle} open={open}>
      <li className="flex flex-col gap-2 rounded-lg border p-3" data-proposal-id={proposal.id}>
        <div className="flex flex-wrap items-center gap-2">
          {/* Phones give the title its own wrapping line; wider screens truncate it beside the
            badges and disclose the rest in a tooltip. */}
          <Tooltip onlyWhenTruncated>
            <TooltipTrigger asChild>
              <span className="min-w-0 flex-1 truncate text-sm font-medium max-[499px]:basis-full max-[499px]:whitespace-normal">
                {title}
              </span>
            </TooltipTrigger>
            <TooltipContent className="max-w-[min(32rem,calc(100vw-2rem))] break-words">
              {title}
            </TooltipContent>
          </Tooltip>
          {proposal.kind && <Badge variant="muted">{proposal.kind}</Badge>}
          {proposal.projectScoped && <Badge variant="outline">project</Badge>}
          {updated && <span className="text-xs text-muted-foreground">{updated} ago</span>}
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
              className="min-h-11"
              disabled={busy || blockedReason !== undefined}
              onClick={onApprove}
              size="sm"
              title={blockedReason}
            >
              Approve
            </Button>
          )}
          {proposal.status === "staged" && (
            <Button
              className="min-h-11"
              disabled={busy}
              onClick={onReject}
              size="sm"
              variant="outline"
            >
              Reject
            </Button>
          )}
          {proposal.status === "promoted" && (
            <Button
              className="min-h-11"
              disabled={busy || !proposal.promotionAvailable}
              onClick={onUndo}
              size="sm"
              title={undoReason}
              variant="outline"
            >
              Undo approval
            </Button>
          )}
          {proposal.status !== "staged" && proposal.status !== "promoted" && (
            <Badge variant="muted">{statusLabel(proposal.status)}</Badge>
          )}
          {decidable && blockedReason && (
            <span className="text-xs text-muted-foreground">{blockedReason}</span>
          )}
          <CollapsibleTrigger asChild>
            <button
              className="ml-auto inline-flex min-h-11 items-center gap-1 rounded-md px-2 text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              type="button"
            >
              <ChevronRight
                aria-hidden="true"
                className={cn("size-3.5 transition-transform", open && "rotate-90")}
              />
              Details
            </button>
          </CollapsibleTrigger>
        </div>
        <CollapsibleContent
          className="space-y-3 border-t pt-3 text-xs"
          data-testid="proposal-details"
        >
          {detailNotice && (
            <p
              className="rounded-lg border border-warning/40 bg-warning/10 px-3 py-2 text-foreground"
              role="status"
            >
              {detailNotice}
            </p>
          )}
          {detailError && (
            <p className="text-destructive" role="alert">
              {detailError}
            </p>
          )}
          <section className="space-y-1.5">
            <h4 className="font-medium text-foreground">Where this came from</h4>
            {loading ? (
              <p className="text-muted-foreground" role="status">
                Loading sources…
              </p>
            ) : detail ? (
              detail.evidence.length === 0 ? (
                <p className="text-muted-foreground">No source recorded for this suggestion.</p>
              ) : (
                <ul className="space-y-2">
                  {detail.evidence.map((evidence) => (
                    <EvidenceRow
                      evidence={evidence}
                      key={`${evidence.sessionId}:${evidence.locator}:${evidence.ordinal}:${evidence.eventSeq}:${evidence.digest}`}
                    />
                  ))}
                </ul>
              )
            ) : (
              !detailError && (
                <p className="text-muted-foreground">
                  {proposal.evidenceCount === 1
                    ? "1 source recorded."
                    : `${proposal.evidenceCount} sources recorded.`}
                </p>
              )
            )}
          </section>
          {proposal.learnedSkillId && (
            <section className="space-y-1">
              <h4 className="font-medium text-foreground">Learned skill</h4>
              <p className="text-muted-foreground">
                <Link
                  className="text-foreground underline-offset-4 hover:underline"
                  params={{ item: proposal.learnedSkillId, view: "learned" }}
                  to="/workspace/skills/$view/$item"
                >
                  View learned skill
                </Link>
              </p>
            </section>
          )}
        </CollapsibleContent>
      </li>
    </Collapsible>
  );
}

/**
 * One source: a link to the chat it came from, whether the daemon could still
 * read it at the recorded digest (with its reason when not), and the daemon's
 * bounded, redacted excerpt, shown as plain text.
 */
function EvidenceRow({ evidence }: { evidence: Evidence }) {
  return (
    <li className="space-y-1 rounded-md border px-2.5 py-2">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-muted-foreground">
        {evidence.sessionId ? (
          <Link
            className="inline-flex min-h-11 items-center text-foreground underline-offset-4 hover:underline"
            search={{ sessionId: evidence.sessionId }}
            to="/workspace/chat"
          >
            View chat
          </Link>
        ) : (
          <span>From a chat</span>
        )}
        {evidence.protocol && <span>Evidence protocol: {evidence.protocol}</span>}
        {evidence.attachmentUnavailable && <span>Tool association unavailable</span>}
        <Badge variant={evidence.available ? "success" : "warning"}>
          {evidence.available ? "Available" : "Unavailable"}
          {!evidence.available && evidence.availability ? ` (${evidence.availability})` : ""}
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
