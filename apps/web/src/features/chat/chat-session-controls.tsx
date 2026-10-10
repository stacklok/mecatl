// SPDX-License-Identifier: Apache-2.0

import type { SessionDetailResponse, SessionUsageResponse } from "@mecatl-studio/contracts";
import { GitFork, Minimize2 } from "lucide-react";
import { cn } from "../../lib/utils";
import { GHOST_TRIGGER_CLASS } from "./composer-option-menu";
import { contextUtilization } from "./context-usage";

interface SessionUsageControlsProps {
  compacting: boolean;
  detail: SessionDetailResponse;
  disabled: boolean;
  forking: boolean;
  modePending: boolean;
  onCompact: () => void;
  /** `pills` sits at the end of the composer's pill row; `sheet` is the phone options sheet's footer. */
  variant: "pills" | "sheet";
}

/**
 * A live chat's usage and compaction, beside the composer's Mode and Model
 * pills: how much of the model's context the conversation fills, the Compact
 * action, and a note while a fork or a mode change is in flight. Mode and
 * Model themselves are composer pills (`chat-composer.tsx`).
 */
export function SessionUsageControls({
  compacting,
  detail,
  disabled,
  forking,
  modePending,
  onCompact,
  variant,
}: SessionUsageControlsProps) {
  const model = detail.model;
  const busy = disabled || compacting || forking || modePending;
  const compactTitle = detail.capabilities.manualCompaction
    ? "Compact model-visible conversation history"
    : "Manual compaction is not enabled on this deployment";
  const pending = (forking || modePending) && (
    <p
      className={cn(
        "flex items-center gap-1.5 text-xs text-muted-foreground",
        variant === "pills" ? "shrink-0 whitespace-nowrap px-1" : "px-4 pb-2",
      )}
      role="status"
    >
      {forking && <GitFork aria-hidden="true" className="size-3.5" />}
      {forking ? "Forking this conversation…" : "Updating permission mode…"}
    </p>
  );
  const usage = model && (
    <ContextUsage
      contextWindow={model.contextWindow}
      model={`${model.id} · ${humanize(model.providerId)}`}
      usage={detail.usage}
      variant={variant}
    />
  );

  if (variant === "sheet") {
    return (
      <div className="border-t py-2">
        {usage}
        {pending}
        <button
          className="flex w-full items-center gap-3 px-4 py-3 text-left text-sm transition-colors hover:bg-muted/50 disabled:opacity-50"
          disabled={busy || !detail.capabilities.manualCompaction}
          onClick={onCompact}
          title={compactTitle}
          type="button"
        >
          <Minimize2 aria-hidden="true" className="size-4 text-muted-foreground" />
          {compacting ? "Compacting…" : "Compact"}
        </button>
      </div>
    );
  }

  return (
    <div className="ml-auto flex shrink-0 items-center gap-1">
      {pending}
      {usage}
      <button
        className={GHOST_TRIGGER_CLASS}
        disabled={busy || !detail.capabilities.manualCompaction}
        onClick={onCompact}
        title={compactTitle}
        type="button"
      >
        <Minimize2 aria-hidden="true" className="size-3.5 text-muted-foreground" />
        {compacting ? "Compacting…" : "Compact"}
      </button>
    </div>
  );
}

function ContextUsage({
  contextWindow,
  model,
  usage,
  variant,
}: {
  contextWindow: string;
  model: string;
  usage: SessionUsageResponse;
  variant: "pills" | "sheet";
}) {
  const fraction = contextUtilization(usage, contextWindow);
  const total = BigInt(usage.inputTokens) + BigInt(usage.outputTokens);
  if (fraction === null || total <= 0n) return null;
  const percent = Math.round(fraction * 100);

  return (
    <div
      className={cn(
        "flex items-center gap-2 text-xs text-muted-foreground",
        variant === "pills"
          ? "shrink-0 whitespace-nowrap px-1.5 @max-md:hidden"
          : "flex-wrap px-4 py-2",
      )}
      title={`${model} · ${formatTokens(usage.inputTokens)} input · ${formatTokens(usage.outputTokens)} output · ${formatTokens(usage.reasoningTokens)} reasoning · ${formatTokens(usage.cacheReadTokens)} cache read`}
    >
      {variant === "sheet" && <span className="truncate font-medium">{model}</span>}
      <span aria-hidden="true" className="h-1 w-12 overflow-hidden rounded-full bg-border">
        <span
          className={`block h-full rounded-full ${fraction >= 0.85 ? "bg-warning" : "bg-brand/60"}`}
          style={{ width: `${Math.max(2, percent)}%` }}
        />
      </span>
      <span className="tabular-nums">~{percent}% of context</span>
      {variant === "sheet" && (
        <span className="text-muted-foreground/70">{formatTokens(total.toString())} tokens</span>
      )}
    </div>
  );
}

function humanize(value: string) {
  return value
    .split(/[-_]+/u)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function formatTokens(value: string) {
  const count = BigInt(value);
  if (count < 1_000n) return count.toString();
  if (count < 1_000_000n) return `${compact(count, 1_000n)}k`;
  return `${compact(count, 1_000_000n)}m`;
}

function compact(value: bigint, divisor: bigint) {
  const tenths = (value * 10n) / divisor;
  return tenths % 10n === 0n ? (tenths / 10n).toString() : `${tenths / 10n}.${tenths % 10n}`;
}
