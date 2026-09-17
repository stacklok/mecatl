"use client";

import { Loader2 } from "lucide-react";
import { useEffect, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
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
 *
 * A failed load keeps the dialog open with a Retry (the TUI viewer's `r`):
 * the operator never has to close and reopen to try again. `subtitle` names
 * what the session IS (a subagent of…, a fire of schedule…), and
 * `onOpenParent` offers the parent chat when the run has one.
 */
export function TranscriptDialog({
  sessionId,
  label,
  subtitle,
  parentSessionId,
  onOpenParent,
  onClose,
}: {
  sessionId: string;
  label: string;
  /** One line describing the session (its relationship), under the label. */
  subtitle?: string;
  /** The parent chat a child run belongs to; with `onOpenParent`, a link. */
  parentSessionId?: string;
  onOpenParent?: (parentSessionId: string) => void;
  onClose: () => void;
}) {
  const [entries, setEntries] = useState<TranscriptEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Bumped by Retry: the effect below re-runs the load for the same session.
  const [attempt, setAttempt] = useState(0);

  // biome-ignore lint/correctness/useExhaustiveDependencies: `attempt` is the Retry trigger — bumping it re-runs this load for the same session
  useEffect(() => {
    const controller = new AbortController();
    setEntries(null);
    setError(null);
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
  }, [sessionId, attempt]);

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm">{label}</DialogTitle>
          <DialogDescription className="break-all font-mono text-xs">
            {subtitle ? (
              <>
                <span className="block font-sans text-muted-foreground">
                  {subtitle}
                </span>
                {sessionId}
              </>
            ) : (
              sessionId
            )}
          </DialogDescription>
          {parentSessionId && onOpenParent && (
            <div>
              <Button
                type="button"
                variant="link"
                size="sm"
                className="h-auto px-0 text-xs"
                onClick={() => onOpenParent(parentSessionId)}
              >
                Open parent chat
              </Button>
            </div>
          )}
        </DialogHeader>
        <ScrollArea className="max-h-[60vh] pr-3">
          {error ? (
            <div className="flex flex-col items-start gap-2 py-6">
              <p role="alert" className="text-sm text-destructive">
                {error}
              </p>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setAttempt((n) => n + 1)}
              >
                Retry
              </Button>
            </div>
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
