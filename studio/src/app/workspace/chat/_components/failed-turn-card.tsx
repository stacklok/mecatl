"use client";

import { AlertCircle, ChevronDown, ChevronRight } from "lucide-react";
import { useId } from "react";
import { useDetailsOpen } from "./use-details-open";

/** The failed bubble's one-line summary is capped here (the TUI's 120 runes). */
const FAILURE_SUMMARY_MAX = 120;

/** Appended to a PERMANENT failure's summary: the identical request is
 *  rejected, so the way forward is a new chat, not a retry. */
export const PERMANENT_FAILURE_SUFFIX =
  " — retrying won't help; the request is rejected. Start a new chat.";

/** The hook's inline annotation on a permanent failure's detail text; the
 *  summary line says the same thing in its own words, so it is stripped. */
const PERMANENT_ANNOTATION =
  " (permanent — retrying the identical request cannot succeed)";

const GENERIC_SUMMARY = "The run failed without a specific error.";
const GENERIC_PERMANENT_SUMMARY = "Permanent provider error";

/**
 * The one-line summary of a failed turn: the detail's first non-empty line,
 * clamped to `FAILURE_SUMMARY_MAX` characters (an ellipsis marks the cut), and
 * for a permanent failure the "retrying won't help" suffix. The full text
 * stays available behind Show details.
 */
export function summarizeFailure(
  detail: string | undefined,
  permanent: boolean,
): string {
  const stripped = (detail ?? "").split(PERMANENT_ANNOTATION).join("");
  const first =
    stripped
      .split(/\r?\n/)
      .map((line) => line.trim())
      .find((line) => line.length > 0) ?? "";
  const chars = [...first];
  const clamped =
    chars.length > FAILURE_SUMMARY_MAX
      ? `${chars.slice(0, FAILURE_SUMMARY_MAX - 1).join("")}…`
      : first;
  const base =
    clamped || (permanent ? GENERIC_PERMANENT_SUMMARY : GENERIC_SUMMARY);
  return permanent ? `${base}${PERMANENT_FAILURE_SUFFIX}` : base;
}

/**
 * The failed-turn block on an assistant bubble: a one-line summary (never a
 * wall of raw provider JSON), with a Show details toggle that reveals the
 * full text under a dim `raw payload:` label when the summary hid any of it.
 */
export function FailedTurnCard({
  detail,
  permanent = false,
}: {
  detail?: string;
  permanent?: boolean;
}) {
  // Collapsed by default; follows the global Expand details preference.
  const [expanded, toggleExpanded] = useDetailsOpen();
  const detailsId = useId();
  const summary = summarizeFailure(detail, permanent);
  const hasDetails = Boolean(detail?.trim()) && detail !== summary;
  return (
    <div
      data-testid="failed-turn-card"
      className="mt-2 flex items-start gap-2 rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2"
    >
      <AlertCircle className="mt-0.5 size-4 shrink-0 text-destructive" />
      <div className="min-w-0 flex-1 text-sm">
        <p className="font-medium text-destructive">
          {permanent ? "This turn failed permanently" : "This turn failed"}
        </p>
        <p className="mt-0.5 break-words text-destructive/90">{summary}</p>
        {hasDetails && (
          <>
            <button
              type="button"
              aria-expanded={expanded}
              aria-controls={detailsId}
              onClick={toggleExpanded}
              className="mt-1 inline-flex items-center gap-1 rounded text-xs text-destructive/80 hover:text-destructive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {expanded ? (
                <ChevronDown className="size-3" aria-hidden="true" />
              ) : (
                <ChevronRight className="size-3" aria-hidden="true" />
              )}
              {expanded ? "Hide details" : "Show details"}
            </button>
            {expanded && (
              <div id={detailsId} className="mt-1">
                <p className="text-[11px] uppercase tracking-wide text-muted-foreground">
                  raw payload:
                </p>
                <pre className="mt-0.5 whitespace-pre-wrap break-words font-mono text-xs text-destructive/90">
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
