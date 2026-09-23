// SPDX-License-Identifier: Apache-2.0

import { AlertCircle, ChevronDown, ChevronRight } from "lucide-react";
import { useId } from "react";
import { useDetailsOpen } from "./use-details-open";

/** The card's one-line summary is capped here (an ellipsis marks the cut). */
const SUMMARY_MAX_CHARS = 120;

/** Appended to a permanent failure's summary: the identical request is
 *  rejected, so the way forward is a new chat, not a retry. */
const PERMANENT_SUFFIX = " — retrying will not help; the request is rejected.";

const GENERIC_SUMMARY = "The turn failed without a specific error.";

function clampSummary(summary: string): string {
  const first =
    summary
      .split(/\r?\n/)
      .map((line) => line.trim())
      .find((line) => line.length > 0) ?? "";
  return first.length > SUMMARY_MAX_CHARS ? `${first.slice(0, SUMMARY_MAX_CHARS - 1)}…` : first;
}

/**
 * The failed-turn block under an assistant message: a clamped one-line
 * summary (never a wall of raw provider text), worded for whether retrying
 * could help, with a "Show details" toggle revealing the full raw payload.
 */
export function FailedTurnCard({
  detail,
  permanent,
  summary,
}: {
  detail?: string;
  permanent: boolean;
  summary: string;
}) {
  const [expanded, toggleExpanded] = useDetailsOpen();
  const detailsId = useId();
  const clamped = clampSummary(summary) || GENERIC_SUMMARY;
  const text = permanent ? `${clamped}${PERMANENT_SUFFIX}` : clamped;
  const hasDetails = Boolean(detail?.trim()) && detail !== summary;

  return (
    <div className="mt-2 flex items-start gap-2 rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2">
      <AlertCircle aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-destructive" />
      <div className="min-w-0 flex-1 text-sm">
        <p className="font-medium text-foreground">
          {permanent ? "This turn failed permanently" : "This turn failed"}
        </p>
        <p className="mt-0.5 break-words text-foreground">{text}</p>
        {!permanent && <p className="mt-0.5 text-xs text-foreground">Retrying may succeed.</p>}
        {hasDetails && (
          <>
            <button
              aria-controls={detailsId}
              aria-expanded={expanded}
              className="mt-1 inline-flex items-center gap-1 rounded text-xs text-foreground hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              onClick={toggleExpanded}
              type="button"
            >
              {expanded ? (
                <ChevronDown aria-hidden="true" className="size-3" />
              ) : (
                <ChevronRight aria-hidden="true" className="size-3" />
              )}
              {expanded ? "Hide details" : "Show details"}
            </button>
            {expanded && (
              <div className="mt-1" id={detailsId}>
                <p className="text-[11px] uppercase tracking-wide text-muted-foreground">
                  Raw payload
                </p>
                <pre className="mt-0.5 whitespace-pre-wrap break-words font-mono text-xs text-foreground">
                  {detail}
                </pre>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}
