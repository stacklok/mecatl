"use client";

import { ExternalLink, KeyRound, Link2, RefreshCw } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { Button } from "@/components/ui/button";
import type { AuthorizationRequest } from "@/features/agent";
import { AUTHORIZATION_POLL_INTERVAL_MS } from "@/features/agent/mcp-authorization-phase";
import { useConfirm } from "@/hooks/use-confirm";

/** The line shown while Studio re-checks the sign-in on the poll cadence. */
export const AUTHORIZATION_POLLING_STATUS = `Waiting for you to finish the sign-in in your browser — checking every ${AUTHORIZATION_POLL_INTERVAL_MS / 1000} seconds.`;

/** The expiry line: a live countdown, or the nudge once it has passed. */
export function formatAuthorizationCountdown(
  expiresAt: number | undefined,
  now: number,
): string | null {
  if (expiresAt === undefined) return null;
  const remaining = Math.max(0, Math.round((expiresAt - now) / 1000));
  if (remaining === 0)
    return "The sign-in window has expired — re-check to confirm.";
  const minutes = Math.floor(remaining / 60);
  const seconds = remaining % 60;
  return minutes > 0
    ? `Expires in ${minutes}m ${seconds}s`
    : `Expires in ${seconds}s`;
}

type Action = "open" | "copy" | "recheck" | "cancel";

/**
 * The takeover card for a run parked on an MCP browser sign-in
 * (authorization.required): it replaces the composer until the sign-in is
 * confirmed or abandoned. Every action goes through the daemon's
 * AUTHORIZATION controls — the URL is fetched live when opened/copied, the
 * re-check streams the continuation, and cancel routes to the authorization
 * (never the parked run's own cancel).
 */
export function AuthorizationPanel({
  authorization,
  onOpen,
  onCopyLink,
  onRecheck,
  onCancel,
}: {
  authorization: AuthorizationRequest;
  onOpen: () => Promise<unknown>;
  onCopyLink: () => Promise<unknown>;
  onRecheck: () => Promise<unknown>;
  onCancel: () => Promise<unknown>;
}) {
  const headingId = useId();
  const [busy, setBusy] = useState<Action | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const { confirm, ConfirmDialog } = useConfirm();

  useEffect(() => {
    if (authorization.expiresAt === undefined) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [authorization.expiresAt]);

  const run = (action: Action, handler: () => Promise<unknown>) => {
    return async () => {
      setBusy(action);
      try {
        await handler();
      } finally {
        setBusy(null);
      }
    };
  };

  const server = authorization.displayName.trim() || "An MCP server";
  const serverInSentence = authorization.displayName.trim() || "the MCP server";
  const countdown = formatAuthorizationCountdown(authorization.expiresAt, now);

  // Cancelling is not a dismissal: the daemon records the parked tool call as
  // cancelled and the model carries on without it, so the click confirms.
  const cancelAfterConfirm = async () => {
    const ok = await confirm({
      title: "Cancel the sign-in?",
      description: `The tool call waiting on ${serverInSentence} is cancelled and the run carries on without it.`,
      confirmText: "Cancel sign-in",
      cancelText: "Keep waiting",
      destructive: true,
    });
    if (!ok) return;
    await onCancel();
  };

  return (
    <section
      aria-labelledby={headingId}
      aria-busy={busy !== null}
      className="my-3 rounded-xl border border-info/30 bg-info/5 p-4"
    >
      <div className="mb-2 flex items-center gap-2">
        <KeyRound className="size-4 text-info" aria-hidden="true" />
        <h2 id={headingId} className="text-sm font-semibold text-info">
          Browser authorization required
        </h2>
      </div>
      <p className="mb-1 text-sm">
        {server} needs you to sign in in your browser to continue this tool
        call.
      </p>
      <p className="mb-3 text-xs text-muted-foreground">
        The run is paused until you finish the sign-in or cancel it.
        {countdown ? ` ${countdown}` : ""}
      </p>
      {authorization.polling ? (
        <p className="mb-3 flex items-center gap-1.5 text-xs text-info">
          <RefreshCw
            aria-hidden="true"
            className="size-3 shrink-0 motion-safe:animate-spin"
          />
          {AUTHORIZATION_POLLING_STATUS}
        </p>
      ) : null}
      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          onClick={run("open", onOpen)}
          disabled={busy !== null}
          className="bg-info text-white hover:bg-info/90"
        >
          <ExternalLink aria-hidden="true" />
          Open sign-in page
        </Button>
        <Button
          size="sm"
          variant="outline"
          onClick={run("copy", onCopyLink)}
          disabled={busy !== null}
          className="border-info/30 hover:bg-info/5"
        >
          <Link2 aria-hidden="true" />
          Copy link
        </Button>
        <Button
          size="sm"
          variant="outline"
          onClick={run("recheck", onRecheck)}
          disabled={busy !== null}
          className="border-info/30 hover:bg-info/5"
        >
          <RefreshCw
            aria-hidden="true"
            className={busy === "recheck" ? "animate-spin" : undefined}
          />
          I've finished — re-check
        </Button>
        <Button
          size="sm"
          variant="ghost"
          onClick={run("cancel", cancelAfterConfirm)}
          disabled={busy !== null}
          className="text-muted-foreground hover:text-foreground"
        >
          Cancel
        </Button>
      </div>
      {ConfirmDialog}
      {authorization.error ? (
        <p role="alert" className="mt-3 text-xs font-medium text-destructive">
          {authorization.error}
        </p>
      ) : authorization.notice ? (
        <p role="status" className="mt-3 text-xs text-muted-foreground">
          {authorization.notice}
        </p>
      ) : null}
    </section>
  );
}
