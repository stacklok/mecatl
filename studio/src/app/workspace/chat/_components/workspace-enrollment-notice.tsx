"use client";

import { TriangleAlert } from "lucide-react";
import type { ComponentProps } from "react";
import { Button } from "@/components/ui/button";
import type { WorkspaceEnrollmentView } from "@/features/agent/hooks/use-workspace-enrollment";

/**
 * The workspace-services enrollment notice above the composer — the TUI's
 * "workspace services not connected — /tools-connect to enable protected
 * tools" status line, with its `/tools-connect` and `/tools-cancel` actions
 * as buttons. Amber like `MockProviderNotice`: the chat works, but a class of
 * tools does not until the enrollment completes.
 *
 * Renders nothing when the daemon lacks the capability, the enrollment is
 * connected or not required, the probe has not settled, or the user dismissed
 * the not-connected notice for this chat. Every action is idle-only (the
 * daemon refuses the controls during a run) and single-flight.
 */

export const NOT_IDLE_HINT = "Wait for the current response to finish.";

const NOTICE_CLASS =
  "flex flex-col gap-1.5 rounded-lg border border-amber-500/40 bg-background bg-gradient-to-b from-amber-500/5 to-amber-500/5 px-3 py-2";
const TEXT_CLASS =
  "min-w-0 flex-1 text-sm text-amber-800 dark:text-amber-400 break-words";
const BUTTON_CLASS =
  "h-7 shrink-0 border-amber-500/40 text-amber-800 hover:bg-amber-500/10 dark:text-amber-400";

function NoticeButton({
  disabled,
  title,
  ...props
}: ComponentProps<typeof Button>) {
  return (
    <Button
      size="sm"
      variant="outline"
      className={BUTTON_CLASS}
      disabled={disabled}
      title={title}
      {...props}
    />
  );
}

/** The failed-phase headline: the daemon's status word, plainly. */
function failedHeadline(outcome: string): string {
  return `Connection ${outcome || "failed"}.`;
}

export function WorkspaceEnrollmentNotice({
  enrollment,
}: {
  enrollment: WorkspaceEnrollmentView;
}) {
  const {
    supported,
    phase,
    enrollmentId,
    outcome,
    error,
    busy,
    idle,
    dismissed,
    window: windowState,
  } = enrollment;

  if (!supported) return null;
  if (
    phase === "unknown" ||
    phase === "connected" ||
    phase === "not_required"
  ) {
    return null;
  }
  if (phase === "not_connected" && dismissed) return null;

  const blocked = busy || !idle;
  const hint = idle ? undefined : NOT_IDLE_HINT;

  let text: string;
  let actions: React.ReactNode;
  switch (phase) {
    case "not_connected":
      text =
        "Workspace services aren't connected — protected tools stay unavailable until you connect.";
      actions = (
        <>
          <NoticeButton
            disabled={blocked}
            title={hint}
            onClick={enrollment.connect}
          >
            {busy ? "Connecting…" : "Connect"}
          </NoticeButton>
          <NoticeButton onClick={enrollment.dismiss}>Dismiss</NoticeButton>
        </>
      );
      break;
    case "pending": {
      const cancelButton = enrollmentId ? (
        <NoticeButton
          disabled={blocked}
          title={hint}
          onClick={enrollment.cancel}
        >
          {busy ? "Cancelling…" : "Cancel"}
        </NoticeButton>
      ) : null;
      switch (windowState) {
        case "open":
          text =
            "Finish connecting in the window that opened. Prompts wait until the connection completes or is cancelled.";
          actions = cancelButton;
          break;
        case "blocked":
          text =
            "The browser blocked the connection window. Open it to finish connecting.";
          actions = (
            <>
              <NoticeButton disabled={busy} onClick={enrollment.reopenWindow}>
                Open the connection window
              </NoticeButton>
              {cancelButton}
            </>
          );
          break;
        case "closed":
          text =
            "The connection window closed before the connection finished. Reopen it to finish connecting.";
          actions = (
            <>
              <NoticeButton disabled={busy} onClick={enrollment.reopenWindow}>
                Reopen the connection window
              </NoticeButton>
              {cancelButton}
            </>
          );
          break;
        default:
          text =
            "A workspace-services connection is in progress elsewhere — finish it there, or check whether it completed.";
          actions = (
            <>
              <NoticeButton
                disabled={blocked}
                title={hint}
                onClick={enrollment.connect}
              >
                {busy ? "Checking…" : "Check now"}
              </NoticeButton>
              {cancelButton}
            </>
          );
      }
      break;
    }
    default:
      text = failedHeadline(outcome);
      actions = (
        <>
          <NoticeButton
            disabled={blocked}
            title={hint}
            onClick={enrollment.retry}
          >
            {busy ? "Retrying…" : "Retry"}
          </NoticeButton>
          <NoticeButton
            disabled={blocked}
            title={hint}
            onClick={enrollment.cancel}
          >
            Cancel
          </NoticeButton>
        </>
      );
  }

  return (
    <div
      className={NOTICE_CLASS}
      role="status"
      aria-live="polite"
      data-testid="workspace-enrollment-notice"
      data-phase={phase}
    >
      <div className="flex flex-wrap items-center gap-2">
        <TriangleAlert className="size-4 shrink-0 text-amber-600 dark:text-amber-400" />
        <p className={TEXT_CLASS}>{text}</p>
        {actions}
      </div>
      {error && <p className="text-xs text-destructive break-words">{error}</p>}
    </div>
  );
}
