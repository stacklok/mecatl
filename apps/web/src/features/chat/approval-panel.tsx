// SPDX-License-Identifier: Apache-2.0

import { ShieldAlert } from "lucide-react";
import { Badge } from "../../components/ui/badge";
import { Button } from "../../components/ui/button";
import { cn } from "../../lib/utils";
import { parseDiffArgs, ToolDiff } from "./edit-diff";
import { useActiveEscapeAsk } from "./escape-hint-context";

const destructiveTool = /\b(delete|remove|drop|revoke|destroy|purge|rm)\b/iu;

export interface ApprovalRequest {
  args: string;
  askId: string;
  /** Presentation correlation only; never a verdict target. */
  callId?: string;
  /** Immutable identity from the permission.ask delivery, when its run is known. */
  controlTarget?: { askId: string; runId: string; sessionId: string };
  reason: string;
  tool: string;
}

export type ApprovalVerdict = "allow_once" | "allow_always" | "deny";

export function ApprovalPanel({
  approval,
  disabled,
  onRespond,
  position = 1,
  uncertain = false,
  total = 1,
}: {
  approval: ApprovalRequest;
  disabled: boolean;
  onRespond: (verdict: ApprovalVerdict) => void;
  position?: number;
  uncertain?: boolean;
  total?: number;
}) {
  const destructive = destructiveTool.test(approval.tool);
  const escapeHint = useActiveEscapeAsk(approval) && !disabled;
  const hasArgs = approval.args.trim().length > 0;
  const showDiff =
    hasArgs &&
    approval.args.length <= 65_536 &&
    parseDiffArgs(approval.tool, approval.args) !== null;

  return (
    <section
      aria-label={`Permission required: ${approval.tool || "Tool"}`}
      className={cn(
        "my-2 min-w-0 rounded-xl border p-4",
        destructive ? "border-destructive/40 bg-destructive/5" : "border-warning/40 bg-warning/5",
      )}
    >
      <div className="flex items-center gap-2">
        <ShieldAlert
          aria-hidden="true"
          className={cn("size-4", destructive ? "text-destructive" : "text-warning")}
        />
        <span className="text-sm font-semibold">Permission required</span>
        {total > 1 && (
          <Badge variant="outline">
            {position} of {total}
          </Badge>
        )}
        <Badge className="font-mono" variant={destructive ? "destructive" : "warning"}>
          {approval.tool || "Tool"}
        </Badge>
      </div>
      {approval.reason && <p className="mt-2 text-sm">{approval.reason}</p>}
      {!hasArgs && (
        <p className="mt-2 text-sm" role="status">
          Arguments unavailable
        </p>
      )}
      {showDiff && (
        <ToolDiff bounded className="mt-3" name={approval.tool} rawArgs={approval.args} />
      )}
      {hasArgs && (
        <details className="my-3 text-xs">
          <summary className="cursor-pointer">Raw arguments</summary>
          <pre className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-all rounded-lg border bg-background p-3 font-mono">
            {approval.args}
          </pre>
        </details>
      )}
      {uncertain && (
        <p className="mb-3 text-sm" role="status">
          This verdict's outcome is uncertain. Refresh activity before deciding again.
        </p>
      )}
      <div className="flex flex-wrap gap-2">
        <Button disabled={disabled || !hasArgs} onClick={() => onRespond("allow_once")} size="sm">
          Allow once
        </Button>
        <Button
          disabled={disabled || !hasArgs}
          onClick={() => onRespond("allow_always")}
          size="sm"
          variant="outline"
        >
          Always allow
        </Button>
        <Button disabled={disabled} onClick={() => onRespond("deny")} size="sm" variant="ghost">
          Deny
        </Button>
      </div>
      {escapeHint && <p className="mt-2 text-xs text-muted-foreground">Esc to Deny</p>}
    </section>
  );
}
