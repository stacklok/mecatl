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
 * SPEC: The dialog reads the session detail first, and the saved transcript
 * only after it, and only when the session's inventory row grants
 * `viewTranscript` (with `inspect`, while the runtime is online): the same
 * gate as the chat inspection dialog. The prototype's dialog skips the gate.
 * A refused session shows the daemon's reason instead of a transcript.
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
  const detail = useQuery({
    ...getSessionDetailOptions({ path: { sessionId } }),
    enabled: online,
  });
  const session = sessions.data?.items.find((item) => item.id === sessionId);
  const access = session
    ? transcriptAccess(session, runtime.data)
    : {
        readable: false,
        reason: online ? "Transcript unavailable." : "Transcript unavailable while offline.",
      };
  const transcript = useQuery({
    ...getSessionTranscriptOptions({ path: { sessionId } }),
    enabled: detail.isSuccess && access.readable,
  });

  const checking = runtime.isPending || sessions.isPending || (online && detail.isPending);

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
          ) : detail.isError || sessions.isError || runtime.isError ? (
            <TranscriptView
              error
              onRetry={() => {
                if (runtime.isError) void runtime.refetch();
                if (sessions.isError) void sessions.refetch();
                if (detail.isError) void detail.refetch();
              }}
              pending={false}
            />
          ) : !access.readable ? (
            <p className="text-muted-foreground" role="status">
              {access.reason}
            </p>
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
