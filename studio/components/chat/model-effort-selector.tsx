"use client";

import { Check, ChevronDown, RotateCcw, Search } from "lucide-react";
import { useMemo, useState } from "react";

import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";

export type ModelOption = {
  id: string;
  provider_id?: string;
  display_name?: string;
  reasoning?: boolean;
};

export type ModelSelection = { providerId: string; modelId: string; label: string } | null;

/**
 * The neutral reasoning-effort tiers mecated accepts on CreateSession
 * (`reasoning_effort`). "auto" is the wire's empty string — send no effort field
 * and let the provider default apply — so it belongs in the list as a real
 * choice rather than being modelled as "unset".
 */
const EFFORT_LEVELS = [
  { id: "", label: "Auto" },
  { id: "low", label: "Low" },
  { id: "medium", label: "Medium" },
  { id: "high", label: "High" },
  { id: "xhigh", label: "Extra high" },
  { id: "max", label: "Max" },
] as const;

export type EffortId = (typeof EFFORT_LEVELS)[number]["id"];

const GHOST_TRIGGER =
  "h-7 gap-1 rounded-full border-0 bg-transparent px-2.5 text-sm font-normal text-foreground shadow-none hover:bg-accent hover:text-accent-foreground";

const ROW =
  "flex cursor-pointer items-center justify-between gap-3 rounded-lg px-3 py-2.5 text-sm";

/**
 * Combined model + effort picker: the trigger reads "{model} {effort}", and the
 * menu drills into a Model submenu and an Effort submenu, with a reset.
 *
 * The selection applies to the NEXT session. mecatl fixes provider and model for
 * a session's lifetime — reasoning replay and the byte-stable prompt prefix are
 * provider-private, so "switch provider" means "new session". Rather than
 * disabling the control once a task is bound, it stays usable and the menu says
 * where the change lands; a picker that greys out as soon as you send your first
 * message is worse than one that tells you when it takes effect.
 */
export function ModelEffortSelector({
  models,
  selection,
  onSelectModel,
  effort,
  onSelectEffort,
  sessionModelLabel,
  defaultModelLabel,
}: {
  models: readonly ModelOption[];
  selection: ModelSelection;
  onSelectModel: (selection: ModelSelection) => void;
  effort: EffortId;
  onSelectEffort: (effort: EffortId) => void;
  /** The resolved model of the active task, once it has a session bound. */
  sessionModelLabel?: string;
  /**
   * What "no explicit selection" actually resolves to, learned from the last
   * session mecated opened. There is no read-only endpoint for the effective
   * default — it is only echoed on CreateSession.resolved_model — so before the
   * first session of a fresh install this is genuinely unknown.
   */
  defaultModelLabel?: string;
}) {
  const [query, setQuery] = useState("");
  // An OpenRouter inventory runs to several hundred entries, so the list is only
  // usable with a filter. Matched against the label AND the provider id, because
  // "which anthropic models do I have" is the common question.
  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return models;
    return models.filter((model) =>
      `${model.display_name ?? ""} ${model.id} ${model.provider_id ?? ""}`
        .toLowerCase()
        .includes(needle),
    );
  }, [models, query]);

  // Grouped by provider so the id appears once as a heading instead of on every
  // row. With a single provider the heading carries no information — every row
  // would share it — so the grouping collapses and the label disappears.
  const groups = useMemo(() => {
    const byProvider = new Map<string, ModelOption[]>();
    for (const model of filtered) {
      const key = model.provider_id ?? "";
      const bucket = byProvider.get(key);
      if (bucket) bucket.push(model);
      else byProvider.set(key, [model]);
    }
    return [...byProvider.entries()];
  }, [filtered]);
  const showProviderHeadings = groups.length > 1;

  const selectedEffort = EFFORT_LEVELS.find((level) => level.id === effort) ?? EFFORT_LEVELS[0];
  const isDefault = selection === null && effort === "";
  // Name the model, never the word "default": an operator wants to see WHICH
  // model is about to run. An explicit choice wins; otherwise show what the live
  // session resolved to, then what the last one did, and only fall back to a
  // placeholder when nothing has ever resolved.
  const unpinnedLabel = defaultModelLabel || "Default model";
  const triggerLabel = selection?.label || sessionModelLabel || unpinnedLabel;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button type="button" size="sm" className={GHOST_TRIGGER}>
          <span className="max-w-48 truncate">{triggerLabel}</span>
          <span className="text-muted-foreground">{selectedEffort.label}</span>
          <ChevronDown className="size-3.5 text-muted-foreground" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-64">
        <DropdownMenuSub>
          <DropdownMenuSubTrigger>
            <span className="flex-1">Model</span>
            <span className="max-w-32 truncate text-muted-foreground">
              {selection?.label || unpinnedLabel}
            </span>
          </DropdownMenuSubTrigger>
          <DropdownMenuSubContent className="flex max-h-[min(22rem,55vh)] w-80 flex-col p-0">
            <div className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-2.5">
              <Search className="size-3.5 shrink-0 text-muted-foreground" />
              <input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Search models…"
                aria-label="Search models"
                // Radix menus implement typeahead on keydown; without this the
                // first letter jumps focus to a matching row instead of typing.
                onKeyDown={(event) => event.stopPropagation()}
                className="w-full min-w-0 border-0 bg-transparent text-sm outline-none placeholder:text-muted-foreground/60 focus-visible:outline-none"
              />
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto p-2">
            <DropdownMenuItem
              className={cn(ROW, selection === null && "bg-accent")}
              onSelect={() => onSelectModel(null)}
            >
              <span className="min-w-0 flex-1">
                <span className="block truncate font-medium">{unpinnedLabel}</span>
                <span className="block text-xs text-muted-foreground">
                  Server default — not pinned
                </span>
              </span>
              <Check
                className={cn(
                  "size-4 shrink-0",
                  selection === null ? "text-foreground" : "text-transparent",
                )}
              />
            </DropdownMenuItem>
            {models.length === 0 && (
              <p className="px-3 py-2 text-xs text-muted-foreground/60">
                No model inventory yet
              </p>
            )}
            {models.length > 0 && filtered.length === 0 && (
              <p className="px-3 py-2 text-xs text-muted-foreground/60">
                No model matches “{query.trim()}”
              </p>
            )}
            {groups.map(([providerId, providerModels]) => (
              <div key={providerId || "unknown"}>
                {showProviderHeadings && providerId && (
                  <div className="px-3 pt-3 pb-1 text-[0.7rem] font-medium tracking-wider text-muted-foreground uppercase">
                    {providerId}
                  </div>
                )}
                {providerModels.map((model) => {
                  const label = model.display_name || model.id;
                  const isSelected = selection?.modelId === model.id;
                  return (
                    <DropdownMenuItem
                      key={`${model.provider_id}/${model.id}`}
                      className={cn(ROW, isSelected && "bg-accent")}
                      onSelect={() =>
                        onSelectModel({
                          // model_id without provider_id is a loud
                          // InvalidArgument, so the pair always travels together.
                          providerId: model.provider_id ?? "",
                          modelId: model.id,
                          label,
                        })
                      }
                    >
                      <span className="min-w-0 flex-1 truncate font-medium">{label}</span>
                      <Check
                        className={cn(
                          "size-4 shrink-0",
                          isSelected ? "text-foreground" : "text-transparent",
                        )}
                      />
                    </DropdownMenuItem>
                  );
                })}
              </div>
            ))}
            </div>
          </DropdownMenuSubContent>
        </DropdownMenuSub>

        <DropdownMenuSub>
          <DropdownMenuSubTrigger>
            <span className="flex-1">Effort</span>
            <span className="text-muted-foreground">{selectedEffort.label}</span>
          </DropdownMenuSubTrigger>
          <DropdownMenuSubContent className="w-72 p-2">
            {EFFORT_LEVELS.map((level) => {
              const isSelected = level.id === effort;
              return (
                <DropdownMenuItem
                  key={level.id || "auto"}
                  className={cn(ROW, isSelected && "bg-accent")}
                  onSelect={() => onSelectEffort(level.id)}
                >
                  <span className="font-medium">{level.label}</span>
                  <Check
                    className={cn(
                      "size-4 shrink-0",
                      isSelected ? "text-foreground" : "text-transparent",
                    )}
                  />
                </DropdownMenuItem>
              );
            })}
          </DropdownMenuSubContent>
        </DropdownMenuSub>

        <DropdownMenuSeparator />
        <DropdownMenuItem
          disabled={isDefault}
          onSelect={() => {
            onSelectModel(null);
            onSelectEffort("");
          }}
        >
          <RotateCcw className="mr-2 size-4 text-muted-foreground" />
          Reset to default
        </DropdownMenuItem>

      </DropdownMenuContent>
    </DropdownMenu>
  );
}
