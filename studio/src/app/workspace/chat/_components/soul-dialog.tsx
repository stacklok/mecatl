"use client";

import { Loader2, RefreshCw } from "lucide-react";
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
import { fetchHarnessSoul, type HarnessSoul } from "@/lib/harness/soul";

export const SOUL_NONE = "No soul is applied on this daemon.";
export const SOUL_UNSUPPORTED =
  "This daemon does not report its soul (older daemon).";

/** The provenance badge word (lib/harness/soul.ts vocabulary). */
function provenanceLabel(soul: HarnessSoul): string {
  switch (soul.provenance) {
    case "user":
      return "User soul";
    case "project":
      return "Project soul";
    case "driver":
      return "Driver soul";
    default:
      return "Soul";
  }
}

/**
 * The `/soul` built-in: the soul the daemon resolved and applies to every
 * run — its provenance, trust and size, and the clean body exactly as the
 * prompt receives it (read-only; the TUI's soul preview). Read fresh each
 * time the dialog opens; a failed read offers Retry. A daemon that
 * selected no soul says so plainly instead of showing an empty box.
 */
export function SoulDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [soul, setSoul] = useState<HarnessSoul | null | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [attempt, setAttempt] = useState(0);

  // biome-ignore lint/correctness/useExhaustiveDependencies: `attempt` is the Retry trigger — bumping it re-runs the read
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    setSoul(undefined);
    fetchHarnessSoul(controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setSoul(value);
      })
      .catch((caught: unknown) => {
        if (controller.signal.aborted) return;
        setError(caught instanceof Error ? caught.message : String(caught));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [open, attempt]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Soul</DialogTitle>
          <DialogDescription>
            The instructions the daemon adds to every run, as the model receives
            them.
          </DialogDescription>
        </DialogHeader>
        {loading && (
          <p
            role="status"
            className="flex items-center gap-2 text-sm text-muted-foreground"
          >
            <Loader2 className="size-4 animate-spin" aria-hidden="true" />
            Reading the soul…
          </p>
        )}
        {error && !loading && (
          <div className="flex flex-col gap-2">
            <p role="alert" className="text-sm text-destructive break-words">
              {error}
            </p>
            <Button
              variant="outline"
              size="sm"
              className="w-fit"
              onClick={() => setAttempt((n) => n + 1)}
            >
              <RefreshCw className="size-3.5" aria-hidden="true" />
              Retry
            </Button>
          </div>
        )}
        {!loading && !error && soul === null && (
          <p className="text-sm text-muted-foreground">{SOUL_UNSUPPORTED}</p>
        )}
        {!loading && !error && soul && !soul.present && (
          <p className="text-sm text-muted-foreground">{SOUL_NONE}</p>
        )}
        {!loading && !error && soul?.present && (
          <div className="flex flex-col gap-3">
            <div className="flex flex-wrap items-center gap-1.5">
              <Badge variant="secondary">{provenanceLabel(soul)}</Badge>
              <Badge variant={soul.trusted ? "secondary" : "destructive"}>
                {soul.trusted ? "trusted" : "untrusted"}
              </Badge>
              {soul.drifted && <Badge variant="outline">drifted</Badge>}
              <span className="text-xs text-muted-foreground">
                {soul.sizeBytes.toLocaleString()} bytes
              </span>
            </div>
            <pre
              className="max-h-[50vh] overflow-auto whitespace-pre-wrap break-words rounded-md border border-border bg-muted/40 p-3 font-mono text-xs"
              data-testid="soul-content"
            >
              {soul.content}
            </pre>
            {soul.sha256 && (
              <p className="break-all font-mono text-[11px] text-muted-foreground">
                sha256 {soul.sha256}
              </p>
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
