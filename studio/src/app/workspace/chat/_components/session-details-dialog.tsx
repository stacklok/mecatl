"use client";

import { Check, Copy, Loader2, RefreshCw } from "lucide-react";
import { type ReactNode, useEffect, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useOptionalRuntimeStatus } from "@/features/agent/runtime-status";
import { copyToClipboard } from "@/lib/clipboard";
import { formatRelativeTime } from "@/lib/formatters";
import {
  fetchHarnessSessionIdentity,
  type HarnessSessionIdentity,
} from "@/lib/harness/sessions";
import type { SessionPermissionMode } from "@/lib/protocol";
import { cn } from "@/lib/utils";

/**
 * Facts the dialog cannot read off the session snapshot: they come from the
 * inventory row the workspace already holds. All optional — a session the
 * list does not (yet) know renders without them.
 */
export interface SessionDetailsExtras {
  /** The row's `copy_id` capability. `false` disables Copy with the reason;
   *  undefined (row unknown) leaves the copy offered. */
  canCopyId?: boolean;
  copyIdReason?: string;
  /** The row's last write, epoch millis; 0/undefined hides the row. */
  updatedAt?: number;
}

/** Shown under a disabled Copy when the daemon gave no closed reason. */
export const COPY_ID_DENIED_FALLBACK =
  "The daemon does not offer this session's ID for copying";

/**
 * The `/session` built-in (also the chat ··· menu's "Session details" and
 * ⌘I): the active session's exact daemon id with a Copy button — the id is
 * what `InspectSubagent`, `resume:` and support asks need verbatim — plus
 * its title and provenance, lifecycle state, kind, permission mode, resolved
 * provider/model/context window, the server-owned placement's DISPLAY
 * metadata (label, branch, revision — never a path, ADR 0291), timestamps,
 * turn/tool-call counts, limits and every relationship the daemon set (the
 * debug target with its own Copy, ADR 0254). Read fresh from the snapshot
 * each time the dialog opens; a failed read offers Retry.
 */
export function SessionDetailsDialog({
  sessionId,
  open,
  onOpenChange,
  extras,
}: {
  sessionId: string | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  extras?: SessionDetailsExtras;
}) {
  const [identity, setIdentity] = useState<HarnessSessionIdentity | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [copied, setCopied] = useState(false);
  const [attempt, setAttempt] = useState(0);

  // biome-ignore lint/correctness/useExhaustiveDependencies: `attempt` is the Retry trigger — bumping it re-runs the read
  useEffect(() => {
    if (!open || !sessionId) return;
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    setIdentity(null);
    setCopied(false);
    fetchHarnessSessionIdentity(sessionId, controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setIdentity(value);
      })
      .catch((caught: unknown) => {
        if (controller.signal.aborted) return;
        setError(caught instanceof Error ? caught.message : String(caught));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [open, sessionId, attempt]);

  const id = identity?.id ?? sessionId ?? "";
  const copyAllowed = extras?.canCopyId !== false;
  const copyDeniedReason = copyAllowed
    ? null
    : extras?.copyIdReason || COPY_ID_DENIED_FALLBACK;

  const copyId = async () => {
    if (await copyToClipboard(id, "Session ID")) setCopied(true);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Session details</DialogTitle>
          <DialogDescription>
            The active chat as the daemon records it.
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-1.5">
          <div className="flex items-start gap-2">
            <code className="min-w-0 flex-1 break-all rounded-md border border-border bg-muted/40 px-2 py-1.5 font-mono text-xs">
              {id}
            </code>
            <Button
              variant="outline"
              size="sm"
              className="h-8 shrink-0"
              onClick={() => void copyId()}
              aria-label="Copy session ID"
              disabled={!copyAllowed}
              aria-describedby={copyDeniedReason ? "copy-id-denied" : undefined}
            >
              {copied ? (
                <Check className="size-3.5" />
              ) : (
                <Copy className="size-3.5" />
              )}
              {copied ? "Copied" : "Copy"}
            </Button>
          </div>
          {copyDeniedReason && (
            <p id="copy-id-denied" className="text-xs text-muted-foreground">
              Copying is unavailable: {copyDeniedReason}
            </p>
          )}
        </div>
        {loading && (
          <p
            className="flex items-center gap-2 text-sm text-muted-foreground"
            role="status"
          >
            <Loader2 className="size-3.5 animate-spin" aria-hidden />
            Loading session details…
          </p>
        )}
        {error && (
          <div className="flex flex-col gap-2">
            <p className="text-sm text-destructive" role="alert">
              Could not load the session: {error}
            </p>
            <Button
              variant="outline"
              size="sm"
              className="h-8 w-fit"
              onClick={() => setAttempt((n) => n + 1)}
            >
              <RefreshCw className="size-3.5" />
              Retry
            </Button>
          </div>
        )}
        {identity && (
          <IdentityRows identity={identity} updatedAt={extras?.updatedAt} />
        )}
      </DialogContent>
    </Dialog>
  );
}

const MODE_LABEL: Record<SessionPermissionMode, string> = {
  default: "Default",
  plan: "Plan",
  acceptEdits: "Accept edits",
};

/**
 * The daemon's lifecycle vocabulary (engine/session state machine) as a label
 * plus a status dot — the first place Studio names every state as text, not
 * only "running" (the sidebar pulse) and "awaiting" (the ask badge).
 */
const LIFECYCLE: Record<string, { label: string; dot: string }> = {
  idle: { label: "Idle", dot: "bg-muted-foreground/60" },
  running: { label: "Running", dot: "bg-brand animate-pulse" },
  awaiting: { label: "Awaiting approval", dot: "bg-warning" },
  completed: { label: "Completed", dot: "bg-success" },
  failed: { label: "Failed", dot: "bg-destructive" },
  cancelled: { label: "Cancelled", dot: "bg-muted-foreground/60" },
};

/** The label the dialog shows for a daemon lifecycle state (verbatim when unknown). */
export function lifecycleLabel(state: string): string {
  return LIFECYCLE[state]?.label ?? (state || "unavailable");
}

/** engine/session/kind.go, in the user's words. */
const KIND_LABEL: Record<string, string> = {
  main: "Main chat",
  debug: "Debug session",
  subagent: "Subagent",
  team_member: "Team member",
  parallel_branch: "Parallel branch",
  scheduled: "Scheduled run",
};

/** engine/session/title.go provenance → the chip beside the title. */
const PROVENANCE_LABEL: Record<string, string> = {
  operator: "Set manually",
  "first-prompt": "From first prompt",
  generated: "Generated",
};

/** A no-filesystem session's placement kinds: the `no-fs` profile/selector
 *  spelling and the environment-ref `nofs` spelling (issue #55). */
const NO_FS_PLACEMENT_KINDS: ReadonlySet<string> = new Set(["no-fs", "nofs"]);

function formatInstant(millis: number): string {
  const relative = formatRelativeTime(millis);
  const absolute = new Date(millis).toLocaleString();
  return relative ? `${absolute} (${relative} ago)` : absolute;
}

function limitText(value: number): string {
  return value > 0 ? value.toLocaleString() : "none";
}

function Row({
  label,
  children,
  mono = false,
}: {
  label: string;
  children: ReactNode;
  mono?: boolean;
}) {
  return (
    <div className="contents">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={cn("min-w-0 break-words", mono && "font-mono text-xs")}>
        {children}
      </dd>
    </div>
  );
}

/** A related session's id with its own Copy (the debug target, ADR 0254). */
function CopyableId({ id, label }: { id: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <span className="flex items-start gap-2">
      <code className="min-w-0 flex-1 break-all font-mono text-xs">{id}</code>
      <Button
        variant="ghost"
        size="sm"
        className="h-6 shrink-0 px-2 text-xs"
        aria-label={`Copy ${label}`}
        onClick={() => {
          void copyToClipboard(id, label).then((ok) => {
            if (ok) setCopied(true);
          });
        }}
      >
        {copied ? <Check className="size-3" /> : <Copy className="size-3" />}
        {copied ? "Copied" : "Copy"}
      </Button>
    </span>
  );
}

function IdentityRows({
  identity,
  updatedAt,
}: {
  identity: HarnessSessionIdentity;
  updatedAt?: number;
}) {
  const state = LIFECYCLE[identity.state];
  const model = identity.resolvedModel;
  const placement = identity.placement;
  // The daemon-level facts the old status strip carried: which daemon this
  // chat runs against (never a URL — the proxy hides the address) and the
  // operator posture it reports. Absent outside the runtime provider.
  const runtime = useOptionalRuntimeStatus();
  const serverText = runtime
    ? runtime.state === "connecting"
      ? "connecting…"
      : runtime.state === "offline"
        ? "offline"
        : `${runtime.mode === "managed" ? "Managed by Studio" : "External deployment"}${runtime.deployment ? ` · ${runtime.deployment}` : ""}`
    : "";
  const posture =
    typeof runtime?.serverCapabilities.posture === "string"
      ? runtime.serverCapabilities.posture
      : "";
  const relationship = identity.relationship;
  const provenance = PROVENANCE_LABEL[identity.titleProvenance];
  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
      <Row label="Title">
        <span className="flex flex-wrap items-center gap-1.5">
          <span className="min-w-0 break-words">
            {identity.title || "Untitled chat"}
          </span>
          {provenance && (
            <Badge variant="muted" className="text-[0.65rem]">
              {provenance}
            </Badge>
          )}
        </span>
      </Row>
      <Row label="State">
        <span className="inline-flex items-center gap-2">
          <span
            aria-hidden
            className={cn(
              "size-2 shrink-0 rounded-full",
              state?.dot ?? "bg-muted-foreground/40",
            )}
          />
          {lifecycleLabel(identity.state)}
        </span>
      </Row>
      {identity.kind && (
        <Row label="Kind">{KIND_LABEL[identity.kind] ?? identity.kind}</Row>
      )}
      <Row label="Permission mode">
        {MODE_LABEL[identity.mode] ?? identity.mode}
      </Row>
      {posture && (
        <Row label="Safety level">
          <span data-testid="session-details-posture">
            {posture.charAt(0).toUpperCase() + posture.slice(1)}
          </span>
        </Row>
      )}
      {serverText && (
        <Row label="Server">
          <span data-testid="session-details-server">{serverText}</span>
        </Row>
      )}
      <Row label="Provider">{model?.providerId || "unavailable"}</Row>
      <Row label="Model">{model?.modelId || "unavailable"}</Row>
      {model?.reasoningEffort && (
        <Row label="Reasoning effort">{model.reasoningEffort}</Row>
      )}
      <Row label="Context window">
        {model?.contextWindow
          ? `${model.contextWindow.toLocaleString()} tokens`
          : "unavailable"}
      </Row>
      {placement &&
        (NO_FS_PLACEMENT_KINDS.has(placement.kind) ? (
          <Row label="Placement">No filesystem</Row>
        ) : (
          <>
            <Row label="Placement">
              {placement.label
                ? `${placement.label}${placement.kind ? ` (${placement.kind})` : ""}`
                : placement.kind}
            </Row>
            {placement.branch && <Row label="Branch">{placement.branch}</Row>}
            {placement.revision && (
              <Row label="Revision" mono>
                {placement.revision}
              </Row>
            )}
          </>
        ))}
      <Row label="Created">
        {identity.createdAtUnix > 0
          ? formatInstant(identity.createdAtUnix * 1000)
          : "unavailable"}
      </Row>
      {updatedAt !== undefined && updatedAt > 0 && (
        <Row label="Last updated">{formatInstant(updatedAt)}</Row>
      )}
      <Row label="Turns">{identity.turns.toLocaleString()}</Row>
      <Row label="Tool calls">{identity.toolCalls.toLocaleString()}</Row>
      {identity.limits && (
        <Row label="Limits">
          {`${limitText(identity.limits.maxTurns)} turns · ${limitText(identity.limits.maxToolCalls)} tool calls · ${limitText(identity.limits.maxConsecutiveFailures)} consecutive failures`}
        </Row>
      )}
      {relationship?.parentSessionId && (
        <Row label="Parent session" mono>
          {relationship.parentSessionId}
        </Row>
      )}
      {relationship?.callId && (
        <Row label="Call" mono>
          {relationship.callId}
        </Row>
      )}
      {relationship?.branchIndex !== undefined && (
        <Row label="Branch index">{relationship.branchIndex}</Row>
      )}
      {relationship?.scheduleName && (
        <Row label="Schedule">{relationship.scheduleName}</Row>
      )}
      {relationship?.teamId && (
        <Row label="Team" mono>
          {relationship.teamId}
        </Row>
      )}
      {relationship?.memberName && (
        <Row label="Member">{relationship.memberName}</Row>
      )}
      {relationship?.originSessionId && (
        <Row label="Origin session" mono>
          {relationship.originSessionId}
        </Row>
      )}
      {relationship?.debugTargetSessionId && (
        <Row label="Debug target">
          <CopyableId
            id={relationship.debugTargetSessionId}
            label="Debug target ID"
          />
        </Row>
      )}
    </dl>
  );
}
