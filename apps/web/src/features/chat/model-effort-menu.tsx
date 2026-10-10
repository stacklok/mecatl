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
import { effortLabel } from "../../lib/reasoning-effort";
import { cn } from "../../lib/utils";
import type { ComposerModelOption } from "./chat-composer";
import { GHOST_TRIGGER_CLASS } from "./composer-option-menu";
import { EffortSubmenu } from "./effort-picker";

interface ModelSelection {
  id: string;
  providerId: string;
}

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

function sameModel(model: ModelSelection | undefined, candidate: ModelSelection): boolean {
  return model?.id === candidate.id && model.providerId === candidate.providerId;
}

/** The label of the selected model, its ID when the inventory no longer lists it, or "Default". */
export function modelLabel(
  groupedModels: Map<string, ComposerModelOption[]>,
  model: ModelSelection | undefined,
): string {
  if (!model) return "Default";
  return (
    groupedModels.get(model.providerId)?.find((candidate) => candidate.id === model.id)?.label ??
    model.id
  );
}

/**
 * The composer's Model pill, in the prototype's (`stack-08`) look: one ghost
 * trigger reading "Model · Effort" opens a menu with a Model row (a filtered,
 * grouped submenu), an Effort row, and, on a draft, "Reset to default".
 *
 * A pick's meaning belongs to the caller: a draft updates its configuration,
 * and a live chat forks, because `forkSession` is the only call that changes a
 * session's model or effort.
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
  model?: ModelSelection;
  onEffortChange: (value: string) => void;
  onModelChange: (model: ModelSelection | undefined) => void;
  /** Omit to hide "Reset to default" and the Default row: a live chat has no default to revert to, since every change is an explicit fork. */
  onReset?: () => void;
}) {
  const label = modelLabel(groupedModels, model);
  const resolvedModel = [...groupedModels.values()]
    .flat()
    .find((candidate) => sameModel(model, candidate));
  const canReset = model !== undefined || effort !== "default";

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild disabled={disabled}>
        <button className={GHOST_TRIGGER_CLASS} type="button">
          <span>Model</span>
          <span className="text-muted-foreground @max-md:hidden">
            {label} · {effortLabel(effort)}
          </span>
          <ChevronDown aria-hidden="true" className="size-3.5 text-muted-foreground" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-64">
        <DropdownMenuSub>
          <DropdownMenuSubTrigger>
            <span className="flex-1">Model</span>
            <span className="truncate text-muted-foreground">{label}</span>
          </DropdownMenuSubTrigger>
          <DropdownMenuSubContent className="w-[min(26rem,calc(100vw-2rem))] p-0">
            <ModelFilterList
              allowUnset={onReset !== undefined}
              groupedModels={groupedModels}
              model={model}
              onModelChange={onModelChange}
            />
          </DropdownMenuSubContent>
        </DropdownMenuSub>
        <EffortSubmenu
          effort={effort}
          modelSupportsReasoning={resolvedModel?.reasoning}
          onSelect={onEffortChange}
        />
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

function filterGroups(
  groupedModels: Map<string, ComposerModelOption[]>,
  filter: string,
): (readonly [string, ComposerModelOption[]])[] {
  const normalized = filter.trim().toLocaleLowerCase();
  return [...groupedModels.entries()]
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
}

const modelRowClass = "flex items-center gap-3 rounded-lg px-3 py-2.5 text-sm";

/**
 * The Model submenu's body: a filter input (matching the model's label or its
 * provider) above the grouped list, for a catalogue too long to scan.
 */
function ModelFilterList({
  allowUnset,
  groupedModels,
  model,
  onModelChange,
}: {
  allowUnset: boolean;
  groupedModels: Map<string, ComposerModelOption[]>;
  model?: ModelSelection;
  onModelChange: (model: ModelSelection | undefined) => void;
}) {
  const [filter, setFilter] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  // The submenu grabs initial focus for its own items; steal it back for the filter input.
  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  const filteredGroups = filterGroups(groupedModels, filter);
  const normalized = filter.trim().toLocaleLowerCase();
  const showDefault = allowUnset && (!normalized || "default".includes(normalized));

  return (
    <>
      <div className="flex items-center gap-2 border-b px-3 py-2.5">
        <Search aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
        <input
          aria-label="Filter models"
          className="w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
          onChange={(event) => setFilter(event.target.value)}
          onKeyDown={(event) => {
            // Let Escape (close the whole menu) and Enter (Radix's own commit
            // key) through; everything else is this input's own business,
            // not the menu's roving-focus / type-ahead navigation.
            if (event.key !== "Escape" && event.key !== "Enter") event.stopPropagation();
          }}
          placeholder="Filter models"
          ref={inputRef}
          value={filter}
        />
      </div>
      <div className="max-h-72 overflow-y-auto p-1">
        {showDefault && (
          <DropdownMenuItem
            className={cn(modelRowClass, model === undefined && "bg-zinc-100 dark:bg-zinc-800")}
            onSelect={() => onModelChange(undefined)}
          >
            <span className="flex-1 truncate">Default</span>
            {model === undefined && <Check aria-hidden="true" className="size-4 shrink-0" />}
          </DropdownMenuItem>
        )}
        {filteredGroups.map(([providerId, models]) => (
          <div key={providerId}>
            <p className="px-3 pt-2 pb-1 text-xs font-medium text-muted-foreground">
              {humanize(providerId)}
            </p>
            {models.map((candidate) => {
              const selected = sameModel(model, candidate);
              return (
                <DropdownMenuItem
                  className={cn(modelRowClass, selected && "bg-zinc-100 dark:bg-zinc-800")}
                  key={`${candidate.providerId}:${candidate.id}`}
                  onSelect={() =>
                    onModelChange({ id: candidate.id, providerId: candidate.providerId })
                  }
                >
                  <span className="flex-1 truncate">{candidate.label}</span>
                  {selected && <Check aria-hidden="true" className="size-4 shrink-0" />}
                </DropdownMenuItem>
              );
            })}
          </div>
        ))}
        {!showDefault && filteredGroups.length === 0 && (
          <p className="px-3 py-6 text-center text-sm text-muted-foreground" role="status">
            No models match
          </p>
        )}
      </div>
    </>
  );
}

/**
 * The phone sheet's Model view, from the prototype's `ModelSheetSection`: a
 * plain filtered list with an always-visible search field. A touch sheet has
 * room for it, and menu keyboard navigation adds nothing there.
 */
export function ModelSheetSection({
  allowUnset,
  disabled,
  groupedModels,
  model,
  onModelChange,
}: {
  allowUnset: boolean;
  disabled?: boolean;
  groupedModels: Map<string, ComposerModelOption[]>;
  model?: ModelSelection;
  onModelChange: (model: ModelSelection | undefined) => void;
}) {
  const [filter, setFilter] = useState("");
  const filteredGroups = filterGroups(groupedModels, filter);
  const normalized = filter.trim().toLocaleLowerCase();
  const showDefault = allowUnset && (!normalized || "default".includes(normalized));
  const rowClass =
    "flex w-full items-center gap-3 px-4 py-3 text-left text-sm transition-colors hover:bg-muted/50 disabled:opacity-50";

  return (
    <div className="flex min-h-0 flex-col">
      <div className="flex items-center gap-2 border-b px-4 py-2.5">
        <Search aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
        <input
          aria-label="Search models"
          className="w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
          disabled={disabled}
          onChange={(event) => setFilter(event.target.value)}
          placeholder="Search models"
          value={filter}
        />
      </div>
      <div className="max-h-[50dvh] overflow-y-auto py-1">
        {showDefault && (
          <button
            aria-pressed={model === undefined}
            className={cn(rowClass, model === undefined && "bg-zinc-100 dark:bg-zinc-800")}
            disabled={disabled}
            onClick={() => onModelChange(undefined)}
            type="button"
          >
            <span className="flex-1 truncate">Default</span>
            {model === undefined && <Check aria-hidden="true" className="size-4 shrink-0" />}
          </button>
        )}
        {!showDefault && filteredGroups.length === 0 && (
          <p className="px-4 py-6 text-center text-sm text-muted-foreground" role="status">
            No models match
          </p>
        )}
        {filteredGroups.map(([providerId, models]) => (
          <div key={providerId}>
            <p className="px-4 py-1.5 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
              {humanize(providerId)}
            </p>
            {models.map((candidate) => {
              const selected = sameModel(model, candidate);
              return (
                <button
                  aria-pressed={selected}
                  className={cn(rowClass, selected && "bg-zinc-100 dark:bg-zinc-800")}
                  disabled={disabled}
                  key={`${candidate.providerId}:${candidate.id}`}
                  onClick={() =>
                    onModelChange({ id: candidate.id, providerId: candidate.providerId })
                  }
                  type="button"
                >
                  <span className="flex-1 truncate">{candidate.label}</span>
                  {selected && <Check aria-hidden="true" className="size-4 shrink-0" />}
                </button>
              );
            })}
          </div>
        ))}
      </div>
    </div>
  );
}

export function humanize(value: string): string {
  return value
    .split(/[-_]+/u)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}
