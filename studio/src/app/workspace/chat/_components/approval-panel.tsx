"use client";

import { ShieldAlert } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { ApprovalChoice, ApprovalRequest } from "@/features/agent";
import { cn } from "@/lib/utils";

const DELETE_WORDS = /\b(delete|remove|drop|revoke|destroy|purge|rm)\b/i;

/**
 * The daemon's verdict is three-way (allow_once / allow_always / deny), so the
 * panel offers exactly those scopes — no "for session" button, which would
 * promise a grant scope the backend does not model.
 */
export function ApprovalPanel({
  approval,
  onRespond,
}: {
  approval: ApprovalRequest;
  onRespond: (choice: ApprovalChoice) => void;
}) {
  // ONE ask = ONE tool call. The pill names the tool; the args belong in the
  // preview block below, never badge-ified (a Write ask's args are a whole
  // file). Destructiveness is judged on the tool name alone — scanning file
  // CONTENT for the word "delete" painted harmless writes red.
  const toolName =
    approval.toolName ||
    approval.description.replace(/ needs your approval\.?$/i, "").trim() ||
    "Tool";
  const destructive = DELETE_WORDS.test(toolName);

  return (
    <div
      className={cn(
        "my-3 rounded-xl border p-4",
        destructive
          ? "border-destructive/40 bg-destructive/5"
          : "border-warning/30 bg-warning/5",
      )}
    >
      <div className="mb-2 flex items-center gap-2">
        <ShieldAlert
          className={cn(
            "size-4",
            destructive ? "text-destructive" : "text-warning",
          )}
        />
        <span
          className={cn(
            "text-sm font-semibold",
            destructive ? "text-destructive" : "text-warning",
          )}
        >
          Permission required
        </span>
      </div>
      <p className="mb-2 text-sm">{approval.description}</p>
      <div className="mb-2 flex flex-wrap gap-1.5">
        <Badge
          variant="secondary"
          className={cn(
            "gap-1 border-transparent font-mono text-xs",
            destructive
              ? "bg-destructive/15 text-destructive"
              : "bg-warning/15 text-warning",
          )}
        >
          {toolName}
        </Badge>
      </div>
      {destructive && (
        <p className="mb-3 text-xs font-medium text-destructive">
          This action modifies or deletes data.
        </p>
      )}
      <pre
        className={cn(
          "mb-4 max-h-56 overflow-y-auto whitespace-pre-wrap rounded-lg border bg-background px-3 py-2.5 font-mono text-xs leading-relaxed",
          destructive ? "border-destructive/20" : "border-warning/20",
        )}
      >
        {approval.details}
      </pre>
      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          onClick={() => onRespond("once")}
          className={cn(
            "text-white",
            destructive
              ? "bg-destructive hover:bg-destructive-strong"
              : "bg-warning hover:bg-warning/90",
          )}
        >
          Allow once
        </Button>
        <Button
          size="sm"
          variant="outline"
          onClick={() => onRespond("always")}
          className={cn(
            destructive
              ? "border-destructive/30 hover:bg-destructive/5"
              : "border-warning/30 hover:bg-warning/5",
          )}
        >
          Always allow
        </Button>
        <Button
          size="sm"
          variant="ghost"
          onClick={() => onRespond("deny")}
          className="text-muted-foreground hover:text-foreground"
        >
          Deny
        </Button>
      </div>
    </div>
  );
}
