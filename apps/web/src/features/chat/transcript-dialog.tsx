// SPDX-License-Identifier: Apache-2.0

import type { GetSessionTranscriptResponse } from "@mecatl-studio/contracts/generated";
import { getSessionTranscriptOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "../../components/ui/dialog";

type TranscriptMessage = GetSessionTranscriptResponse["messages"][number];

/**
 * A durable transcript inspection surface. It deliberately exposes no session
 * controls: scheduled sessions are readable here, never continued or mutated.
 */
export function TranscriptDialog({
  onOpenChange,
  open,
  sessionId,
  title,
}: {
  onOpenChange: (open: boolean) => void;
  open: boolean;
  sessionId: string;
  title: string;
}) {
  const transcript = useQuery({
    ...getSessionTranscriptOptions({ path: { sessionId } }),
    enabled: open && Boolean(sessionId),
  });

  return (
    <Dialog onOpenChange={onOpenChange} open={open}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>Read-only transcript from this scheduled execution.</DialogDescription>
        </DialogHeader>

        {transcript.isPending ? (
          <TranscriptState text="Reading transcript…" />
        ) : transcript.isError ? (
          <TranscriptState error text={errorMessage(transcript.error)} />
        ) : (
          <div className="space-y-4" data-read-only-transcript>
            {!transcript.data.complete && (
              <p className="rounded-lg border border-warning/40 bg-warning/10 px-3 py-2 text-sm">
                This transcript is incomplete; some messages may be unavailable.
              </p>
            )}
            {transcript.data.messages.length === 0 ? (
              <TranscriptState text="This execution has no recorded messages." />
            ) : (
              transcriptRows(transcript.data.messages).map((row) => (
                <TranscriptMessageRow key={row.key} message={row.message} />
              ))
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

function transcriptRows(messages: readonly TranscriptMessage[]) {
  const occurrences = new Map<string, number>();
  return messages.map((message) => {
    const fingerprint = `${message.role}\u0000${message.text}\u0000${message.toolCalls.map((tool) => tool.id).join("\u0000")}\u0000${message.toolResult?.callId ?? ""}`;
    const occurrence = (occurrences.get(fingerprint) ?? 0) + 1;
    occurrences.set(fingerprint, occurrence);
    return { key: `${fingerprint}\u0000${occurrence}`, message };
  });
}

function TranscriptMessageRow({ message }: { message: TranscriptMessage }) {
  return (
    <article
      className="rounded-lg border bg-card p-4"
      aria-label={`${roleLabel(message.role)} message`}
    >
      <h3 className="text-xs font-semibold text-muted-foreground">{roleLabel(message.role)}</h3>
      {message.delivery && (
        <p className="mt-2 text-xs text-muted-foreground">
          Scheduled task {message.delivery.scheduleName} {message.delivery.kind}
          {message.delivery.stop ? ` · ${message.delivery.stop}` : ""}
        </p>
      )}
      {message.text && (
        <p className="mt-2 whitespace-pre-wrap break-words text-sm">{message.text}</p>
      )}
      {message.images.length > 0 && (
        <p className="mt-2 text-xs text-muted-foreground">
          {message.images.length} image{message.images.length === 1 ? "" : "s"}
        </p>
      )}
      {message.toolCalls.length > 0 && (
        <div className="mt-3 space-y-2">
          {message.toolCalls.map((tool) => (
            <div className="rounded-md bg-muted/40 p-3 text-xs" key={tool.id}>
              <p className="font-mono font-semibold">Tool: {tool.name}</p>
              <pre className="mt-2 overflow-x-auto whitespace-pre-wrap break-words font-mono">
                {tool.args || "{}"}
              </pre>
              {message.toolResult?.callId === tool.id && (
                <>
                  <p className="mt-2 font-medium">
                    {message.toolResult.isError ? "Failed result" : "Result"}
                  </p>
                  <pre className="mt-1 overflow-x-auto whitespace-pre-wrap break-words font-mono">
                    {message.toolResult.content || "No output"}
                  </pre>
                </>
              )}
            </div>
          ))}
        </div>
      )}
      {message.toolResult &&
        !message.toolCalls.some((tool) => tool.id === message.toolResult?.callId) && (
          <div className="mt-3 rounded-md bg-muted/40 p-3 text-xs">
            <p className="font-medium">
              {message.toolResult.isError ? "Failed result" : "Tool result"}
            </p>
            <pre className="mt-1 overflow-x-auto whitespace-pre-wrap break-words font-mono">
              {message.toolResult.content || "No output"}
            </pre>
          </div>
        )}
    </article>
  );
}

function TranscriptState({ error, text }: { error?: boolean; text: string }) {
  return (
    <p
      className={`rounded-lg border border-dashed p-6 text-sm ${error ? "text-destructive" : "text-muted-foreground"}`}
      role={error ? "alert" : "status"}
    >
      {text}
    </p>
  );
}

function roleLabel(role: string) {
  if (role === "user") return "You";
  if (role === "assistant") return "Mecatl";
  return role || "Message";
}

function errorMessage(error: unknown) {
  if (typeof error === "object" && error !== null && "detail" in error) return String(error.detail);
  return error instanceof Error ? error.message : "The transcript could not be read.";
}
