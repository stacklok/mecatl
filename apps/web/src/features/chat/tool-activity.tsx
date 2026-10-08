// SPDX-License-Identifier: Apache-2.0

import { ChevronRight, ExternalLink, Wrench } from "lucide-react";
import { Button } from "../../components/ui/button";
import { cn } from "../../lib/utils";
import { ApprovalPanel, type ApprovalRequest, type ApprovalVerdict } from "./approval-panel";
import { approvalMatchesToolCall } from "./chat-state";
import { parseDiffArgs, ToolDiff } from "./edit-diff";
import { useDetailsOpen } from "./use-details-open";

export interface ToolActivity {
  args: string;
  id: string;
  isError?: boolean;
  name: string;
  output?: string;
  /** Present only for a tool.call observed on a specific live or replayed run. */
  runId?: string;
}

export function ToolActivityList({
  approvalDisabled,
  approvalUncertain,
  approvals = [],
  onPreview,
  onRespondToApproval,
  tools,
}: {
  approvalDisabled?: (approval: ApprovalRequest) => boolean;
  approvalUncertain?: (approval: ApprovalRequest) => boolean;
  approvals?: ApprovalRequest[];
  onPreview?: (tool: ToolActivity) => void;
  onRespondToApproval?: (approval: ApprovalRequest, verdict: ApprovalVerdict) => void;
  tools: ToolActivity[];
}) {
  const [open, toggleOpen] = useDetailsOpen();
  const failed = tools.filter((tool) => tool.isError).length;

  return (
    <div className="my-2 rounded-lg border bg-muted/20">
      <button
        aria-expanded={open || approvals.length > 0}
        className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-muted-foreground"
        onClick={toggleOpen}
        type="button"
      >
        <ChevronRight
          aria-hidden="true"
          className={cn(
            "size-3.5 transition-transform",
            (open || approvals.length > 0) && "rotate-90",
          )}
        />
        <Wrench aria-hidden="true" className="size-3.5" />
        <span className="font-medium">
          {tools.length} tool {tools.length === 1 ? "call" : "calls"}
        </span>
        {failed > 0 && <span className="text-destructive">· {failed} failed</span>}
      </button>
      {(open || approvals.length > 0) && (
        <div className="space-y-2 border-t p-3">
          {tools.map((tool) => (
            <ToolCallRow
              approvalDisabled={approvalDisabled}
              approvalUncertain={approvalUncertain}
              approvals={approvals.filter((approval) => approvalMatchesToolCall(approval, tool))}
              key={`${tool.runId ?? ""}:${tool.id}`}
              onPreview={onPreview}
              onRespondToApproval={onRespondToApproval}
              tool={tool}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function ToolCallRow({
  approvalDisabled,
  approvalUncertain,
  approvals,
  onPreview,
  onRespondToApproval,
  tool,
}: {
  approvalDisabled?: (approval: ApprovalRequest) => boolean;
  approvalUncertain?: (approval: ApprovalRequest) => boolean;
  approvals: ApprovalRequest[];
  onPreview?: (tool: ToolActivity) => void;
  onRespondToApproval?: (approval: ApprovalRequest, verdict: ApprovalVerdict) => void;
  tool: ToolActivity;
}) {
  const [open, toggleOpen] = useDetailsOpen();

  return (
    <details
      className="rounded-md bg-background px-3 py-2 text-xs"
      onToggle={toggleOpen}
      open={open || approvals.length > 0}
    >
      <summary className="cursor-pointer font-mono font-medium">
        <span
          className={cn(
            "mr-2 inline-block size-1.5 rounded-full",
            tool.output === undefined
              ? "animate-pulse bg-brand"
              : tool.isError
                ? "bg-destructive"
                : "bg-success",
          )}
        />
        {tool.name}
      </summary>
      {parseDiffArgs(tool.name, tool.args) ? (
        <div className="mt-3">
          <ToolDiff name={tool.name} rawArgs={tool.args} />
        </div>
      ) : (
        <>
          <p className="mt-3 font-medium text-muted-foreground">Input</p>
          <pre className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap rounded bg-muted/50 p-2 font-mono">
            {tool.args || "{}"}
          </pre>
        </>
      )}
      {tool.output !== undefined && (
        <>
          <div className="mt-3 flex items-center justify-between gap-3">
            <p className="font-medium text-muted-foreground">Output</p>
            {onPreview && (
              <Button onClick={() => onPreview(tool)} size="sm" variant="ghost">
                <ExternalLink aria-hidden="true" />
                Open result
              </Button>
            )}
          </div>
          <pre className="mt-1 max-h-56 overflow-auto whitespace-pre-wrap rounded bg-muted/50 p-2 font-mono">
            {tool.output || "No output"}
          </pre>
        </>
      )}
      {approvals.map((approval) => (
        <ApprovalPanel
          approval={approval}
          disabled={!approval.controlTarget || (approvalDisabled?.(approval) ?? false)}
          key={`${approval.controlTarget?.runId ?? ""}:${approval.askId}`}
          onRespond={(verdict) => onRespondToApproval?.(approval, verdict)}
          uncertain={approvalUncertain?.(approval)}
        />
      ))}
    </details>
  );
}
