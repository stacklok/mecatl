"use client";

/**
 * The model half of the composer's model + effort picker (the TUI's /models
 * surface): the daemon's live inventory as provider-qualified rows —
 * `provider · name`, capability glyphs (image input, reasoning), context
 * window — behind a type-to-filter box, under a `current:` header that names
 * the model in force and WHERE it came from (your pick / Studio default /
 * daemon default), with a ★ on the browser-local default for new chats and
 * an action to set or clear it (the ctrl+g analogue).
 *
 * The desktop list is a cmdk `Command` inside the Radix submenu — the shadcn
 * "combobox in a dropdown" shape — because a bare text input fights Radix's
 * roving focus and typeahead: cmdk owns arrow/Enter/Home/End from the input
 * and the submenu holds no Radix items, so Radix's typeahead has nothing to
 * steal focus for. Escape is two-stage like the TUI: a non-empty filter
 * clears first (`onEscapeKeyDown` on the submenu — Radix listens on the
 * document in the capture phase, so the input itself cannot intercept it),
 * an empty one lets Radix close the menu.
 */

import { Check, Star } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import {
  Command,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { DropdownMenuSubContent } from "@/components/ui/dropdown-menu";
import { InputSearch } from "@/components/ui/input-search";
import { type DefaultModel, useDefaultModel } from "@/lib/model-preferences";
import { cn } from "@/lib/utils";
import type { ComposerModelOption } from "./chat-input";
import { isDefaultOption, type ModelProvenance } from "./draft-model";

/** Shown in place of the rows when the filter matches nothing. */
export const NO_MODELS_MATCH = "No models match";

/** The filter box's accessible name (desktop and mobile alike). */
export const FILTER_MODELS_LABEL = "Filter models";

/**
 * Case-insensitive substring filter over provider id, model id and display
 * name; an empty (or whitespace) query keeps every row. Callers pass the
 * real models only — the auto row is pinned above the list, not filtered.
 */
export function filterModelOptions(
  options: readonly ComposerModelOption[],
  query: string,
): ComposerModelOption[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return [...options];
  return options.filter((option) =>
    [option.providerId ?? "", option.id, option.label].some((field) =>
      field.toLowerCase().includes(needle),
    ),
  );
}

/** cmdk selection value for a row: provider-qualified so the same model id
 *  on two providers stays two rows; the auto row has a fixed sentinel. */
const AUTO_VALUE = "__auto__";
function optionValue(option: ComposerModelOption): string {
  return option.id === ""
    ? AUTO_VALUE
    : `${option.providerId ?? ""}/${option.id}`;
}

/**
 * One row's body: the model's display name, the ★ default marker and the
 * current checkmark. Provider and capabilities are NOT repeated per row —
 * the list is grouped by provider, and the details live on the model's
 * own documentation, not in a picker a non-technical user scans by name.
 */
function ModelOptionBody({
  option,
  isDefault,
  isCurrent,
}: {
  option: ComposerModelOption;
  isDefault: boolean;
  isCurrent: boolean;
}) {
  return (
    <>
      <span className="flex min-w-0 flex-1 items-center gap-1.5">
        <span className="truncate font-medium">{option.label}</span>
        {isDefault && (
          <Star
            role="img"
            aria-label="Your default for new chats"
            className="size-3.5 shrink-0 fill-current text-warning"
          />
        )}
      </span>
      <Check
        className={cn(
          "size-4 shrink-0",
          isCurrent ? "text-foreground" : "text-transparent",
        )}
      />
    </>
  );
}

/** Human name for a provider id as the group heading. */
const PROVIDER_NAMES: Record<string, string> = {
  anthropic: "Anthropic",
  openai: "OpenAI",
  openrouter: "OpenRouter",
  toolhive: "ToolHive",
  mock: "Offline mock",
};
function providerGroupLabel(providerId: string): string {
  if (!providerId) return "Other";
  return (
    PROVIDER_NAMES[providerId] ??
    providerId.charAt(0).toUpperCase() + providerId.slice(1)
  );
}

/** The real models grouped by provider, in first-seen provider order. */
function groupModelsByProvider(
  options: readonly ComposerModelOption[],
): { providerId: string; label: string; options: ComposerModelOption[] }[] {
  const groups = new Map<string, ComposerModelOption[]>();
  for (const option of options) {
    const key = option.providerId ?? "";
    const bucket = groups.get(key);
    if (bucket) bucket.push(option);
    else groups.set(key, [option]);
  }
  return [...groups.entries()].map(([providerId, items]) => ({
    providerId,
    label: providerGroupLabel(providerId),
    options: items,
  }));
}

/** The set/clear label for the default action aimed at `option`. */
function defaultActionLabel(
  option: ComposerModelOption | null,
  studioDefault: DefaultModel | null,
  listed: readonly ComposerModelOption[],
): string | null {
  // A default the inventory no longer lists (a stale pick) can only be
  // cleared, so that action wins over "set the highlighted row".
  if (
    studioDefault &&
    !listed.some((candidate) => isDefaultOption(candidate, studioDefault))
  ) {
    return "Clear my default";
  }
  if (option && option.id !== "" && option.providerId) {
    return isDefaultOption(option, studioDefault)
      ? "Clear my default"
      : `Set ${option.label} as my default`;
  }
  return studioDefault ? "Clear my default" : null;
}

export interface ModelPickerListProps {
  /** Every option, the auto row (id "") first. */
  options: readonly ComposerModelOption[];
  /** The model in force ("" = auto): the live session's, or the draft's
   *  resolved pick, for the checkmark. */
  selectedId: string;
  /** Label of the model in force (kept for callers; the list shows no header). */
  currentLabel: string;
  provenance: ModelProvenance;
  /** Live chat: a pick forks the chat onto the model. */
  live: boolean;
  onPick: (option: ComposerModelOption) => void;
}

/**
 * The desktop Model submenu — renders the `DropdownMenuSubContent` itself so
 * it can own the filter state the two-stage Escape needs.
 */
export function ModelSubmenuContent({
  options,
  selectedId,
  onPick,
}: ModelPickerListProps) {
  const [filter, setFilter] = useState("");
  const { defaultModel, setDefaultModel, clearDefaultModel } =
    useDefaultModel();
  // The auto ("Default model") row is deliberately not offered: the list is
  // the daemon's real models only. With no pick in force nothing is checked.
  const real = options.filter((option) => option.id !== "");
  const rows = filterModelOptions(real, filter);
  const visible = rows;
  const groups = groupModelsByProvider(rows);
  const selectedOption = real.find((option) => option.id === selectedId);
  // cmdk's highlighted row (its "value"), controlled so the footer action
  // and Shift+Enter know which model they aim at; starts on the current one.
  const [highlighted, setHighlighted] = useState(() =>
    selectedOption ? optionValue(selectedOption) : "",
  );
  const highlightedOption =
    visible.find((option) => optionValue(option) === highlighted) ?? null;
  const inputRef = useRef<HTMLInputElement>(null);

  // Radix focuses the submenu container for keyboard users AFTER the
  // input's own autoFocus; take the focus back a frame later so typing
  // always lands in the filter.
  useEffect(() => {
    const frame = requestAnimationFrame(() => inputRef.current?.focus());
    return () => cancelAnimationFrame(frame);
  }, []);

  const toggleDefault = (option: ComposerModelOption | null) => {
    const stale =
      defaultModel !== null &&
      !real.some((candidate) => isDefaultOption(candidate, defaultModel));
    if (stale) {
      clearDefaultModel();
    } else if (option && option.id !== "" && option.providerId) {
      if (isDefaultOption(option, defaultModel)) clearDefaultModel();
      else
        setDefaultModel({ modelId: option.id, providerId: option.providerId });
    } else if (defaultModel) {
      clearDefaultModel();
    }
    inputRef.current?.focus();
  };
  const actionLabel = defaultActionLabel(highlightedOption, defaultModel, real);

  return (
    <DropdownMenuSubContent
      className="w-[26rem] max-w-[calc(100vw-2rem)] p-0"
      onEscapeKeyDown={(event) => {
        // Two-stage Escape: clear a filter first, close only when empty.
        if (filter) {
          event.preventDefault();
          setFilter("");
        }
      }}
    >
      <Command
        shouldFilter={false}
        loop
        label={FILTER_MODELS_LABEL}
        value={highlighted}
        onValueChange={setHighlighted}
        className="rounded-none bg-transparent"
        onKeyDown={(event) => {
          // The ctrl+g analogue: Shift+Enter marks the highlighted row as
          // the default instead of picking it.
          if (event.key === "Enter" && event.shiftKey) {
            event.preventDefault();
            event.stopPropagation();
            toggleDefault(highlightedOption);
          }
        }}
      >
        <CommandInput
          ref={inputRef}
          autoFocus
          value={filter}
          onValueChange={setFilter}
          placeholder={FILTER_MODELS_LABEL}
          aria-label={FILTER_MODELS_LABEL}
          onKeyDown={(event) => {
            // With text in the box the horizontal keys move the caret; keep
            // them from Radix, whose ArrowLeft would close the submenu.
            if (
              filter &&
              ["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)
            ) {
              event.stopPropagation();
            }
          }}
        />
        <CommandList className="max-h-72 p-1">
          {groups.map((group) => (
            <CommandGroup
              key={group.providerId || "other"}
              heading={group.label}
              data-testid={`model-group-${group.providerId || "other"}`}
            >
              {group.options.map((option) => {
                const isCurrent = option.id === selectedId;
                return (
                  <CommandItem
                    key={optionValue(option)}
                    value={optionValue(option)}
                    data-current={isCurrent || undefined}
                    onSelect={() => onPick(option)}
                    className={cn(
                      "flex cursor-pointer items-center gap-3 rounded-lg px-3 py-2.5 text-sm",
                      isCurrent && "bg-zinc-100 dark:bg-zinc-800",
                    )}
                  >
                    <ModelOptionBody
                      option={option}
                      isDefault={isDefaultOption(option, defaultModel)}
                      isCurrent={isCurrent}
                    />
                  </CommandItem>
                );
              })}
            </CommandGroup>
          ))}
          {rows.length === 0 && (
            <p className="px-3 py-4 text-center text-sm text-muted-foreground">
              {NO_MODELS_MATCH}
            </p>
          )}
        </CommandList>
        {actionLabel && (
          <div className="flex items-center justify-end border-t px-3 py-2">
            <button
              type="button"
              title="Shift+Enter"
              onClick={() => toggleDefault(highlightedOption)}
              className="rounded-md px-2 py-1 text-xs text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none"
            >
              {actionLabel}
            </button>
          </div>
        )}
      </Command>
    </DropdownMenuSubContent>
  );
}

/**
 * The mobile picker sheet's Model section: heading, live note, the header,
 * a filter box, one tappable row per model with a trailing ★ toggle for the
 * default, and the same "No models match" empty state.
 */
export function ModelSheetSection({
  options,
  selectedId,
  onPick,
}: ModelPickerListProps) {
  const [filter, setFilter] = useState("");
  const { defaultModel, setDefaultModel, clearDefaultModel } =
    useDefaultModel();
  const real = options.filter((option) => option.id !== "");
  const rows = filterModelOptions(real, filter);
  const groups = groupModelsByProvider(rows);

  return (
    <>
      <p className="px-4 pt-3 pb-1 text-xs font-medium text-muted-foreground">
        Model
      </p>
      <div className="px-4 pb-2">
        <InputSearch
          value={filter}
          onChange={setFilter}
          placeholder={FILTER_MODELS_LABEL}
          aria-label={FILTER_MODELS_LABEL}
          className="w-full"
        />
      </div>
      {groups.map((group) => (
        <div key={group.providerId || "other"}>
          <p className="px-4 pt-2 pb-1 text-xs text-muted-foreground">
            {group.label}
          </p>
          {group.options.map((option) => {
            const isCurrent = option.id === selectedId;
            const isDefault = isDefaultOption(option, defaultModel);
            const canDefault = option.id !== "" && Boolean(option.providerId);
            return (
              <div key={optionValue(option)} className="flex items-stretch">
                <button
                  type="button"
                  onClick={() => onPick(option)}
                  aria-current={isCurrent || undefined}
                  className="flex min-w-0 flex-1 items-center gap-3 px-4 py-3 text-sm transition-colors hover:bg-muted/50"
                >
                  <ModelOptionBody
                    option={option}
                    isDefault={isDefault}
                    isCurrent={isCurrent}
                  />
                </button>
                {canDefault && (
                  <button
                    type="button"
                    onClick={() =>
                      isDefault
                        ? clearDefaultModel()
                        : setDefaultModel({
                            modelId: option.id,
                            providerId: option.providerId ?? "",
                          })
                    }
                    aria-label={
                      isDefault
                        ? "Clear my default"
                        : `Set ${option.label} as my default`
                    }
                    aria-pressed={isDefault}
                    className="flex shrink-0 items-center px-3 text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground"
                  >
                    <Star
                      className={cn(
                        "size-4",
                        isDefault && "fill-current text-warning",
                      )}
                    />
                  </button>
                )}
              </div>
            );
          })}
        </div>
      ))}
      {rows.length === 0 && (
        <p className="px-4 py-4 text-center text-sm text-muted-foreground">
          {NO_MODELS_MATCH}
        </p>
      )}
    </>
  );
}
