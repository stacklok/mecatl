"use client";

import { ArrowUp, Diamond, Paperclip, Plus, Square } from "lucide-react";
import type { ChangeEvent, DragEvent, FormEvent, KeyboardEvent, RefObject } from "react";

import {
  ModelEffortSelector,
  type EffortId,
  type ModelOption,
  type ModelSelection,
} from "@/components/chat/model-effort-selector";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";

/**
 * Ghost trigger for the dropdowns BELOW the input box — no pill background, a
 * subtle hover, and a small chevron. Copied from the console's composer so the
 * two read identically.
 */
const GHOST_TRIGGER =
  "h-7 gap-1 rounded-full border-0 bg-transparent px-2.5 text-sm font-normal text-foreground shadow-none hover:bg-accent hover:text-accent-foreground";

export type CsvAttachmentSummary = {
  id: string;
  name: string;
  size: number;
  rows: number;
  columns: number;
};

/**
 * The composer. Three nested boxes, and the nesting IS the design:
 *
 *  1. outer wrapper — the drop zone, and the tray colour behind the toolbar;
 *  2. input box     — its own rounded border: banners, chip, textarea, send row;
 *  3. toolbar row   — pulled up with `-mt-4 pt-5` and `border-t-0` so its top
 *                     edge slides behind the input box's bottom corners and the
 *                     two borders read as ONE continuous outline.
 *
 * Studio keeps its stop control (the console's composer has none — it queues
 * instead), rendered as the streaming `Square` so a run is always cancellable.
 */
export function Composer({
  prompt,
  onPromptChange,
  onSubmit,
  running,
  onCancel,
  mode,
  onModeChange,
  models,
  modelSelection,
  onSelectModel,
  effort,
  onSelectEffort,
  sessionModelLabel,
  defaultModelLabel,
  csvAttachment,
  onRemoveCsv,
  onCsvInput,
  onCsvDrop,
  dragging,
  onDraggingChange,
  textareaRef,
  csvInputRef,
  formatBytes,
  placeholder,
}: {
  prompt: string;
  onPromptChange: (value: string) => void;
  onSubmit: (event?: FormEvent) => void;
  running: boolean;
  onCancel: () => void;
  mode: "default" | "plan";
  onModeChange: (mode: "default" | "plan") => void;
  models: readonly ModelOption[];
  modelSelection: ModelSelection;
  onSelectModel: (selection: ModelSelection) => void;
  effort: EffortId;
  onSelectEffort: (effort: EffortId) => void;
  sessionModelLabel?: string;
  defaultModelLabel?: string;
  csvAttachment: CsvAttachmentSummary | null;
  onRemoveCsv: () => void;
  onCsvInput: (event: ChangeEvent<HTMLInputElement>) => void;
  onCsvDrop: (event: DragEvent<HTMLElement>) => void;
  dragging: boolean;
  onDraggingChange: (dragging: boolean) => void;
  textareaRef: RefObject<HTMLTextAreaElement | null>;
  csvInputRef: RefObject<HTMLInputElement | null>;
  formatBytes: (bytes: number) => string;
  placeholder: string;
}) {
  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key !== "Enter") return;
    // An IME candidate window also fires Enter; committing there must not send.
    if (event.nativeEvent.isComposing) return;
    if (event.shiftKey) return;
    event.preventDefault();
    onSubmit();
  };

  return (
    <form
      onSubmit={onSubmit}
      onDragEnter={(event) => {
        event.preventDefault();
        onDraggingChange(true);
      }}
      onDragOver={(event) => {
        event.preventDefault();
        event.dataTransfer.dropEffect = "copy";
      }}
      onDragLeave={(event) => {
        if (event.currentTarget === event.target) onDraggingChange(false);
      }}
      onDrop={onCsvDrop}
      className="relative rounded-2xl bg-secondary"
    >
      <input
        ref={csvInputRef}
        type="file"
        accept=".csv,text/csv,application/vnd.ms-excel"
        onChange={onCsvInput}
        className="hidden"
        aria-label="Choose CSV file"
      />

      <div
        className={cn(
          "relative rounded-2xl border bg-background transition-colors focus-within:border-ring",
          dragging
            ? "border-brand bg-brand/5 ring-2 ring-brand/20 dark:bg-brand/10"
            : "border-input",
        )}
      >
        {dragging && (
          <div className="pointer-events-none absolute inset-0 z-20 flex items-center justify-center rounded-2xl bg-brand/10 dark:bg-brand/15">
            <div className="flex items-center gap-2 text-brand">
              <Paperclip className="size-5" />
              <span className="text-sm font-medium">Drop a CSV to attach</span>
            </div>
          </div>
        )}

        {csvAttachment && (
          <div className="flex flex-wrap gap-1.5 px-4 pt-3" role="status">
            <span className="inline-flex items-center gap-1.5 rounded-full border border-brand/30 bg-brand/5 py-1 pr-1.5 pl-2.5 text-xs text-brand">
              <Paperclip className="size-3" />
              {csvAttachment.name}
              <span className="text-brand/70">
                {csvAttachment.rows} rows · {csvAttachment.columns} cols ·{" "}
                {formatBytes(csvAttachment.size)}
              </span>
              <button
                type="button"
                onClick={onRemoveCsv}
                aria-label={`Remove ${csvAttachment.name}`}
                className="flex size-4 items-center justify-center rounded-full hover:bg-brand/10"
              >
                ×
              </button>
            </span>
          </div>
        )}

        <div className="flex flex-wrap items-start gap-1.5 px-4 pt-4 pb-2">
          <textarea
            ref={textareaRef}
            value={prompt}
            onChange={(event) => onPromptChange(event.target.value)}
            onKeyDown={onKeyDown}
            rows={1}
            placeholder={placeholder}
            aria-label="Task prompt"
            // field-sizing-content grows the textarea with its content in pure
            // CSS — no JS height measurement, no layout thrash while streaming.
            className="min-h-6 max-h-48 w-full flex-1 resize-none self-center border-0 bg-transparent text-sm [field-sizing:content] placeholder:text-muted-foreground/60 focus:outline-none disabled:opacity-50"
          />
        </div>

        <div className="flex items-center gap-1 px-2 pb-2">
          <Button
            type="button"
            size="icon"
            onClick={() => csvInputRef.current?.click()}
            aria-label="Attach CSV file"
            title="Attach CSV file"
            className="size-8 rounded-full border-0 bg-transparent text-muted-foreground shadow-none hover:bg-muted/60"
          >
            <Plus className="size-4" />
          </Button>
          {running ? (
            <Button
              type="button"
              size="icon"
              onClick={onCancel}
              aria-label="Stop task"
              className="ml-auto size-8 rounded-full bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              <Square className="size-4" />
            </Button>
          ) : (
            <Button
              type="submit"
              size="icon"
              disabled={!prompt.trim() && !csvAttachment}
              aria-label="Send prompt"
              className="ml-auto size-8 rounded-full bg-brand text-brand-foreground hover:bg-brand/90 disabled:opacity-50"
            >
              <ArrowUp className="size-4" />
            </Button>
          )}
        </div>
      </div>

      {/* Pulled up behind the input box so the outlines merge into one. */}
      <div className="hide-scrollbar -mt-4 flex items-center gap-1 overflow-x-auto rounded-b-2xl border border-t-0 border-input bg-secondary px-2 pt-5 pb-1.5">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button type="button" size="sm" className={GHOST_TRIGGER}>
              <Diamond className="size-3.5 text-muted-foreground" />
              {mode === "plan" ? "Plan" : "Agent"}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-72 p-2">
            {(
              [
                {
                  value: "default",
                  label: "Agent",
                  description: "Read, run commands, and edit files — with approvals.",
                },
                {
                  value: "plan",
                  label: "Plan",
                  description: "Investigate and propose only. Mutations are denied.",
                },
              ] as const
            ).map((option) => (
              <DropdownMenuItem
                key={option.value}
                onSelect={() => onModeChange(option.value)}
                className={cn(
                  "flex cursor-pointer flex-col items-start gap-0.5 rounded-lg px-3 py-2.5",
                  mode === option.value && "bg-accent",
                )}
              >
                <span className="text-sm font-medium">{option.label}</span>
                <span className="text-xs text-muted-foreground">
                  {option.description}
                </span>
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>

        <ModelEffortSelector
          models={models}
          selection={modelSelection}
          onSelectModel={onSelectModel}
          effort={effort}
          onSelectEffort={onSelectEffort}
          sessionModelLabel={sessionModelLabel}
          defaultModelLabel={defaultModelLabel}
        />
      </div>
    </form>
  );
}
