// SPDX-License-Identifier: Apache-2.0

import type { SessionDetailResponse, SessionUsageResponse } from "@mecatl-studio/contracts";
import { GitFork, Minimize2 } from "lucide-react";
import { Button } from "../../components/ui/button";
import type { ComposerModelOption, DraftChatConfiguration } from "./chat-composer";
import { ComposerOptionMenu } from "./composer-option-menu";
import { contextUtilization } from "./context-usage";
import { groupModels, ModelEffortMenu } from "./model-effort-menu";

const MODE_OPTIONS = [
  {
    description: "Always ask before making changes.",
    title: "Manual",
    value: "default" as const,
  },
  {
    description: "Automatically accept all file edits.",
    title: "Accept edits",
    value: "acceptEdits" as const,
  },
  {
    description: "Create a plan before making changes.",
    title: "Plan",
    value: "plan" as const,
  },
];

interface ChatSessionControlsProps {
  compacting: boolean;
  detail: SessionDetailResponse;
  disabled: boolean;
  forking: boolean;
  models: ComposerModelOption[];
  modePending: boolean;
  onCompact: () => void;
  onFork: (
    model: { id: string; providerId: string },
    reasoningEffort: DraftChatConfiguration["reasoningEffort"],
  ) => void;
  onModeChange: (mode: DraftChatConfiguration["mode"]) => void;
  safetyLevel?: string;
}

export function ChatSessionControls({
  compacting,
  detail,
  disabled,
  forking,
  models,
  modePending,
  onCompact,
  onFork,
  onModeChange,
  safetyLevel = "managed",
}: ChatSessionControlsProps) {
  const busy = disabled || compacting || forking || modePending;
  const model = detail.model;
  const groupedModels = groupModels(models);

  return (
    <section
      aria-label="Chat configuration and usage"
      className="mx-auto mb-2 w-[calc(100%-2rem)] max-w-3xl rounded-xl border bg-muted/20 px-3 py-2"
    >
      <div className="flex flex-wrap items-center gap-2">
        <ComposerOptionMenu
          disabled={busy}
          items={MODE_OPTIONS}
          label="Mode"
          onSelect={onModeChange}
          value={detail.mode}
          valueLabel={
            MODE_OPTIONS.find((option) => option.value === detail.mode)?.title ?? "Manual"
          }
        />

        <div
          aria-label={`Safety level: ${safetyLabel(safetyLevel)}. Managed by your organization.`}
          className="flex h-8 items-center gap-1.5 rounded-full border bg-background px-3 text-xs"
          role="status"
          title="Safety level is managed by your organization"
        >
          <span className="font-medium">Safety</span>
          <span className="text-muted-foreground">{safetyLabel(safetyLevel)}</span>
        </div>

        {/* Hidden without an inventory: see the note in chat-composer.tsx. */}
        {models.length > 0 ? (
          <ModelEffortMenu
            disabled={busy || !detail.capabilities.modelSelection}
            effort={model?.reasoningEffort ?? "default"}
            groupedModels={groupedModels}
            model={model}
            onEffortChange={(effort) =>
              model && onFork(model, effort as DraftChatConfiguration["reasoningEffort"])
            }
            onModelChange={(nextModel) =>
              nextModel && onFork(nextModel, model?.reasoningEffort ?? "default")
            }
          />
        ) : null}

        <Button
          className="ml-auto h-8"
          disabled={busy || !detail.capabilities.manualCompaction}
          onClick={onCompact}
          size="sm"
          title={
            detail.capabilities.manualCompaction
              ? "Compact model-visible conversation history"
              : "Manual compaction is not enabled on this deployment"
          }
          variant="ghost"
        >
          <Minimize2 aria-hidden="true" />
          {compacting ? "Compacting…" : "Compact"}
        </Button>
      </div>

      {model && (
        <ContextUsage
          contextWindow={model.contextWindow}
          model={`${model.id} · ${humanize(model.providerId)}`}
          usage={detail.usage}
        />
      )}
      {(forking || modePending) && (
        <p className="mt-2 flex items-center gap-1.5 text-xs text-muted-foreground">
          {forking && <GitFork aria-hidden="true" className="size-3.5" />}
          {forking ? "Forking this conversation…" : "Updating permission mode…"}
        </p>
      )}
    </section>
  );
}

function ContextUsage({
  contextWindow,
  model,
  usage,
}: {
  contextWindow: string;
  model: string;
  usage: SessionUsageResponse;
}) {
  const fraction = contextUtilization(usage, contextWindow);
  const total = BigInt(usage.inputTokens) + BigInt(usage.outputTokens);
  if (fraction === null || total <= 0n) return null;
  const percent = Math.round(fraction * 100);

  return (
    <div
      className="mt-2 flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground"
      title={`${formatTokens(usage.inputTokens)} input · ${formatTokens(usage.outputTokens)} output · ${formatTokens(usage.reasoningTokens)} reasoning · ${formatTokens(usage.cacheReadTokens)} cache read`}
    >
      <span className="truncate font-medium">{model}</span>
      <span aria-hidden="true" className="h-1 w-16 overflow-hidden rounded-full bg-border">
        <span
          className={`block h-full rounded-full ${fraction >= 0.85 ? "bg-warning" : "bg-brand/60"}`}
          style={{ width: `${Math.max(2, percent)}%` }}
        />
      </span>
      <span className="tabular-nums">~{percent}% of context</span>
      <span className="text-muted-foreground/70">{formatTokens(total.toString())} tokens</span>
    </div>
  );
}

function safetyLabel(value: string) {
  if (!value) return "Managed";
  return value.charAt(0).toUpperCase() + value.slice(1).toLowerCase();
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
