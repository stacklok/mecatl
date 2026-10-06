// SPDX-License-Identifier: Apache-2.0

import { Check, ChevronDown, Search } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "../../components/ui/dropdown-menu";
import { cn } from "../../lib/utils";
import type { ComposerModelOption } from "./chat-composer";

const EFFORT_OPTIONS = [
  { label: "Auto", value: "default" },
  { label: "Low", value: "low" },
  { label: "Medium", value: "medium" },
  { label: "High", value: "high" },
  { label: "Extra high", value: "xhigh" },
  { label: "Max", value: "max" },
] as const;

/** Groups models by provider for `ModelEffortMenu`, each group ordered by label. */
export function groupModels(models: ComposerModelOption[]): Map<string, ComposerModelOption[]> {
  const groups = new Map<string, ComposerModelOption[]>();
  for (const model of [...models].sort((left, right) =>
    `${left.providerId}\0${left.label}`.localeCompare(`${right.providerId}\0${right.label}`),
  )) {
    groups.set(model.providerId, [...(groups.get(model.providerId) ?? []), model]);
  }
  return groups;
}

/**
 * The composer's combined Model + Effort picker, matching Studio's single
 * "Model · Effort" trigger: one pill opens a menu with two rows ("Model",
 * "Effort"), each opening its own submenu, plus a reset action.
 */
export function ModelEffortMenu({
  disabled,
  effort,
  groupedModels,
  model,
  onEffortChange,
  onModelChange,
  onReset,
}: {
  disabled?: boolean;
  effort: string;
  groupedModels: Map<string, ComposerModelOption[]>;
  model?: { id: string; providerId: string };
  onEffortChange: (value: string) => void;
  onModelChange: (model: { id: string; providerId: string } | undefined) => void;
  /** Omit to hide "Reset to default" — appropriate where there is no default to revert to (e.g. a live session, where every change is an explicit fork). */
  onReset?: () => void;
}) {
  const modelLabel =
    groupedModels.get(model?.providerId ?? "")?.find((candidate) => candidate.id === model?.id)
      ?.label ?? "Default";
  const effortLabel = EFFORT_OPTIONS.find((option) => option.value === effort)?.label ?? "Auto";
  const canReset = model !== undefined || effort !== "default";

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild disabled={disabled}>
        <button
          className="flex h-8 items-center gap-1.5 rounded-full border px-3 text-xs disabled:pointer-events-none disabled:opacity-50"
          type="button"
        >
          <span className="font-medium">Model</span>
          <span className="text-muted-foreground">
            {modelLabel} · {effortLabel}
          </span>
          <ChevronDown aria-hidden="true" className="size-3 text-muted-foreground" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-56">
        <DropdownMenuSub>
          <DropdownMenuSubTrigger>
            <span className="flex-1">Model</span>
            <span className="text-muted-foreground">{modelLabel}</span>
          </DropdownMenuSubTrigger>
          <DropdownMenuSubContent className="w-64 p-0">
            <ModelFilterList
              groupedModels={groupedModels}
              model={model}
              onModelChange={onModelChange}
            />
          </DropdownMenuSubContent>
        </DropdownMenuSub>
        <DropdownMenuSub>
          <DropdownMenuSubTrigger>
            <span className="flex-1">Effort</span>
            <span className="text-muted-foreground">{effortLabel}</span>
          </DropdownMenuSubTrigger>
          <DropdownMenuSubContent className="w-48">
            {EFFORT_OPTIONS.map((option) => (
              <DropdownMenuItem key={option.value} onSelect={() => onEffortChange(option.value)}>
                <Check
                  aria-hidden="true"
                  className={cn("size-3.5", effort !== option.value && "invisible")}
                />
                {option.label}
              </DropdownMenuItem>
            ))}
          </DropdownMenuSubContent>
        </DropdownMenuSub>
        {onReset && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem disabled={!canReset} onSelect={onReset}>
              Reset to default
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/**
 * The Model submenu's body: a filter input (matching the model's own label
 * or its provider) above the grouped, checkable list — for a catalogue too
 * long to scan by scrolling alone.
 */
function ModelFilterList({
  groupedModels,
  model,
  onModelChange,
}: {
  groupedModels: Map<string, ComposerModelOption[]>;
  model?: { id: string; providerId: string };
  onModelChange: (model: { id: string; providerId: string } | undefined) => void;
}) {
  const [filter, setFilter] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  // The submenu grabs initial focus for its own items; steal it back for the filter input.
  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  const normalized = filter.trim().toLocaleLowerCase();
  const filteredGroups = [...groupedModels.entries()]
    .map(
      ([providerId, models]) =>
        [
          providerId,
          normalized
            ? models.filter(
                (candidate) =>
                  candidate.label.toLocaleLowerCase().includes(normalized) ||
                  humanize(providerId).toLocaleLowerCase().includes(normalized),
              )
            : models,
        ] as const,
    )
    .filter(([, models]) => models.length > 0);
  const showDefault = !normalized || "default".includes(normalized);

  return (
    <>
      <div className="flex items-center gap-2 border-b px-2.5 py-2">
        <Search aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground" />
        <input
          className="w-full bg-transparent text-xs outline-none placeholder:text-muted-foreground"
          onChange={(event) => setFilter(event.target.value)}
          onKeyDown={(event) => {
            // Let Escape (close the whole menu) and Enter (Radix's own commit
            // key) through; everything else is this input's own business,
            // not the menu's roving-focus / type-ahead navigation.
            if (event.key !== "Escape" && event.key !== "Enter") event.stopPropagation();
          }}
          placeholder="Filter models…"
          ref={inputRef}
          value={filter}
        />
      </div>
      <div className="max-h-64 overflow-y-auto p-1">
        {showDefault && (
          <DropdownMenuItem onSelect={() => onModelChange(undefined)}>
            <Check
              aria-hidden="true"
              className={cn("size-3.5", model !== undefined && "invisible")}
            />
            Default
          </DropdownMenuItem>
        )}
        {filteredGroups.map(([providerId, models]) => (
          <div key={providerId}>
            <p className="px-2 py-1 text-[11px] font-medium text-muted-foreground">
              {humanize(providerId)}
            </p>
            {models.map((candidate) => {
              const selected =
                model?.id === candidate.id && model.providerId === candidate.providerId;
              return (
                <DropdownMenuItem
                  key={`${candidate.providerId}:${candidate.id}`}
                  onSelect={() =>
                    onModelChange({ id: candidate.id, providerId: candidate.providerId })
                  }
                >
                  <Check aria-hidden="true" className={cn("size-3.5", !selected && "invisible")} />
                  {candidate.label}
                </DropdownMenuItem>
              );
            })}
          </div>
        ))}
        {!showDefault && filteredGroups.length === 0 && (
          <p className="px-2.5 py-4 text-center text-xs text-muted-foreground">No models match.</p>
        )}
      </div>
    </>
  );
}

function humanize(value: string): string {
  return value
    .split(/[-_]+/u)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}
