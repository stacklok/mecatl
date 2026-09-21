// SPDX-License-Identifier: Apache-2.0

import { ShieldAlert } from "lucide-react";
import { Badge } from "../../components/ui/badge";
import { Button } from "../../components/ui/button";
import { cn } from "../../lib/utils";

const destructiveTool = /\b(delete|remove|drop|revoke|destroy|purge|rm)\b/iu;

export interface ApprovalRequest {
  args: string;
  askId: string;
  reason: string;
  tool: string;
}

export type ApprovalVerdict = "allow_once" | "allow_always" | "deny";

export function ApprovalPanel({
  approval,
  disabled,
  onRespond,
  position = 1,
  total = 1,
}: {
  approval: ApprovalRequest;
  disabled: boolean;
  onRespond: (verdict: ApprovalVerdict) => void;
  position?: number;
  total?: number;
}) {
  const destructive = destructiveTool.test(approval.tool);

  return (
    <div
      className={cn(
        "mx-auto mb-3 w-[calc(100%-2rem)] max-w-3xl rounded-xl border p-4",
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
      <pre className="my-3 max-h-48 overflow-auto whitespace-pre-wrap rounded-lg border bg-background p-3 font-mono text-xs">
        {approval.args || "{}"}
      </pre>
      <div className="flex flex-wrap gap-2">
        <Button disabled={disabled} onClick={() => onRespond("allow_once")} size="sm">
          Allow once
        </Button>
        <Button
          disabled={disabled}
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
    </div>
  );
}
