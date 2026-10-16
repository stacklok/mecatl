// SPDX-License-Identifier: Apache-2.0

import {
  Bot,
  Check,
  ChevronLeft,
  ChevronRight,
  Paperclip,
  Shield,
  SlidersHorizontal,
  X,
} from "lucide-react";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { Button } from "../../components/ui/button";
import { Sheet, SheetClose, SheetContent, SheetTitle } from "../../components/ui/sheet";
import { Toggle } from "../../components/ui/toggle";
import { MODE_OPTIONS, modeTitle, type SessionPermissionMode } from "../../lib/permission-mode";
import { EFFORT_OPTIONS, effortLabel } from "../../lib/reasoning-effort";
import { cn } from "../../lib/utils";
import { useShortcutSuppression } from "../shortcuts/shortcut-provider";
import type { ComposerModelOption } from "./chat-composer";
import type { ComposerOption } from "./composer-option-menu";
import { ModelSheetSection, modelLabel } from "./model-effort-menu";

type ModelSelection = { id: string; providerId: string };

export interface ComposerModeControl {
  disabled: boolean;
  onChange: (mode: SessionPermissionMode) => void;
  value: SessionPermissionMode;
}

export interface ComposerModelControl {
  disabled: boolean;
  effort: string;
  groupedModels: Map<string, ComposerModelOption[]>;
  model?: ModelSelection;
  onEffortChange: (effort: string) => void;
  onModelChange: (model: ModelSelection | undefined) => void;
  /** A draft's "Reset to default". A live chat has none, and no Default row either. */
  onReset?: () => void;
}

const rowClass =
  "flex w-full items-center gap-3 px-4 py-3 text-left text-sm transition-colors hover:bg-muted/50 disabled:opacity-50";

/** Undoes the Toggle's own size, shape, ink and pressed fill on a `rowClass` row. */
const toggleRowClass =
  "h-auto min-w-0 justify-start rounded-none font-normal whitespace-normal hover:text-current data-[state=on]:bg-transparent data-[state=on]:text-current data-[state=on]:hover:bg-muted/50";

/** The Effort pill, brand-tinted while pressed, with no hover fill. */
const effortPillClass =
  "h-auto min-h-8 min-w-0 rounded-full border px-3 py-1 text-xs font-normal whitespace-normal transition-none hover:bg-transparent hover:text-current data-[state=on]:border-brand data-[state=on]:bg-brand/10 data-[state=on]:text-brand data-[state=on]:hover:bg-brand/10 data-[state=on]:hover:text-brand";

/**
 * The phone composer's options, from the prototype's `MobileComposerMenu`
 * (`stack-08`): a sliders button opening a bottom sheet with "Add a file" and
 * one row per setting. Each setting drills into its own view of the same
 * sheet, and a pick closes the sheet. It replaces the native selects the
 * sheet used to hold, except a draft's Execution picker, which the prototype
 * has no view for: it sits inline in the main view.
 */
export function ComposerOptionsSheet({
  attachDisabled,
  disabled,
  execution,
  footer,
  mode,
  model,
  onAddFile,
}: {
  attachDisabled: boolean;
  disabled: boolean;
  /** A draft's Execution picker. */
  execution?: ReactNode;
  /** A live chat's usage and Compact action. */
  footer?: ReactNode;
  mode?: ComposerModeControl;
  model?: ComposerModelControl;
  onAddFile: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [view, setView] = useState<"main" | "mode" | "model">("main");
  const trigger = useRef<HTMLButtonElement>(null);
  const restoreFocus = useRef(true);

  // A CSS-hidden open modal still owns its overlay and focus trap. Close when the
  // desktop layout takes over, where this sheet has no trigger or visible content.
  useEffect(() => {
    const desktop = window.matchMedia("(min-width: 640px)");
    const closeOnDesktop = () => {
      if (!desktop.matches) return;
      restoreFocus.current = false;
      setOpen(false);
      setView("main");
    };
    desktop.addEventListener("change", closeOnDesktop);
    closeOnDesktop();
    return () => desktop.removeEventListener("change", closeOnDesktop);
  }, []);

  // A modal sheet: app shortcuts must not act on the chat behind it.
  useShortcutSuppression(open);

  function close(returnFocus = true) {
    restoreFocus.current = returnFocus;
    setOpen(false);
    setView("main");
  }

  return (
    <>
      <Button
        aria-label="Composer options"
        className="size-11 shrink-0 rounded-full border-0 bg-transparent text-muted-foreground shadow-none hover:bg-muted/60 min-[640px]:hidden"
        disabled={disabled}
        onClick={() => setOpen(true)}
        ref={trigger}
        size="icon"
        type="button"
        variant="ghost"
      >
        <SlidersHorizontal aria-hidden="true" />
      </Button>
      <Sheet
        onOpenChange={(next) => {
          if (next) setOpen(true);
          else close();
        }}
        open={open}
      >
        <SheetContent
          className="max-h-[min(85dvh,40rem)] overflow-y-auto p-0 pb-[env(safe-area-inset-bottom)] min-[640px]:hidden"
          onCloseAutoFocus={(event) => {
            if (!restoreFocus.current) {
              restoreFocus.current = true;
              return;
            }
            event.preventDefault();
            trigger.current?.focus();
          }}
          side="bottom"
        >
          <SheetTitle className="sr-only">Composer options</SheetTitle>
          <SheetClose asChild>
            <Button
              aria-label="Close composer options"
              className="sr-only focus-visible:not-sr-only focus-visible:absolute focus-visible:top-2 focus-visible:right-2"
              size="icon"
              type="button"
              variant="ghost"
            >
              <X aria-hidden="true" />
            </Button>
          </SheetClose>
          {view === "main" && (
            <div className="py-2">
              <button
                className={rowClass}
                disabled={attachDisabled}
                onClick={() => {
                  close(false);
                  onAddFile();
                }}
                type="button"
              >
                <Paperclip aria-hidden="true" className="size-4 text-muted-foreground" />
                Add a file
              </button>
              {mode && (
                <button
                  className={rowClass}
                  disabled={mode.disabled}
                  onClick={() => setView("mode")}
                  type="button"
                >
                  <Shield aria-hidden="true" className="size-4 text-muted-foreground" />
                  <span className="flex-1">Mode</span>
                  <span
                    aria-hidden="true"
                    className={cn(
                      "size-1.5 shrink-0 rounded-full",
                      MODE_OPTIONS.find((option) => option.value === mode.value)?.dotClassName,
                    )}
                  />
                  <span className="text-muted-foreground">{modeTitle(mode.value)}</span>
                  <ChevronRight aria-hidden="true" className="size-4 text-muted-foreground" />
                </button>
              )}
              {model && (
                <button
                  className={rowClass}
                  disabled={model.disabled}
                  onClick={() => setView("model")}
                  type="button"
                >
                  <Bot aria-hidden="true" className="size-4 text-muted-foreground" />
                  <span className="flex-1">Model</span>
                  <span className="min-w-0 truncate text-muted-foreground">
                    {modelLabel(model.groupedModels, model.model)} · {effortLabel(model.effort)}
                  </span>
                  <ChevronRight
                    aria-hidden="true"
                    className="size-4 shrink-0 text-muted-foreground"
                  />
                </button>
              )}
              {execution && <div className="px-4 py-3">{execution}</div>}
              {footer}
            </div>
          )}
          {view === "mode" && mode && (
            <OptionView
              disabled={mode.disabled}
              items={MODE_OPTIONS}
              onBack={() => setView("main")}
              onSelect={(value) => {
                mode.onChange(value);
                close();
              }}
              title="Mode"
              value={mode.value}
            />
          )}
          {view === "model" && model && (
            <div className="flex flex-col">
              <BackButton label="Model" onBack={() => setView("main")} />
              <ModelSheetSection
                allowUnset={model.onReset !== undefined}
                disabled={model.disabled}
                groupedModels={model.groupedModels}
                model={model.model}
                onModelChange={(next) => {
                  model.onModelChange(next);
                  close();
                }}
              />
              <fieldset className="min-w-0 border-t px-4 py-3">
                <legend className="float-left mb-2 w-full text-xs font-medium uppercase tracking-wide text-muted-foreground">
                  Effort
                </legend>
                <div className="clear-left flex flex-wrap gap-1.5">
                  {EFFORT_OPTIONS.map((option) => (
                    // Separate Toggles, not a single-choice ToggleGroup: that
                    // would turn the pills into radios with roving focus.
                    // Pressing the current pill picks it again.
                    <Toggle
                      className={effortPillClass}
                      disabled={model.disabled}
                      key={option.value}
                      onPressedChange={() => {
                        model.onEffortChange(option.value);
                        close();
                      }}
                      pressed={option.value === model.effort}
                    >
                      {option.label}
                    </Toggle>
                  ))}
                </div>
              </fieldset>
              {model.onReset && (
                <button
                  className="border-t px-4 py-3 text-left text-sm text-muted-foreground transition-colors hover:bg-muted/50 disabled:opacity-50"
                  disabled={
                    model.disabled || (model.model === undefined && model.effort === "default")
                  }
                  onClick={() => {
                    model.onReset?.();
                    close();
                  }}
                  type="button"
                >
                  Reset to default
                </button>
              )}
            </div>
          )}
        </SheetContent>
      </Sheet>
    </>
  );
}

function BackButton({ label, onBack }: { label: string; onBack: () => void }) {
  return (
    <button
      aria-label={`Back from ${label}`}
      className="flex w-full items-center gap-2 px-4 py-3 text-sm font-medium"
      onClick={onBack}
      type="button"
    >
      <ChevronLeft aria-hidden="true" className="size-4" />
      {label}
    </button>
  );
}

/** One setting's choices. `disabled` follows the setting's pill, so a run or a pending change that starts while the view is open blocks it. */
function OptionView<V extends string>({
  disabled,
  items,
  onBack,
  onSelect,
  title,
  value,
}: {
  disabled: boolean;
  items: ComposerOption<V>[];
  onBack: () => void;
  onSelect: (value: V) => void;
  title: string;
  value: V;
}) {
  return (
    <div className="py-2">
      <BackButton label={title} onBack={onBack} />
      {items.map((option) => (
        // One Toggle per choice, as for the Effort pills; pressing the
        // current choice picks it again.
        <Toggle
          className={cn(rowClass, toggleRowClass)}
          disabled={disabled}
          key={option.value}
          onPressedChange={() => onSelect(option.value)}
          pressed={option.value === value}
        >
          {option.dotClassName && (
            <span
              aria-hidden="true"
              className={cn("size-1.5 shrink-0 rounded-full", option.dotClassName)}
            />
          )}
          <span className="flex-1">
            <span className="block font-medium">{option.title}</span>
            <span className="block text-xs text-muted-foreground">{option.description}</span>
          </span>
          {option.value === value && <Check aria-hidden="true" className="size-4 shrink-0" />}
        </Toggle>
      ))}
    </div>
  );
}
