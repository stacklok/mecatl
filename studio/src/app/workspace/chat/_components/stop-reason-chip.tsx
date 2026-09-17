"use client";

import { AlertTriangle } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { StopReasonLabel } from "@/features/agent/stop-reason";

/**
 * The durable stop-reason chip under an assistant turn that ended on
 * something other than a clean `end_turn`: a limit stop ("stopped · turn
 * limit", warning tint) or a muted cue ("cancelled", "plan approved ·
 * executing"). The transcript's record of HOW the turn ended, so a stopped
 * turn never reads as a quiet success — the transient status line says the
 * same thing once, then the next run clears it; this chip stays.
 */
export function StopReasonChip({ label }: { label: StopReasonLabel }) {
  const warn = label.tone === "warn";
  return (
    <Badge
      variant={warn ? "warning" : "muted"}
      className="mt-1.5"
      data-testid="stop-reason-chip"
      title="How this turn ended"
    >
      {warn && <AlertTriangle aria-hidden="true" />}
      {label.text}
    </Badge>
  );
}
