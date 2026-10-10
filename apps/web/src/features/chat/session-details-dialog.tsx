// SPDX-License-Identifier: Apache-2.0

import type {
  RuntimeResponse,
  SessionDetailResponse,
  SessionSummaryResponse,
} from "@mecatl-studio/contracts";
import { Bug, Check, Copy, Loader2, RefreshCw } from "lucide-react";
import { type ReactNode, useState } from "react";
import { Badge } from "../../components/ui/badge";
import { Button } from "../../components/ui/button";
import { copyToClipboard } from "../../lib/clipboard";
import { formatRelativeTime } from "../../lib/formatters";
import { cn } from "../../lib/utils";

/**
 * The details view of the session dialog, ported from the prototype's
 * `session-details-dialog.tsx`. It is a view over `SessionInspection`'s
 * data and rules: the caller owns every query, the capability gates, and
 * the debug hand-off; this file only lays the facts out.
 */

/** Shown under a disabled Copy when the daemon gave no closed reason. */
export const COPY_ID_DENIED_FALLBACK = "ID copying unavailable.";

/** The daemon's kind, in the user's words; an unknown kind stays verbatim. */
const KIND_LABEL: Record<string, string> = {
  debug: "Debug session",
  main: "Main chat",
  parallel_branch: "Parallel branch",
  scheduled: "Scheduled run",
  subagent: "Subagent",
  team_member: "Team member",
};

/** A status dot per known lifecycle word. The word itself stays the daemon's. */
const STATE_DOT: Record<string, string> = {
  awaiting: "bg-warning",
  cancelled: "bg-muted-foreground/60",
  completed: "bg-success",
  failed: "bg-destructive",
  idle: "bg-muted-foreground/60",
  running: "bg-brand animate-pulse",
};

const PROVENANCE_LABEL: Record<string, string> = {
  "first-prompt": "From first prompt",
  generated: "Generated",
  operator: "Set manually",
};

const MODE_LABEL: Record<string, string> = {
  acceptEdits: "Accept edits",
  default: "Default",
  plan: "Plan",
};

/** A no-filesystem session's placement kinds. */
const NO_FS_PLACEMENT_KINDS: ReadonlySet<string> = new Set(["no-fs", "nofs"]);

function formatInstant(iso: string): string {
  const millis = Date.parse(iso);
  if (!iso || Number.isNaN(millis)) return "Unavailable";
  const relative = formatRelativeTime(millis);
  const absolute = new Date(millis).toLocaleString();
  return relative ? `${absolute} (${relative} ago)` : absolute;
}

function Row({
  children,
  label,
  mono = false,
}: {
  children: ReactNode;
  label: string;
  mono?: boolean;
}) {
  return (
    <div className="contents">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={cn("min-w-0 break-words", mono && "font-mono text-xs")}>{children}</dd>
    </div>
  );
}

function CopyableId({ id, label }: { id: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <span className="flex items-start gap-2">
      <code className="min-w-0 flex-1 break-all font-mono text-xs">{id}</code>
      <Button
        aria-label={`Copy ${label}`}
        className="h-6 shrink-0 px-2 text-xs"
        onClick={() => {
          void copyToClipboard(id, label).then((ok) => {
            if (ok) setCopied(true);
          });
        }}
        size="sm"
        variant="ghost"
      >
        {copied ? (
          <Check aria-hidden="true" className="size-3" />
        ) : (
          <Copy aria-hidden="true" className="size-3" />
        )}
        {copied ? "Copied" : "Copy"}
      </Button>
    </span>
  );
}

export function SessionDetailsView({
  canInspect,
  detail,
  detailError,
  detailPending,
  inspectUnavailable,
  notes,
  onDebug,
  onRetry,
  runtime,
  session,
}: {
  canInspect: boolean;
  detail?: SessionDetailResponse;
  detailError: boolean;
  detailPending: boolean;
  /** Why the details cannot be read, shown instead of them. */
  inspectUnavailable: string;
  /** Why each other view is unavailable here, one plain sentence each. */
  notes: string[];
  /** The Debug with AI hand-off; absent when debugging is unavailable (its note says why). */
  onDebug?: () => void;
  onRetry: () => void;
  runtime?: RuntimeResponse;
  session: SessionSummaryResponse;
}) {
  const [copied, setCopied] = useState(false);
  const copyAllowed = session.capabilities.copyId;
  const copyDeniedReason = copyAllowed
    ? ""
    : session.capabilities.copyIdReason || COPY_ID_DENIED_FALLBACK;
  const model = detail?.model;
  const placement = detail?.placement;
  const provenance = PROVENANCE_LABEL[session.titleProvenance];
  const debugTarget =
    detail?.relationship?.debugTargetSessionId || session.debugTargetSessionId || "";
  const posture = runtime?.capabilities.posture ?? "";

  return (
    <>
      <div className="flex flex-col gap-1.5">
        <div className="flex items-start gap-2">
          <code
            className={cn(
              "min-w-0 flex-1 break-all rounded-md border border-border bg-muted/40 px-2 py-1.5 font-mono text-xs",
              !copyAllowed && "text-muted-foreground",
            )}
          >
            {copyAllowed ? session.id : "Unavailable"}
          </code>
          <Button
            aria-describedby={copyDeniedReason ? "copy-id-denied" : undefined}
            aria-label="Copy session ID"
            className="h-8 shrink-0"
            disabled={!copyAllowed}
            onClick={() => {
              void copyToClipboard(session.id, "Session ID").then((ok) => {
                if (ok) setCopied(true);
              });
            }}
            size="sm"
            variant="outline"
          >
            {copied ? (
              <Check aria-hidden="true" className="size-3.5" />
            ) : (
              <Copy aria-hidden="true" className="size-3.5" />
            )}
            {copied ? "Copied" : "Copy"}
          </Button>
        </div>
        {copyDeniedReason && (
          <p className="text-xs text-muted-foreground" id="copy-id-denied">
            Copying is unavailable: {copyDeniedReason}
          </p>
        )}
      </div>
      {!canInspect ? (
        <p className="text-muted-foreground" role="status">
          {inspectUnavailable}
        </p>
      ) : detailPending ? (
        <p className="flex items-center gap-2 text-muted-foreground" role="status">
          <Loader2 aria-hidden="true" className="size-3.5 animate-spin" />
          Loading details…
        </p>
      ) : detailError ? (
        <div className="flex flex-col gap-2">
          <p className="text-destructive" role="alert">
            Session details could not be read.
          </p>
          <Button className="h-8 w-fit" onClick={onRetry} size="sm" variant="outline">
            <RefreshCw aria-hidden="true" className="size-3.5" />
            Retry
          </Button>
        </div>
      ) : (
        detail && (
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5">
            <Row label="Title">
              <span className="flex flex-wrap items-center gap-1.5">
                <span className="min-w-0 break-words">{session.title || "Untitled chat"}</span>
                {provenance && (
                  <Badge className="text-[0.65rem]" variant="muted">
                    {provenance}
                  </Badge>
                )}
              </span>
            </Row>
            <Row label="State">
              <span className="inline-flex items-center gap-2">
                <span
                  aria-hidden="true"
                  className={cn(
                    "size-2 shrink-0 rounded-full",
                    STATE_DOT[detail.state] ?? "bg-muted-foreground/40",
                  )}
                />
                {detail.state || "Unavailable"}
              </span>
            </Row>
            {detail.kind && <Row label="Kind">{KIND_LABEL[detail.kind] ?? detail.kind}</Row>}
            <Row label="Permission mode">{MODE_LABEL[detail.mode] ?? detail.mode}</Row>
            {posture && (
              <Row label="Safety level">{posture.charAt(0).toUpperCase() + posture.slice(1)}</Row>
            )}
            <Row label="Provider">{model?.providerId || "Unavailable"}</Row>
            <Row label="Model">{model?.id ?? (session.modelId || "Unavailable")}</Row>
            {model?.reasoningEffort && model.reasoningEffort !== "default" && (
              <Row label="Reasoning effort">{model.reasoningEffort}</Row>
            )}
            {model?.contextWindow && Number(model.contextWindow) > 0 && (
              <Row label="Context window">
                {`${Number(model.contextWindow).toLocaleString()} tokens`}
              </Row>
            )}
            {placement &&
              (NO_FS_PLACEMENT_KINDS.has(placement.kind) ? (
                <Row label="Placement">No filesystem</Row>
              ) : (
                <>
                  <Row label="Placement">
                    {placement.label || placement.kind}
                    {placement.label && placement.kind && (
                      <span className="text-muted-foreground"> ({placement.kind})</span>
                    )}
                  </Row>
                  {placement.branch && <Row label="Branch">{placement.branch}</Row>}
                  {placement.revision && (
                    <Row label="Revision" mono>
                      {placement.revision}
                    </Row>
                  )}
                </>
              ))}
            <Row label="Created">{formatInstant(session.createdAt)}</Row>
            <Row label="Turns">{session.turns.toLocaleString()}</Row>
            <Row label="Input tokens">{detail.usage.inputTokens}</Row>
            <Row label="Output tokens">{detail.usage.outputTokens}</Row>
            <Row label="Cache read tokens">{detail.usage.cacheReadTokens}</Row>
            <Row label="Cache write tokens">{detail.usage.cacheWriteTokens}</Row>
            <Row label="Reasoning tokens">{detail.usage.reasoningTokens}</Row>
            {detail.relationship?.parentSessionId && (
              <Row label="Parent session" mono>
                {detail.relationship.parentSessionId}
              </Row>
            )}
            {debugTarget && (
              <Row label="Debug target">
                <CopyableId id={debugTarget} label="Debug target ID" />
              </Row>
            )}
          </dl>
        )
      )}
      {(notes.length > 0 || onDebug) && (
        <div className="flex flex-col gap-2 border-t pt-3">
          {onDebug && (
            <Button className="h-8 w-fit" onClick={onDebug} size="sm" variant="outline">
              <Bug aria-hidden="true" className="size-3.5" />
              Debug with AI
            </Button>
          )}
          {notes.length > 0 && (
            <ul className="flex flex-col gap-1 text-xs text-muted-foreground">
              {notes.map((note) => (
                <li key={note}>
                  <p>{note}</p>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </>
  );
}
