"use client";

import { Loader2 } from "lucide-react";
import { useEffect, useState } from "react";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { ScrollArea } from "@/components/ui/scroll-area";
import { fetchSessionTranscriptMessages } from "@/lib/harness/client";

interface TranscriptEntry {
  role: "user" | "assistant" | "event";
  text: string;
}

/**
 * Read-only view of a finished run's conversation, from the daemon's
 * authoritative transcript endpoint (the store snapshot — it also covers
 * scheduler-tick fires whose conversation never reached the durable event
 * log). This is the payoff for every place the UI says "each run happens in
 * its own session" — the session is actually openable.
 */
export function TranscriptDialog({
  sessionId,
  label,
  onClose,
}: {
  sessionId: string;
  label: string;
  onClose: () => void;
}) {
  const [entries, setEntries] = useState<TranscriptEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    void (async () => {
      try {
        const transcript = await fetchSessionTranscriptMessages(
          sessionId,
          controller.signal,
        );
        if (controller.signal.aborted) return;
        const next: TranscriptEntry[] = [];
        if (!transcript.complete && transcript.messages.length) {
          next.push({
            role: "event",
            text: "This transcript could not be proven complete; earlier turns may be missing.",
          });
        }
        for (const message of transcript.messages) {
          const calls = message.toolCalls.length;
          if (calls) {
            next.push({
              role: "event",
              text: `${calls} tool call${calls === 1 ? "" : "s"}`,
            });
          }
          if (message.role === "user" || message.role === "assistant") {
            if (message.text) {
              next.push({ role: message.role, text: message.text });
            }
          }
        }
        setEntries(next);
      } catch (caught) {
        if (!controller.signal.aborted) {
          setError(caught instanceof Error ? caught.message : String(caught));
        }
      }
    })();
    return () => controller.abort();
  }, [sessionId]);

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm">{label}</DialogTitle>
          <DialogDescription className="break-all font-mono text-xs">
            {sessionId}
          </DialogDescription>
        </DialogHeader>
        <ScrollArea className="max-h-[60vh] pr-3">
          {error ? (
            <p className="py-6 text-sm text-destructive">{error}</p>
          ) : entries === null ? (
            <div className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" />
              Replaying the event log…
            </div>
          ) : entries.length === 0 ? (
            <p className="py-6 text-sm text-muted-foreground">
              This session&rsquo;s durable log holds no replayable conversation
              — runs driven outside the API record only their outcome.
            </p>
          ) : (
            <div className="flex flex-col gap-3 pb-2">
              {entries.map((entry) => (
                <div
                  key={`${entry.role}-${entry.text.slice(0, 32)}-${entry.text.length}`}
                  className="flex flex-col gap-1"
                >
                  <Badge
                    variant={entry.role === "user" ? "default" : "secondary"}
                    className="w-fit text-[10px]"
                  >
                    {entry.role === "event" ? "activity" : entry.role}
                  </Badge>
                  <p className="whitespace-pre-wrap text-sm">
                    {entry.text || "(empty)"}
                  </p>
                </div>
              ))}
            </div>
          )}
        </ScrollArea>
      </DialogContent>
    </Dialog>
  );
}
