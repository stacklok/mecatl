// SPDX-License-Identifier: Apache-2.0

import {
  getRuntimeOptions,
  getSessionDetailOptions,
  getSessionTranscriptOptions,
  listSessionsOptions,
} from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "../../components/ui/dialog";
import { formatDate } from "../../lib/formatters";
import { transcriptAccess } from "../chat/transcript-access";
import { TranscriptView } from "../chat/transcript-dialog";
import { useShortcutSuppression } from "../shortcuts/shortcut-provider";

/**
 * DECISION: A past fire's transcript opens in a read-only dialog over the run
 * log, as in the prototype's schedule detail, instead of sending the user to
 * chat (#1852, #2299). The replay itself is chat's `TranscriptView`, so the
 * schedules page adds no second transcript renderer.
 *
 * SPEC: The dialog keeps the chat inspection dialog's gate, `transcriptAccess`:
 * the session's inventory row must grant `inspect` and `viewTranscript`, and the
 * runtime must be online. Only then does it read the session detail, and the
 * saved transcript after it. A session the gate refuses, or one missing from
 * the inventory, is never read; the dialog shows the daemon's reason instead of
 * a read error. The prototype's dialog skips the gate.
 *
 * SPEC: The session list the BFF serves can be cut short (`complete: false`).
 * A session missing from a cut-short list may exist past its end, so the
 * dialog says that rather than a bare "Transcript unavailable."
 */
export function ScheduleTranscriptDialog({
  firedAt,
  onClose,
  scheduleName,
  sessionId,
}: {
  firedAt: string | null;
  onClose: () => void;
  scheduleName: string;
  sessionId: string;
}) {
  useShortcutSuppression(true);
  const runtime = useQuery(getRuntimeOptions());
  const sessions = useQuery(listSessionsOptions());
  const online = runtime.data?.connection === "online";
  const session = sessions.data?.items.find((item) => item.id === sessionId);
  const access = session
    ? transcriptAccess(session, runtime.data)
    : { readable: false, reason: missingReason(online, sessions.data?.complete !== false) };
  const detail = useQuery({
    ...getSessionDetailOptions({ path: { sessionId } }),
    enabled: access.readable,
  });
  const transcript = useQuery({
    ...getSessionTranscriptOptions({ path: { sessionId } }),
    enabled: detail.isSuccess && access.readable,
  });

  // A disabled read stays pending, so only a read the gate allows can hold the dialog.
  const checking = runtime.isPending || sessions.isPending || (access.readable && detail.isPending);
  const listError = runtime.isError || sessions.isError;

  return (
    <Dialog onOpenChange={(open) => !open && onClose()} open>
      <DialogContent className="flex max-h-[min(85vh,48rem)] flex-col gap-4 overflow-hidden sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm">
            {firedAt ? `${scheduleName} — ${formatDate(firedAt)}` : scheduleName}
          </DialogTitle>
          <DialogDescription className="break-all font-mono text-xs">
            {session?.capabilities.copyId ? `Saved transcript · ${sessionId}` : "Saved transcript"}
          </DialogDescription>
        </DialogHeader>
        <div
          className="-mx-6 flex min-h-0 flex-col gap-4 overflow-y-auto px-6 text-sm"
          data-testid="schedule-transcript-scrollport"
        >
          {checking ? (
            <TranscriptView error={false} onRetry={() => {}} pending />
          ) : listError ? (
            <TranscriptView
              error
              onRetry={() => {
                if (runtime.isError) void runtime.refetch();
                if (sessions.isError) void sessions.refetch();
              }}
              pending={false}
            />
          ) : !access.readable ? (
            <p className="text-muted-foreground" role="status">
              {access.reason}
            </p>
          ) : detail.isError ? (
            <TranscriptView error onRetry={() => void detail.refetch()} pending={false} />
          ) : (
            <TranscriptView
              error={transcript.isError}
              onRetry={() => void transcript.refetch()}
              pending={transcript.isPending}
              transcript={transcript.data}
            />
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

/** Why a session missing from the caller's session list can't be shown. */
function missingReason(online: boolean, listComplete: boolean) {
  if (!online) return "Transcript unavailable while offline.";
  if (!listComplete) {
    return "Transcript unavailable: this run's session is past the end of the session list Studio could read.";
  }
  return "Transcript unavailable.";
}
