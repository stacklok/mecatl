// SPDX-License-Identifier: Apache-2.0

import type { SoulInspectionResponse } from "@mecatl-studio/contracts";
import { Loader2, RefreshCw } from "lucide-react";
import { Badge } from "../../components/ui/badge";
import { Button } from "../../components/ui/button";

/**
 * The soul view of the session dialog, ported from the prototype's
 * `soul-dialog.tsx`: the connection's resolved soul with its provenance,
 * trust, and size, and the body exactly as the prompt receives it.
 * `SessionInspection` owns the read and its gate; this only renders it.
 */

export const SOUL_NONE = "No soul is applied on this daemon.";

const PROVENANCE_LABEL: Record<string, string> = {
  driver: "Driver soul",
  project: "Project soul",
  user: "User soul",
};

export function SoulView({
  error,
  onRetry,
  pending,
  soul,
}: {
  error: boolean;
  onRetry: () => void;
  pending: boolean;
  soul?: SoulInspectionResponse;
}) {
  if (pending) {
    return (
      <p className="flex items-center gap-2 text-muted-foreground" role="status">
        <Loader2 aria-hidden="true" className="size-4 animate-spin" />
        Reading the soul…
      </p>
    );
  }
  if (error) {
    return (
      <div className="flex flex-col gap-2">
        <p className="break-words text-destructive" role="alert">
          Soul could not be read.
        </p>
        <Button className="w-fit" onClick={onRetry} size="sm" variant="outline">
          <RefreshCw aria-hidden="true" className="size-3.5" />
          Retry
        </Button>
      </div>
    );
  }
  if (!soul) return null;
  if (!soul.present) return <p className="text-muted-foreground">{SOUL_NONE}</p>;
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge variant="secondary">{PROVENANCE_LABEL[soul.provenance] ?? "Soul"}</Badge>
        <Badge variant={soul.trusted ? "secondary" : "destructive"}>
          {soul.trusted ? "trusted" : "untrusted"}
        </Badge>
        {soul.drifted && <Badge variant="outline">drifted</Badge>}
        <span className="text-xs text-muted-foreground">
          {Number(soul.sizeBytes).toLocaleString()} bytes
        </span>
      </div>
      <pre
        className="overflow-auto whitespace-pre-wrap break-words rounded-md border border-border bg-muted/40 p-3 font-mono text-xs"
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
  );
}
