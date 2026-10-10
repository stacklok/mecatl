// SPDX-License-Identifier: Apache-2.0

import type { SessionTranscriptResponse } from "@mecatl-studio/contracts";
import { Loader2, RefreshCw } from "lucide-react";
import { Badge } from "../../components/ui/badge";
import { Button } from "../../components/ui/button";

/**
 * The saved-transcript view of the session dialog, ported from the
 * prototype's `transcript-dialog.tsx`: a read-only replay of the session's
 * authoritative transcript, one role badge per turn and an activity line
 * for its tool calls. `SessionInspection` owns the read and its gate.
 */

export const TRANSCRIPT_EMPTY_NOTE =
  "This session's saved log holds no replayable conversation. Runs driven outside the API record only their outcome.";

interface TranscriptEntry {
  key: string;
  role: "user" | "assistant" | "event";
  text: string;
}

function transcriptEntries(transcript: SessionTranscriptResponse): TranscriptEntry[] {
  const entries: TranscriptEntry[] = [];
  transcript.messages.forEach((message, index) => {
    const calls = message.toolCalls.length;
    if (calls) {
      entries.push({
        key: `${index}-calls`,
        role: "event",
        text: `${calls} tool call${calls === 1 ? "" : "s"}`,
      });
    }
    if ((message.role === "user" || message.role === "assistant") && message.text) {
      entries.push({ key: `${index}-text`, role: message.role, text: message.text });
    }
  });
  return entries;
}

export function TranscriptView({
  error,
  onRetry,
  pending,
  transcript,
}: {
  error: boolean;
  onRetry: () => void;
  pending: boolean;
  transcript?: SessionTranscriptResponse;
}) {
  if (pending) {
    return (
      <p className="flex items-center gap-2 text-muted-foreground" role="status">
        <Loader2 aria-hidden="true" className="size-4 animate-spin" />
        Loading saved transcript…
      </p>
    );
  }
  if (error) {
    return (
      <div className="flex flex-col items-start gap-2">
        <p className="text-destructive" role="alert">
          Saved transcript could not be read.
        </p>
        <Button onClick={onRetry} size="sm" variant="outline">
          <RefreshCw aria-hidden="true" className="size-3.5" />
          Retry
        </Button>
      </div>
    );
  }
  if (!transcript) return null;
  const entries = transcriptEntries(transcript);
  return (
    <div className="flex flex-col gap-3">
      {!transcript.complete && (
        <p className="rounded-md bg-muted/50 px-3 py-2 text-xs text-muted-foreground" role="status">
          Incomplete saved transcript: earlier turns may be missing.
        </p>
      )}
      {entries.length === 0 ? (
        <p className="text-muted-foreground">{TRANSCRIPT_EMPTY_NOTE}</p>
      ) : (
        <ol className="flex flex-col gap-3">
          {entries.map((entry) => (
            <li className="flex flex-col gap-1" key={entry.key}>
              <Badge
                className="w-fit text-[10px]"
                variant={entry.role === "user" ? "default" : "secondary"}
              >
                {entry.role === "event" ? "activity" : entry.role}
              </Badge>
              <p className="whitespace-pre-wrap break-words">{entry.text}</p>
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}
