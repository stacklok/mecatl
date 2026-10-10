// SPDX-License-Identifier: Apache-2.0

import {
  Bot,
  Check,
  ChevronLeft,
  ChevronRight,
  Paperclip,
  Shield,
  SlidersHorizontal,
  Wrench,
  X,
} from "lucide-react";
import { type ReactNode, useRef, useState } from "react";
import { Button } from "../../components/ui/button";
import { Sheet, SheetClose, SheetContent, SheetTitle } from "../../components/ui/sheet";
import { MODE_OPTIONS, modeTitle, type SessionPermissionMode } from "../../lib/permission-mode";
import { EFFORT_OPTIONS, effortLabel } from "../../lib/reasoning-effort";
import { cn } from "../../lib/utils";
import type { ComposerModelOption, DraftChatConfiguration } from "./chat-composer";
import type { ComposerOption } from "./composer-option-menu";
import { ModelSheetSection, modelLabel } from "./model-effort-menu";

type ToolAccess = DraftChatConfiguration["toolAccess"];
type ModelSelection = { id: string; providerId: string };

export const TOOL_OPTIONS: ComposerOption<ToolAccess>[] = [
  {
    description: "The agent can use every tool, including file and shell access.",
    title: "All",
    value: "all",
  },
  {
    description: "No file or shell tools; other tools stay available.",
    title: "No filesystem",
    value: "noFilesystem",
  },
];

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

export interface ComposerToolsControl {
  disabled: boolean;
  onChange: (toolAccess: ToolAccess) => void;
  value: ToolAccess;
}

const rowClass =
  "flex w-full items-center gap-3 px-4 py-3 text-left text-sm transition-colors hover:bg-muted/50 disabled:opacity-50";

/**
 * The phone composer's options, from the prototype's `MobileComposerMenu`
 * (`stack-08`): a sliders button opening a bottom sheet with "Add a file" and
 * one row per setting. Each setting drills into its own view of the same
 * sheet, and a pick closes the sheet. It replaces the native selects the
 * sheet used to hold.
 */
export function ComposerOptionsSheet({
  attachDisabled,
  disabled,
  footer,
  mode,
  model,
  onAddFile,
  tools,
}: {
  attachDisabled: boolean;
  disabled: boolean;
  /** A live chat's usage and Compact action. */
  footer?: ReactNode;
  mode?: ComposerModeControl;
  model?: ComposerModelControl;
  onAddFile: () => void;
  tools?: ComposerToolsControl;
}) {
  const [open, setOpen] = useState(false);
  const [view, setView] = useState<"main" | "mode" | "model" | "tools">("main");
  const trigger = useRef<HTMLButtonElement>(null);
  const restoreFocus = useRef(true);

  function close(returnFocus = true) {
    restoreFocus.current = returnFocus;
    setOpen(false);
    setView("main");
  }

  return (
    <>
      <Button
        aria-label="Composer options"
        className="size-8 shrink-0 rounded-full border-0 bg-transparent text-muted-foreground shadow-none hover:bg-muted/60 min-[500px]:hidden"
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
          className="max-h-[min(85dvh,40rem)] overflow-y-auto p-0 pb-[env(safe-area-inset-bottom)] min-[500px]:hidden"
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
              {tools && (
                <button
                  className={rowClass}
                  disabled={tools.disabled}
                  onClick={() => setView("tools")}
                  type="button"
                >
                  <Wrench aria-hidden="true" className="size-4 text-muted-foreground" />
                  <span className="flex-1">Tools</span>
                  <span className="text-muted-foreground">
                    {TOOL_OPTIONS.find((option) => option.value === tools.value)?.title ?? "All"}
                  </span>
                  <ChevronRight aria-hidden="true" className="size-4 text-muted-foreground" />
                </button>
              )}
              {footer}
            </div>
          )}
          {view === "mode" && mode && (
            <OptionView
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
          {view === "tools" && tools && (
            <OptionView
              items={TOOL_OPTIONS}
              onBack={() => setView("main")}
              onSelect={(value) => {
                tools.onChange(value);
                close();
              }}
              title="Tools"
              value={tools.value}
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
                    <button
                      aria-pressed={option.value === model.effort}
                      className={cn(
                        "min-h-8 rounded-full border px-3 py-1 text-xs disabled:opacity-50",
                        option.value === model.effort && "border-brand bg-brand/10 text-brand",
                      )}
                      disabled={model.disabled}
                      key={option.value}
                      onClick={() => {
                        model.onEffortChange(option.value);
                        close();
                      }}
                      type="button"
                    >
                      {option.label}
                    </button>
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

function OptionView<V extends string>({
  items,
  onBack,
  onSelect,
  title,
  value,
}: {
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
        <button
          aria-pressed={option.value === value}
          className={rowClass}
          key={option.value}
          onClick={() => onSelect(option.value)}
          type="button"
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
        </button>
      ))}
    </div>
  );
}
