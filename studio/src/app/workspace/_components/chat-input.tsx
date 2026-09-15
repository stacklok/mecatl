"use client";

import Placeholder from "@tiptap/extension-placeholder";
import { EditorContent, useEditor } from "@tiptap/react";
import StarterKit from "@tiptap/starter-kit";
import type { SuggestionProps } from "@tiptap/suggestion";
import {
  ArrowUp,
  Bot,
  Brain,
  Check,
  ChevronDown,
  ChevronRight,
  FolderClosed,
  FolderPlus,
  Mic,
  Paperclip,
  Plus,
  RotateCcw,
  Shield,
  SlidersHorizontal,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import {
  getAgentMentions,
  getSlashCommands,
} from "@/features/agent/composer-capabilities";
import { usePrompt } from "@/hooks/use-prompt";
import { fileKindMeta } from "@/lib/file-meta";
import {
  type EnterSendBehavior,
  useEnterSendBehavior,
} from "@/lib/profile-preferences";
import type { SessionPermissionMode } from "@/lib/protocol";
import { cn } from "@/lib/utils";
import {
  type ComposerMenuItem,
  composerText,
  createComposerMentions,
  setComposerText,
} from "./composer-mentions";

interface ProjectItem {
  id: string;
  name: string;
  color: string;
}

interface ChatInputProps {
  placeholder?: string;
  rows?: number;
  projects?: ProjectItem[];
  selectedProjectId?: string | null;
  onSelectProject?: (id: string | null) => void;
  onCreateProject?: (name: string) => void;
  compact?: boolean;
  /** Docked at a screen edge on mobile: full-bleed single row with only a
      hairline top border, instead of the floating rounded box. */
  mobileDocked?: boolean;
  /** Refocuses the field when this changes (e.g. the open chat's id). */
  focusKey?: string;
  onSend?: (content: string, files?: File[]) => void;
  onQueue?: (content: string) => void;
  /** Injects the text — plus any staged image attachments (ADR 0251) — into
      the in-flight run at the next step (mid-run steering). Only meaningful
      while `isStreaming`; absent when the daemon lacks the steer capability
      (mid-run sends then queue and files stay attached). */
  onSteer?: (content: string, files?: File[]) => void;
  onModelChange?: (alias: string) => void;
  /** Live daemon models for the picker; absent = the sentinel only. */
  models?: ComposerModelOption[];
  /** Label for the empty (daemon-picks) entry. */
  autoModelLabel?: string;
  /** Preview an attached file in the canvas panel. */
  onPreviewAttachment?: (file: File) => void;
  disabled?: boolean;
  isStreaming?: boolean;
  appendText?: string | null;
  onAppendConsumed?: () => void;
  /** Plain-text seed dropped into an empty composer (e.g. a "next step" chip
      that pre-fills a prompt without sending it). Unlike `appendText`, it is
      not quoted and replaces rather than appends. */
  initialText?: string | null;
  onInitialTextConsumed?: () => void;
  /** Display-only model label for live-harness sessions (see ModelSelector). */
  modelLockedLabel?: string;
  /** Live-chat model switch: picking forks the chat onto the model (the
   *  daemon fixes a session's model at create). null = auto-routed. */
  onSwitchModel?: (option: ComposerModelOption | null) => void;
  /** The live session's current model id ("" = auto). */
  currentModelId?: string;
  /** The session's current permission mode, shown by the Mode selector. */
  mode?: SessionPermissionMode;
  /** Renders the Mode selector (first in the control bar) when provided.
      Surfaces without a mode concept (the thread panel, the mock tour chat)
      simply omit it. */
  onModeChange?: (mode: SessionPermissionMode) => void;
}

/**
 * Ghost-style trigger used by dropdowns rendered BELOW the input box
 * (matches the "Build / Claude Opus 5 / Default" reference design).
 * No pill background; subtle hover; small chevron via the trigger itself.
 */
const GHOST_TRIGGER_CLASS =
  "h-7 gap-1 rounded-full px-2.5 text-sm font-normal text-foreground bg-transparent hover:bg-zinc-200 dark:hover:bg-zinc-700 border-0 shadow-none";

/** The composer's "+" button: opens the file picker directly to attach files. */
function FilesDropdown({
  onFilesSelected,
}: {
  onFilesSelected?: (files: File[]) => void;
}) {
  const fileInputRef = useRef<HTMLInputElement>(null);

  return (
    <>
      <input
        ref={fileInputRef}
        type="file"
        multiple
        accept="image/*"
        className="hidden"
        onChange={(e) => {
          const selected = e.target.files;
          if (selected && selected.length > 0) {
            onFilesSelected?.(Array.from(selected));
          }
          e.target.value = "";
        }}
      />
      <Button
        size="icon"
        className="size-8 rounded-full text-muted-foreground bg-transparent hover:bg-muted/60 border-0 shadow-none"
        onClick={() => fileInputRef.current?.click()}
        aria-label="Attach files"
      >
        <Plus className="size-4" />
      </Button>
    </>
  );
}

/**
 * On mobile the composer must NOT grab focus when a chat opens — the keyboard
 * would pop over the transcript the user came to read. Read the breakpoint
 * synchronously (module-stable, so focus effects need not depend on it): the
 * useIsMobile hook reports undefined for a render, which would race the
 * mount-time autofocus.
 */
const mobileViewport = () =>
  typeof window !== "undefined" &&
  window.matchMedia("(max-width: 499px)").matches;

/** The daemon-picks sentinel (model_id omitted at create). Its label is
 *  supplied by the caller: "Auto-routed" only while the router is really on. */
const AUTO_MODEL_ID = "";
const autoModel = (label?: string) => ({
  id: AUTO_MODEL_ID,
  label: label ?? "Default model",
});

export interface ComposerModelOption {
  id: string;
  label: string;
  /** The daemon requires provider_id whenever model_id rides a create. */
  providerId?: string;
}

const EFFORT_LEVELS = [
  { id: "light", label: "Light" },
  { id: "medium", label: "Medium" },
  { id: "high", label: "High" },
  { id: "extra-high", label: "Extra High" },
] as const;

type EffortId = (typeof EFFORT_LEVELS)[number]["id"];

const DEFAULT_EFFORT_ID: EffortId = "medium";

/** One tappable choice row inside a mobile picker sheet (MobileChatMenu's
 *  row idiom plus a trailing checkmark). */
function SheetOptionRow({
  label,
  description,
  selected,
  onSelect,
}: {
  label: string;
  description?: string;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      className="flex w-full items-center gap-3 px-4 py-3 text-sm transition-colors hover:bg-muted/50"
    >
      <span className="flex min-w-0 flex-1 flex-col text-left">
        <span className="truncate font-medium">{label}</span>
        {description && (
          <span className="truncate text-xs text-muted-foreground">
            {description}
          </span>
        )}
      </span>
      <Check
        className={cn(
          "size-4 shrink-0",
          selected ? "text-foreground" : "text-transparent",
        )}
      />
    </button>
  );
}

/** Muted section heading inside a mobile picker sheet. */
function SheetSectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <p className="px-4 pt-3 pb-1 text-xs font-medium text-muted-foreground">
      {children}
    </p>
  );
}

/**
 * Combined model + effort picker in a single menu: the trigger reads
 * "{model} {effort}", and the menu drills into a Model submenu and an Effort
 * submenu, with a reset. When `lockedLabel` is set (a live harness routes the
 * model server-side) the trigger is display-only. On mobile the menu is a
 * bottom sheet instead (the MobileChatMenu convention) with the submenus
 * flattened into sections — it stays open across taps so model and effort
 * can be set in one visit.
 */
function ModelEffortSelector({
  onModelChange,
  lockedLabel,
  onSwitchModel,
  currentModelId,
  models,
  autoModelLabel,
}: {
  onModelChange?: (id: string) => void;
  lockedLabel?: string;
  /** Live-chat switch: picking forks the chat onto the model. */
  onSwitchModel?: (option: ComposerModelOption | null) => void;
  currentModelId?: string;
  models?: ComposerModelOption[];
  autoModelLabel?: string;
}) {
  const modelOptions = [autoModel(autoModelLabel), ...(models ?? [])];
  const [model, setModel] = useState<string>(AUTO_MODEL_ID);
  const [effort, setEffort] = useState<EffortId>(DEFAULT_EFFORT_ID);
  const switchId = currentModelId ?? AUTO_MODEL_ID;
  const selectedModel = onSwitchModel
    ? (modelOptions.find((m) => m.id === switchId) ?? {
        id: switchId,
        label: switchId || autoModel(autoModelLabel).label,
      })
    : (modelOptions.find((m) => m.id === model) ?? modelOptions[0]);
  const selectedEffort =
    EFFORT_LEVELS.find((e) => e.id === effort) ?? EFFORT_LEVELS[1];

  // The toolbar is a CSS container (@container on the footer row): below
  // ~28rem — a narrow side-panel composer, not just mobile viewports — the
  // value labels collapse to the static word "Model", with the full selection
  // kept on the title attribute; in between, truncation caps a long model id.
  if (lockedLabel && !onSwitchModel) {
    return (
      <Button
        size="sm"
        className={GHOST_TRIGGER_CLASS}
        disabled
        title={lockedLabel}
      >
        <span className="max-w-40 truncate @max-md:hidden">{lockedLabel}</span>
        <span className="hidden @max-md:inline">Model</span>
      </Button>
    );
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          size="sm"
          className={GHOST_TRIGGER_CLASS}
          title={
            onSwitchModel
              ? selectedModel.label
              : `${selectedModel.label} · ${selectedEffort.label}`
          }
        >
          <span className="max-w-40 truncate @max-md:hidden">
            {selectedModel.label}
          </span>
          {!onSwitchModel && (
            <span className="max-w-24 truncate text-muted-foreground @max-md:hidden">
              {selectedEffort.label}
            </span>
          )}
          <span className="hidden @max-md:inline">Model</span>
          <ChevronDown className="size-3.5 text-muted-foreground" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        onCloseAutoFocus={(e) => e.preventDefault()}
        align="start"
        className="w-64"
      >
        <DropdownMenuSub>
          <DropdownMenuSubTrigger>
            <span className="flex-1">Model</span>
            <span className="text-muted-foreground">{selectedModel.label}</span>
          </DropdownMenuSubTrigger>
          <DropdownMenuSubContent className="w-80 p-2">
            {onSwitchModel && (
              <p className="px-3 pb-1.5 text-xs text-muted-foreground">
                Picking a model continues this chat in a copy on it.
              </p>
            )}
            {modelOptions.map((m) => {
              const isSelected = m.id === (onSwitchModel ? switchId : model);
              return (
                <DropdownMenuItem
                  key={m.id}
                  className={cn(
                    "flex items-center gap-3 rounded-lg px-3 py-3 text-sm cursor-pointer hover:bg-zinc-100 dark:hover:bg-zinc-800 justify-between",
                    isSelected && "bg-zinc-100 dark:bg-zinc-800",
                  )}
                  onClick={() => {
                    if (onSwitchModel) {
                      if (m.id !== switchId)
                        onSwitchModel(m.id === AUTO_MODEL_ID ? null : m);
                      return;
                    }
                    setModel(m.id);
                    onModelChange?.(m.id);
                  }}
                >
                  <span className="font-medium">{m.label}</span>
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
        {!onSwitchModel && (
          <DropdownMenuSub>
            <DropdownMenuSubTrigger>
              <span className="flex-1">Effort</span>
              <span className="text-muted-foreground">
                {selectedEffort.label}
              </span>
            </DropdownMenuSubTrigger>
            <DropdownMenuSubContent className="w-72 p-2">
              {EFFORT_LEVELS.map((e) => {
                const isSelected = e.id === effort;
                return (
                  <DropdownMenuItem
                    key={e.id}
                    className={cn(
                      "flex items-center gap-3 rounded-lg px-3 py-3 text-sm cursor-pointer hover:bg-zinc-100 dark:hover:bg-zinc-800 justify-between",
                      isSelected && "bg-zinc-100 dark:bg-zinc-800",
                    )}
                    onClick={() => setEffort(e.id)}
                  >
                    <span className="font-medium">{e.label}</span>
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
        )}
        {!onSwitchModel && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              disabled={model === AUTO_MODEL_ID && effort === DEFAULT_EFFORT_ID}
              onClick={() => {
                setModel(AUTO_MODEL_ID);
                setEffort(DEFAULT_EFFORT_ID);
                onModelChange?.(AUTO_MODEL_ID);
              }}
            >
              <RotateCcw className="size-4 mr-2 text-muted-foreground" />
              Reset to default
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

const PERMISSION_MODE_OPTIONS = [
  {
    id: "default",
    label: "Manual",
    description: "Always ask before making changes",
  },
  {
    id: "acceptEdits",
    label: "Accept edits",
    description: "Automatically accept all file edits",
  },
  {
    id: "plan",
    label: "Plan",
    description: "Create a plan before making changes",
  },
] as const satisfies readonly {
  id: SessionPermissionMode;
  label: string;
  description: string;
}[];

/** Display label for a session permission mode. */
function permissionModeLabel(mode: SessionPermissionMode): string {
  return (
    PERMISSION_MODE_OPTIONS.find((option) => option.id === mode)?.label ??
    "Manual"
  );
}

/**
 * The session permission-mode selector (Default / Plan / Accept edits), the
 * first control in the composer bar. Same pill + container-collapse idiom as
 * the model selector: the current mode wide, the bare word "Mode" narrow,
 * always the full selection on the title. Disabled while a run streams — the
 * daemon's session aggregate refuses a mid-turn mode change, so the control
 * matches that reality instead of round-tripping a guaranteed refusal.
 */
function ModeSelector({
  mode,
  onModeChange,
  disabled,
}: {
  mode: SessionPermissionMode;
  onModeChange: (mode: SessionPermissionMode) => void;
  disabled?: boolean;
}) {
  const label = permissionModeLabel(mode);
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          size="sm"
          className={GHOST_TRIGGER_CLASS}
          disabled={disabled}
          title={`Mode: ${label}`}
        >
          <span className="max-w-32 truncate @max-md:hidden">{label}</span>
          <span className="hidden @max-md:inline">Mode</span>
          <ChevronDown className="size-3.5 text-muted-foreground" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        onCloseAutoFocus={(e) => e.preventDefault()}
        align="start"
        className="w-72"
      >
        {PERMISSION_MODE_OPTIONS.map((option) => (
          <DropdownMenuItem
            key={option.id}
            className="items-start gap-2"
            onClick={() => onModeChange(option.id)}
          >
            <Check
              className={cn(
                "mt-0.5 size-4 shrink-0",
                mode === option.id ? "text-foreground" : "text-transparent",
              )}
            />
            <span className="flex min-w-0 flex-col">
              <span>{option.label}</span>
              <span className="text-xs text-muted-foreground">
                {option.description}
              </span>
            </span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/**
 * Controls whether the agent draws on (and writes to) its long-term memory for
 * this conversation. A pill matching the model selector, opening a small On/Off
 * menu; on by default.
 */
function MemoryToggle() {
  const [on, setOn] = useState(true);
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          size="sm"
          className={GHOST_TRIGGER_CLASS}
          title={`Memory ${on ? "On" : "Off"}`}
        >
          Memory
          <span className="text-muted-foreground @max-md:hidden">
            {on ? "On" : "Off"}
          </span>
          <ChevronDown className="size-3.5 text-muted-foreground" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        onCloseAutoFocus={(e) => e.preventDefault()}
        align="start"
        className="w-40"
      >
        {[true, false].map((value) => (
          <DropdownMenuItem
            key={String(value)}
            className="gap-2"
            onClick={() => setOn(value)}
          >
            <Check
              className={cn(
                "size-4",
                on === value ? "text-foreground" : "text-transparent",
              )}
            />
            {value ? "On" : "Off"}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/**
 * The mobile composer's left-hand options button: a + that opens a bottom
 * sheet with the composer's secondary actions — Add a file, Model, Memory —
 * replacing the desktop toolbar row (hidden on mobile). Model and Memory
 * drill into their own sheets; a routed model renders display-only.
 */
function MobileComposerMenu({
  onFilesSelected,
  onModelChange,
  modelLockedLabel,
  onSwitchModel,
  currentModelId,
  mode,
  onModeChange,
  modeDisabled,
  models,
  autoModelLabel,
}: {
  onFilesSelected: (files: File[]) => void;
  onModelChange?: (id: string) => void;
  modelLockedLabel?: string;
  /** Present in a live chat: picking forks the chat onto the model (the
   *  daemon fixes a session's model at create). null = auto-routed. */
  onSwitchModel?: (option: ComposerModelOption | null) => void;
  /** The live session's current model id ("" = auto), for the checkmark. */
  currentModelId?: string;
  mode?: SessionPermissionMode;
  onModeChange?: (mode: SessionPermissionMode) => void;
  modeDisabled?: boolean;
  models?: ComposerModelOption[];
  autoModelLabel?: string;
}) {
  const [menuOpen, setMenuOpen] = useState(false);
  const [sub, setSub] = useState<"mode" | "model" | "memory" | null>(null);
  const [model, setModel] = useState<string>(AUTO_MODEL_ID);
  const [effort, setEffort] = useState<EffortId>(DEFAULT_EFFORT_ID);
  const [memoryOn, setMemoryOn] = useState(true);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const modelOptions = [autoModel(autoModelLabel), ...(models ?? [])];
  const selectedModel =
    modelOptions.find((m) => m.id === model) ?? modelOptions[0];
  // Switch mode ignores the local pick state — the session's model is the
  // truth, and a stale/disabled id still labels honestly as itself.
  const switchId = currentModelId ?? AUTO_MODEL_ID;
  const switchSelected = modelOptions.find((m) => m.id === switchId) ?? {
    id: switchId,
    label: switchId || autoModel(autoModelLabel).label,
  };
  const selectedEffort =
    EFFORT_LEVELS.find((e) => e.id === effort) ?? EFFORT_LEVELS[1];

  const menuRow =
    "flex w-full items-center gap-3 px-4 py-3 text-sm transition-colors hover:bg-muted/50 disabled:opacity-50";

  return (
    <>
      <input
        ref={fileInputRef}
        type="file"
        multiple
        accept="image/*"
        className="hidden"
        onChange={(event) => {
          const files = Array.from(event.target.files ?? []);
          event.target.value = "";
          if (files.length > 0) onFilesSelected(files);
        }}
      />
      <Button
        size="icon"
        className="size-8 rounded-full border-0 bg-transparent text-muted-foreground shadow-none hover:bg-muted/60"
        onClick={() => setMenuOpen(true)}
        aria-label="Composer options"
      >
        {/* Not a +: on desktop + means attach, here it opens the options
            menu, so it gets a distinct options glyph. */}
        <SlidersHorizontal className="size-4" />
      </Button>

      <Sheet open={menuOpen} onOpenChange={setMenuOpen}>
        <SheetContent side="bottom" className="p-0">
          <SheetTitle className="sr-only">Composer options</SheetTitle>
          <div className="py-2">
            <button
              type="button"
              className={menuRow}
              onClick={() => {
                setMenuOpen(false);
                fileInputRef.current?.click();
              }}
            >
              <Paperclip className="size-4 text-muted-foreground" />
              Add a file
            </button>
            {onModeChange && (
              <button
                type="button"
                className={menuRow}
                disabled={modeDisabled}
                onClick={() => {
                  setMenuOpen(false);
                  setSub("mode");
                }}
              >
                <Shield className="size-4 text-muted-foreground" />
                <span className="flex-1 text-left">Mode</span>
                <span className="text-muted-foreground">
                  {permissionModeLabel(mode ?? "default")}
                </span>
                {!modeDisabled && (
                  <ChevronRight className="size-4 text-muted-foreground/60" />
                )}
              </button>
            )}
            <button
              type="button"
              className={menuRow}
              disabled={!!modelLockedLabel && !onSwitchModel}
              onClick={() => {
                setMenuOpen(false);
                setSub("model");
              }}
            >
              <Bot className="size-4 text-muted-foreground" />
              <span className="flex-1 text-left">Model</span>
              <span className="text-muted-foreground">
                {onSwitchModel
                  ? switchSelected.label
                  : (modelLockedLabel ??
                    `${selectedModel.label} ${selectedEffort.label}`)}
              </span>
              {(!modelLockedLabel || onSwitchModel) && (
                <ChevronRight className="size-4 text-muted-foreground/60" />
              )}
            </button>
            <button
              type="button"
              className={menuRow}
              onClick={() => {
                setMenuOpen(false);
                setSub("memory");
              }}
            >
              <Brain className="size-4 text-muted-foreground" />
              <span className="flex-1 text-left">Memory</span>
              <span className="text-muted-foreground">
                {memoryOn ? "On" : "Off"}
              </span>
              <ChevronRight className="size-4 text-muted-foreground/60" />
            </button>
          </div>
        </SheetContent>
      </Sheet>

      <Sheet
        open={sub === "mode"}
        onOpenChange={(open) => {
          if (!open) setSub(null);
        }}
      >
        <SheetContent side="bottom" className="p-0">
          <SheetTitle className="sr-only">Permission mode</SheetTitle>
          <div className="py-2">
            {PERMISSION_MODE_OPTIONS.map((option) => (
              <SheetOptionRow
                key={option.id}
                label={option.label}
                description={option.description}
                selected={(mode ?? "default") === option.id}
                onSelect={() => {
                  onModeChange?.(option.id);
                  setSub(null);
                }}
              />
            ))}
          </div>
        </SheetContent>
      </Sheet>

      <Sheet
        open={sub === "model"}
        onOpenChange={(open) => {
          if (!open) setSub(null);
        }}
      >
        <SheetContent side="bottom" className="p-0">
          <SheetTitle className="sr-only">Model and effort</SheetTitle>
          <div className="max-h-[70dvh] overflow-y-auto pb-2">
            <SheetSectionLabel>Model</SheetSectionLabel>
            {onSwitchModel && (
              <p className="px-4 pb-1 text-xs text-muted-foreground">
                Picking a model continues this chat in a copy on it.
              </p>
            )}
            {modelOptions.map((m) => (
              <SheetOptionRow
                key={m.id}
                label={m.label}
                selected={m.id === (onSwitchModel ? switchId : model)}
                onSelect={() => {
                  if (onSwitchModel) {
                    setSub(null);
                    if (m.id !== switchId)
                      onSwitchModel(m.id === AUTO_MODEL_ID ? null : m);
                    return;
                  }
                  setModel(m.id);
                  onModelChange?.(m.id);
                }}
              />
            ))}
            {!onSwitchModel && <SheetSectionLabel>Effort</SheetSectionLabel>}
            {!onSwitchModel &&
              EFFORT_LEVELS.map((e) => (
                <SheetOptionRow
                  key={e.id}
                  label={e.label}
                  selected={e.id === effort}
                  onSelect={() => setEffort(e.id)}
                />
              ))}
            <div className="mx-4 my-1 h-px bg-border" />
            <button
              type="button"
              disabled={model === AUTO_MODEL_ID && effort === DEFAULT_EFFORT_ID}
              onClick={() => {
                setModel(AUTO_MODEL_ID);
                setEffort(DEFAULT_EFFORT_ID);
                onModelChange?.(AUTO_MODEL_ID);
                setSub(null);
              }}
              className={cn(menuRow, "text-muted-foreground")}
            >
              <RotateCcw className="size-4" />
              Reset to default
            </button>
          </div>
        </SheetContent>
      </Sheet>

      <Sheet
        open={sub === "memory"}
        onOpenChange={(open) => {
          if (!open) setSub(null);
        }}
      >
        <SheetContent side="bottom" className="p-0">
          <SheetTitle className="sr-only">Memory</SheetTitle>
          <div className="py-2">
            {[true, false].map((value) => (
              <SheetOptionRow
                key={String(value)}
                label={value ? "On" : "Off"}
                selected={memoryOn === value}
                onSelect={() => {
                  setMemoryOn(value);
                  setSub(null);
                }}
              />
            ))}
          </div>
        </SheetContent>
      </Sheet>
    </>
  );
}

function ProjectsDropdown({
  projects,
  selectedProjectId,
  onSelect,
  onCreate,
}: {
  projects?: ProjectItem[];
  selectedProjectId?: string | null;
  onSelect?: (id: string | null) => void;
  onCreate?: (name: string) => void;
}) {
  const { prompt, PromptDialog } = usePrompt();
  const selected = projects?.find((p) => p.id === selectedProjectId);

  return (
    <>
      {PromptDialog}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button size="sm" className={GHOST_TRIGGER_CLASS}>
            <FolderClosed className="size-3.5 text-muted-foreground" />
            {selected ? selected.name : "Project"}
            <ChevronDown className="size-3.5 text-muted-foreground" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent
          onCloseAutoFocus={(e) => e.preventDefault()}
          align="start"
          className="w-56"
        >
          <DropdownMenuLabel>Projects</DropdownMenuLabel>
          <DropdownMenuSeparator />
          <DropdownMenuGroup>
            <DropdownMenuItem onClick={() => onSelect?.(null)}>
              <span className={cn(!selectedProjectId && "font-semibold")}>
                Chats
              </span>
            </DropdownMenuItem>
            {projects?.map((p) => (
              <DropdownMenuItem key={p.id} onClick={() => onSelect?.(p.id)}>
                <FolderClosed className="size-3.5 shrink-0 mr-1.5 text-muted-foreground" />
                <span
                  className={cn(
                    "truncate",
                    p.id === selectedProjectId && "font-semibold",
                  )}
                >
                  {p.name}
                </span>
              </DropdownMenuItem>
            ))}
          </DropdownMenuGroup>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            onClick={async () => {
              const name = await prompt({
                title: "New project",
                description:
                  "Give your project a name to organize related conversations.",
                placeholder: "Project name",
                confirmText: "Create project",
              });
              if (name) onCreate?.(name);
            }}
          >
            <FolderPlus className="size-3.5 mr-2" />
            New project
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </>
  );
}

function useVoiceInput(onTranscript: (text: string) => void) {
  const [isListening, setIsListening] = useState(false);
  const [isSupported, setIsSupported] = useState(false);
  const recognitionRef = useRef<ReturnType<
    SpeechRecognitionType["prototype"]["constructor"]
  > | null>(null);

  useEffect(() => {
    const SR =
      typeof window !== "undefined"
        ? window.SpeechRecognition || window.webkitSpeechRecognition
        : null;
    setIsSupported(!!SR);
  }, []);

  const toggle = useCallback(() => {
    if (isListening) {
      recognitionRef.current?.stop();
      setIsListening(false);
      return;
    }

    const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
    if (!SR) return;

    const recognition = new SR();
    recognition.continuous = false;
    recognition.interimResults = true;
    recognition.lang = "en-US";

    let finalText = "";

    recognition.onresult = (event: SpeechRecognitionEvent) => {
      let interim = "";
      for (let i = event.resultIndex; i < event.results.length; i++) {
        const transcript = event.results[i][0].transcript;
        if (event.results[i].isFinal) {
          finalText += transcript;
        } else {
          interim += transcript;
        }
      }
      onTranscript(finalText + interim);
    };

    recognition.onend = () => {
      setIsListening(false);
      if (finalText) onTranscript(finalText);
    };

    recognition.onerror = () => {
      setIsListening(false);
    };

    recognitionRef.current = recognition;
    recognition.start();
    setIsListening(true);
  }, [isListening, onTranscript]);

  return { isListening, isSupported, toggle };
}

type SpeechRecognitionType = new () => {
  continuous: boolean;
  interimResults: boolean;
  lang: string;
  onresult: ((event: SpeechRecognitionEvent) => void) | null;
  onend: (() => void) | null;
  onerror: ((event: Event) => void) | null;
  start: () => void;
  stop: () => void;
};

declare global {
  interface Window {
    SpeechRecognition: SpeechRecognitionType;
    webkitSpeechRecognition: SpeechRecognitionType;
  }
}

const DEFAULT_PLACEHOLDER =
  "Pull in tools and expertise by including @agent or +Files";

/** Filter the agent list for the `@`-mention menu by handle or name. */
function agentMenuItems(query: string): ComposerMenuItem[] {
  const q = query.toLowerCase();
  return getAgentMentions()
    .filter((a) => a.handle.startsWith(q) || a.name.toLowerCase().includes(q))
    .map((a) => ({
      id: a.handle,
      label: a.handle,
      primary: a.name,
      secondary: a.description,
    }));
}

/** Filter the slash-command list for the `/`-command menu by name prefix. */
function commandMenuItems(query: string): ComposerMenuItem[] {
  const q = query.toLowerCase();
  return getSlashCommands()
    .filter((c) => c.name.startsWith(q))
    .map((c) => ({
      id: c.name,
      label: c.name,
      primary: `/${c.name}`,
      secondary: c.description,
    }));
}

/**
 * One attached-file chip. Image files show a small thumbnail of the file
 * itself (an object URL — cheaper than a data-URL read) before the name; the
 * URL is revoked when the pill unmounts (remove/send), so attach/remove
 * cycles never leak blob URLs. Non-image files (and an image whose thumbnail
 * has not resolved yet) show their file-kind glyph — image/PDF/code — with
 * the paperclip as the fallback. Exported for its unit test.
 */
export function AttachmentPill({
  file,
  onRemove,
  onPreview,
}: {
  file: File;
  onRemove: () => void;
  /** Opens the file in the canvas panel (main composer only). */
  onPreview?: () => void;
}) {
  const [previewUrl, setPreviewUrl] = useState<string | null>(null);
  useEffect(() => {
    if (!file.type.startsWith("image/")) {
      setPreviewUrl(null);
      return;
    }
    const url = URL.createObjectURL(file);
    setPreviewUrl(url);
    return () => URL.revokeObjectURL(url);
  }, [file]);
  const kind = fileKindMeta(file.name, file.type);
  const KindIcon = kind.icon;

  return (
    <span
      className={cn(
        "inline-flex h-7 items-center gap-1.5 rounded-full border border-brand/30 bg-brand/5 pr-1.5 text-xs text-brand dark:text-brand",
        previewUrl ? "pl-1" : "pl-2.5",
      )}
    >
      <button
        type="button"
        onClick={onPreview}
        disabled={!onPreview}
        className="inline-flex min-w-0 items-center gap-1.5 disabled:cursor-default"
        title={onPreview ? `Preview ${file.name}` : undefined}
      >
        {previewUrl ? (
          // biome-ignore lint/performance/noImgElement: object URLs need a plain img
          <img
            src={previewUrl}
            alt=""
            className="size-5 shrink-0 rounded-full object-cover"
          />
        ) : (
          <KindIcon aria-label={kind.label} className="size-3" />
        )}
        <span className="max-w-40 truncate">{file.name}</span>
      </button>
      <button
        type="button"
        onClick={onRemove}
        className="flex items-center justify-center size-4 rounded-full hover:bg-brand/10"
      >
        ×
      </button>
    </span>
  );
}

/** What Enter resolves to in the composer. `newline` means "do not intercept
 *  the key" — the editor's own hardBreak handles it. */
export type ComposerEnterAction = "send" | "queue" | "steer" | "newline";

/**
 * The composer's Enter decision table, extracted pure so the whole matrix is
 * testable without driving the TipTap editor:
 *
 * - Idle: Enter sends; Shift+Enter inserts a newline (fall through to the
 *   editor's hardBreak).
 * - Streaming: Enter performs the preferred action (Settings → Personalize) and
 *   Shift+Enter the opposite. Attachments no longer force the queue path —
 *   steers carry image parts (ADR 0251), so a mid-run send with files steers
 *   when steering is available (and degrades to queue when it is not, via
 *   performAction's missing-handler fallback, keeping the files attached).
 */
export function resolveComposerAction(input: {
  shift: boolean;
  isStreaming: boolean;
  behavior: EnterSendBehavior;
}): ComposerEnterAction {
  if (!input.isStreaming) return input.shift ? "newline" : "send";
  if (!input.shift) return input.behavior;
  return input.behavior === "queue" ? "steer" : "queue";
}

/** Open autocomplete menu state, mirrored from TipTap's suggestion lifecycle. */
interface ComposerMenu {
  kind: "agent" | "command";
  items: ComposerMenuItem[];
  index: number;
  select: (item: ComposerMenuItem) => void;
}

/**
 * Drive the open autocomplete menu from a keydown. Returns true when the key
 * was for the menu (arrows move the highlight, Enter/Tab pick the row, Escape
 * closes it). Selection is deferred a microtask so the chip insert lands after
 * ProseMirror finishes dispatching the key.
 */
function handleMenuNavKey(
  event: KeyboardEvent,
  menu: ComposerMenu,
  setMenu: React.Dispatch<React.SetStateAction<ComposerMenu | null>>,
): boolean {
  if (menu.items.length === 0) return false;
  switch (event.key) {
    case "ArrowDown":
      setMenu((m) => (m ? { ...m, index: (m.index + 1) % m.items.length } : m));
      return true;
    case "ArrowUp":
      setMenu((m) =>
        m
          ? { ...m, index: (m.index - 1 + m.items.length) % m.items.length }
          : m,
      );
      return true;
    case "Enter":
    case "Tab": {
      const item = menu.items[menu.index];
      if (item) queueMicrotask(() => menu.select(item));
      return true;
    }
    case "Escape":
      setMenu(null);
      return true;
    default:
      return false;
  }
}

export function ChatInput({
  placeholder: placeholderProp,
  projects,
  selectedProjectId,
  onSelectProject,
  onCreateProject,
  compact = false,
  mobileDocked = false,
  focusKey,
  onSend,
  onQueue,
  onSteer,
  onModelChange,
  disabled = false,
  isStreaming = false,
  appendText,
  onAppendConsumed,
  initialText,
  onInitialTextConsumed,
  modelLockedLabel,
  onSwitchModel,
  currentModelId,
  models,
  autoModelLabel,
  onPreviewAttachment,
  mode,
  onModeChange,
}: ChatInputProps) {
  const placeholder = placeholderProp ?? DEFAULT_PLACEHOLDER;
  // Plain-text mirror of the editor, kept in sync via onUpdate. Used only for
  // "is there something to send" checks and the streaming border state;
  // the editor document is the source of truth for the message itself.
  const [text, setText] = useState("");
  const [attachedFiles, setAttachedFiles] = useState<File[]>([]);
  const [isDragOver, setIsDragOver] = useState(false);
  const [isWindowDrag, setIsWindowDrag] = useState(false);
  const dragCountRef = useRef(0);
  const windowDragCountRef = useRef(0);

  // Autocomplete menu, mirrored from TipTap's suggestion lifecycle so we can
  // render the same full-width popover the pre-TipTap composer used. `menuRef`
  // gives the suggestion keydown handler a synchronous read of current state.
  const [menu, setMenu] = useState<ComposerMenu | null>(null);

  // Wire each mention's TipTap suggestion to the shared menu state. `command`
  // (from the suggestion props) inserts the atomic chip at the trigger range.
  // Keyboard navigation is handled by the editor's handleKeyDown (below), not
  // here, so the suggestion's own onKeyDown is intentionally omitted.
  const makeRender = useCallback(
    (kind: "agent" | "command") => () => ({
      onStart: (props: SuggestionProps<ComposerMenuItem>) => {
        setMenu({
          kind,
          items: props.items,
          index: 0,
          select: (item) => props.command(item),
        });
      },
      onUpdate: (props: SuggestionProps<ComposerMenuItem>) => {
        setMenu((m) => {
          if (!m || m.kind !== kind) return m;
          const index = props.items.length
            ? Math.min(m.index, props.items.length - 1)
            : 0;
          return {
            kind,
            items: props.items,
            index,
            select: (item) => props.command(item),
          };
        });
      },
      onExit: () => setMenu((m) => (m && m.kind === kind ? null : m)),
    }),
    [],
  );

  const mentions = useMemo(
    () =>
      createComposerMentions({
        agentItems: agentMenuItems,
        commandItems: commandMenuItems,
        makeRender,
      }),
    [makeRender],
  );

  const editor = useEditor({
    immediatelyRender: false,
    autofocus: mobileViewport() ? false : "end",
    extensions: [
      // A deliberately plain field: keep the editing primitives (undo, hard
      // break, drop/gap cursors) but drop every rich-text mark and block so
      // typing `# `, `**`, `- ` etc. stays literal, exactly like the textarea.
      StarterKit.configure({
        heading: false,
        bold: false,
        italic: false,
        strike: false,
        code: false,
        codeBlock: false,
        blockquote: false,
        bulletList: false,
        orderedList: false,
        listItem: false,
        horizontalRule: false,
        link: false,
        underline: false,
      }),
      Placeholder.configure({ placeholder }),
      ...mentions,
    ],
    onUpdate: ({ editor }) => setText(editor.getText({ blockSeparator: "\n" })),
  });

  // Entering a chat or thread puts the caret in the field; autofocus only
  // covers the first mount, so a change of target refocuses explicitly.
  // Desktop only — see mobileViewport above.
  useEffect(() => {
    if (focusKey !== undefined && !mobileViewport())
      editor?.commands.focus("end");
  }, [focusKey, editor]);

  useEffect(() => {
    editor?.setEditable(!disabled);
  }, [editor, disabled]);

  useEffect(() => {
    if (appendText && editor) {
      const raw = editor.getText({ blockSeparator: "\n" });
      const sep = raw && !raw.endsWith("\n") ? "\n" : "";
      setComposerText(editor, `${raw}${sep}> ${appendText}\n`);
      setText(editor.getText({ blockSeparator: "\n" }));
      onAppendConsumed?.();
      requestAnimationFrame(() => editor.commands.focus("end"));
    }
  }, [appendText, onAppendConsumed, editor]);

  useEffect(() => {
    if (initialText && editor) {
      setComposerText(editor, initialText);
      setText(editor.getText({ blockSeparator: "\n" }));
      onInitialTextConsumed?.();
      requestAnimationFrame(() => editor.commands.focus("end"));
    }
  }, [initialText, onInitialTextConsumed, editor]);

  useEffect(() => {
    const onEnter = (e: DragEvent) => {
      if (e.dataTransfer?.types.includes("Files")) {
        windowDragCountRef.current++;
        setIsWindowDrag(true);
      }
    };
    const onLeave = () => {
      windowDragCountRef.current--;
      if (windowDragCountRef.current <= 0) {
        windowDragCountRef.current = 0;
        setIsWindowDrag(false);
      }
    };
    const onDrop = () => {
      windowDragCountRef.current = 0;
      setIsWindowDrag(false);
    };
    window.addEventListener("dragenter", onEnter);
    window.addEventListener("dragleave", onLeave);
    window.addEventListener("drop", onDrop);
    return () => {
      window.removeEventListener("dragenter", onEnter);
      window.removeEventListener("dragleave", onLeave);
      window.removeEventListener("drop", onDrop);
    };
  }, []);

  const voice = useVoiceInput(
    useCallback(
      (transcript: string) => {
        if (editor) setComposerText(editor, transcript);
        setText(transcript);
      },
      [editor],
    ),
  );

  // The preferred Enter action while a reply is streaming (Settings → Personalize).
  // Hydrates on mount, so the first frame is always the "queue" default.
  const { behavior: enterBehavior } = useEnterSendBehavior();

  /** Executes one resolved Enter action against the current editor content.
   *  Missing handlers degrade toward the pre-steer behavior: steer without
   *  onSteer queues, queue without onQueue sends. */
  const performAction = useCallback(
    (action: ComposerEnterAction) => {
      if (action === "newline") return;
      const trimmed = editor ? composerText(editor) : "";
      if (!trimmed || disabled) return;
      const files = attachedFiles.length > 0 ? attachedFiles : undefined;
      const resolved = action === "steer" && !onSteer ? "queue" : action;
      if (resolved === "steer" && onSteer) {
        // A steer carries the staged attachments as image parts (ADR 0251);
        // they leave the composer with the text.
        onSteer(trimmed, files);
        editor?.commands.clearContent();
        setText("");
        setAttachedFiles([]);
        return;
      }
      if (resolved === "queue" && onQueue) {
        onQueue(trimmed);
        editor?.commands.clearContent();
        setText("");
        // Attached files deliberately stay attached: a queued text cannot
        // carry them, so they ride the next real send.
        return;
      }
      onSend?.(trimmed, files);
      editor?.commands.clearContent();
      setText("");
      setAttachedFiles([]);
    },
    [editor, disabled, onQueue, onSteer, onSend, attachedFiles],
  );

  /** Resolve + perform for one Enter press (or a send-button click, which is
   *  the plain-Enter path). */
  const actOnEnter = useCallback(
    (shift: boolean) => {
      performAction(
        resolveComposerAction({
          shift,
          isStreaming,
          behavior: enterBehavior,
        }),
      );
    },
    [performAction, isStreaming, enterBehavior],
  );

  const handleSend = useCallback(() => actOnEnter(false), [actOnEnter]);

  // Menu nav + Enter-to-send are wired with a native capture-phase keydown
  // listener on the editor DOM, re-subscribed each render with fresh closures
  // over `menu`/`actOnEnter`. Capture phase runs before ProseMirror's own
  // (bubble-phase) handler, and stopPropagation keeps the base keymap and the
  // suggestion plugins from also acting. This sidesteps both TipTap re-syncing
  // its editorProps and the React Compiler not preserving render-phase refs.
  useEffect(() => {
    const dom = editor?.view.dom;
    if (!dom) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (menu) {
        if (handleMenuNavKey(event, menu, setMenu)) {
          event.preventDefault();
          event.stopPropagation();
        }
        return;
      }
      if (event.key !== "Enter") return;
      if (event.shiftKey) {
        // Shift+Enter acts (as the opposite of the Enter preference) only
        // while a reply is streaming AND there is text to act on; otherwise
        // it keeps its newline behavior — fall through to the editor's
        // hardBreak without preventDefault.
        const trimmed = editor ? composerText(editor) : "";
        if (!isStreaming || !trimmed) return;
        event.preventDefault();
        event.stopPropagation();
        actOnEnter(true);
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      actOnEnter(false);
    };
    dom.addEventListener("keydown", onKeyDown, true);
    return () => dom.removeEventListener("keydown", onKeyDown, true);
  }, [editor, menu, isStreaming, actOnEnter]);

  const hasText = text.trim().length > 0;

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: drop zone for file attachments
    <div
      onDragEnter={(e) => {
        e.preventDefault();
        dragCountRef.current++;
        setIsDragOver(true);
      }}
      onDragOver={(e) => {
        e.preventDefault();
        e.dataTransfer.dropEffect = "copy";
      }}
      onDragLeave={(e) => {
        e.preventDefault();
        dragCountRef.current--;
        if (dragCountRef.current <= 0) {
          dragCountRef.current = 0;
          setIsDragOver(false);
        }
      }}
      onDrop={(e) => {
        e.preventDefault();
        dragCountRef.current = 0;
        setIsDragOver(false);
        // Only images can cross the wire (the daemon's prompt parts are
        // image/audio only), so only images attach.
        const droppedFiles = Array.from(e.dataTransfer.files).filter((f) =>
          f.type.startsWith("image/"),
        );
        if (droppedFiles.length > 0) {
          setAttachedFiles((prev) => [...prev, ...droppedFiles]);
        }
      }}
      className={cn(
        "relative rounded-2xl bg-zinc-50 dark:bg-zinc-900",
        mobileDocked && "max-[499px]:rounded-none max-[499px]:bg-transparent",
      )}
    >
      {/* Autocomplete popover (agent @-mentions or slash commands), floating
          above the input box. Driven by TipTap's suggestion lifecycle. */}
      {menu && menu.items.length > 0 && (
        <div className="absolute bottom-full left-0 right-0 z-30 mb-2 overflow-hidden rounded-xl border border-border bg-popover text-popover-foreground shadow-xl">
          <div className="max-h-64 overflow-y-auto py-1">
            {menu.items.map((item, i) => (
              <button
                key={item.id}
                type="button"
                onMouseEnter={() =>
                  setMenu((m) => (m ? { ...m, index: i } : m))
                }
                // Use onMouseDown + preventDefault so selecting a row doesn't
                // blur the editor (which would close the suggestion first).
                onMouseDown={(e) => {
                  e.preventDefault();
                  item && menu.select(item);
                }}
                className={cn(
                  "flex w-full items-center gap-3 px-3 py-2 text-left",
                  i === menu.index ? "bg-accent" : "hover:bg-accent/50",
                )}
              >
                {menu.kind === "command" ? (
                  <span className="font-mono text-sm">/{item.id}</span>
                ) : (
                  <>
                    <Bot className="size-4 shrink-0 text-muted-foreground" />
                    <span className="shrink-0 text-sm font-medium">
                      {item.primary}
                    </span>
                  </>
                )}
                <span className="truncate text-xs text-muted-foreground">
                  {item.secondary}
                </span>
              </button>
            ))}
          </div>
        </div>
      )}
      {/* Input box: textarea + inline toolbar (+, mic) + send button.
          Has its own rounded border. The toolbar row below has a matching
          border on its top/sides/bottom; the two borders meet along the
          input box's bottom edge, sharing a single visible line. */}
      <div
        className={cn(
          "relative rounded-2xl border bg-background transition-colors focus-within:border-zinc-400 dark:focus-within:border-zinc-600",
          // Docked mobile composer: full-bleed, only the top hairline
          // separates it from the conversation above; it owns the
          // home-indicator safe area now that it touches the screen edge.
          // The hairline matches the chat title bar's divider (border-border),
          // steady across focus — the docked bar is chrome, not a field.
          mobileDocked &&
            "max-[499px]:rounded-none max-[499px]:border-x-0 max-[499px]:border-b-0 max-[499px]:border-border max-[499px]:focus-within:border-border max-[499px]:pb-[env(safe-area-inset-bottom)]",
          isDragOver
            ? "border-brand bg-brand/5 dark:bg-brand/10 ring-2 ring-brand/20"
            : isWindowDrag
              ? "border-brand/50 ring-1 ring-brand/10"
              : isStreaming && hasText
                ? "border-warning shadow-warning/10"
                : "border-zinc-300 dark:border-zinc-700",
        )}
      >
        {voice.isListening && (
          <div className="flex items-center gap-2 px-4 pt-3 pb-1">
            <span className="size-2 rounded-full bg-brand animate-pulse" />
            <span className="text-xs font-medium text-brand dark:text-brand">
              Listening
            </span>
          </div>
        )}
        {isDragOver && (
          <div className="absolute inset-0 z-20 flex items-center justify-center rounded-2xl bg-brand/10 dark:bg-brand/15 pointer-events-none">
            <div className="flex items-center gap-2 text-brand dark:text-brand">
              <Paperclip className="size-5" />
              <span className="text-sm font-medium">Drop files to attach</span>
            </div>
          </div>
        )}
        {attachedFiles.length > 0 && (
          <div className="flex flex-wrap gap-1.5 px-4 pt-3">
            {attachedFiles.map((f, i) => (
              <AttachmentPill
                onPreview={
                  onPreviewAttachment ? () => onPreviewAttachment(f) : undefined
                }
                // biome-ignore lint/suspicious/noArrayIndexKey: files may share names
                key={`${f.name}-${i}`}
                file={f}
                onRemove={() =>
                  setAttachedFiles((prev) => prev.filter((_, j) => j !== i))
                }
              />
            ))}
          </div>
        )}
        {/* Mobile consolidates to ONE line — + on the left, the field, and a
            single right slot that is the mic until there is text, then the
            send button (native messaging convention). Desktop keeps the
            two-row layout below. */}
        {/* min-h-14 mirrors the chat header bar, so the docked composer and
            the title bar read as symmetric top/bottom bands. */}
        <div className="flex flex-wrap items-start gap-1.5 px-4 pt-4 pb-2 max-[499px]:min-h-14 max-[499px]:flex-nowrap max-[499px]:items-center max-[499px]:gap-1 max-[499px]:px-2 max-[499px]:py-1.5">
          <div className="hidden max-[499px]:block">
            <MobileComposerMenu
              models={models}
              autoModelLabel={autoModelLabel}
              onFilesSelected={(newFiles) =>
                setAttachedFiles((prev) => [...prev, ...newFiles])
              }
              onModelChange={onModelChange}
              modelLockedLabel={modelLockedLabel}
              onSwitchModel={onSwitchModel}
              currentModelId={currentModelId}
              mode={mode}
              onModeChange={onModeChange}
              modeDisabled={disabled || isStreaming}
            />
          </div>
          {/* TipTap composer: resolved @agent / /skill mentions are atomic
              green chips (Backspace removes a whole chip); Enter sends and
              Shift+Enter inserts a newline (handled in the editor keymap). */}
          <div
            className={cn(
              "relative flex-1 min-w-[120px] self-center max-[499px]:py-1",
              disabled && "opacity-50",
            )}
          >
            <EditorContent editor={editor} className="composer-editor" />
          </div>
          <div className="hidden max-[499px]:block">
            {!hasText && voice.isSupported ? (
              <Button
                size="icon"
                className={cn(
                  "size-8 rounded-full border-0 shadow-none",
                  voice.isListening
                    ? "bg-brand/10 text-brand hover:bg-brand/20"
                    : "bg-transparent text-muted-foreground hover:bg-muted/60",
                )}
                onClick={voice.toggle}
                disabled={disabled}
                aria-label={
                  voice.isListening ? "Stop listening" : "Voice input"
                }
              >
                <Mic className="size-4" />
              </Button>
            ) : (
              <Button
                size="icon"
                className="size-8 rounded-full bg-brand text-brand-foreground hover:bg-brand/90 disabled:opacity-50"
                onClick={handleSend}
                disabled={disabled || !hasText}
                aria-label="Send message"
              >
                <ArrowUp className="size-4" />
              </Button>
            )}
          </div>
        </div>
        {/* Desktop-only bottom row: attach (+), mic on the left; send right */}
        <div className="flex items-center gap-1 px-2 pb-2 max-[499px]:hidden">
          <FilesDropdown
            onFilesSelected={(newFiles) =>
              setAttachedFiles((prev) => [...prev, ...newFiles])
            }
          />
          {voice.isSupported && (
            <Button
              size="icon"
              className={cn(
                "size-8 rounded-full border-0 shadow-none",
                voice.isListening
                  ? "bg-brand/10 text-brand hover:bg-brand/20"
                  : "bg-transparent text-muted-foreground hover:bg-muted/60",
              )}
              onClick={voice.toggle}
              disabled={disabled}
              aria-label={voice.isListening ? "Stop listening" : "Voice input"}
            >
              <Mic className="size-4" />
            </Button>
          )}
          <Button
            size="icon"
            className="ml-auto size-8 rounded-full bg-brand text-brand-foreground hover:bg-brand/90 disabled:opacity-50"
            onClick={handleSend}
            disabled={disabled || !hasText}
            aria-label="Send message"
          >
            <ArrowUp className="size-4" />
          </Button>
        </div>
      </div>
      {/* Toolbar sits BEHIND the input box: negative top margin pulls it up
          so its top edge overlaps the input box's bottom rounded corners,
          making the two boxes appear to share a single outline. */}
      {/* Hidden on mobile: Model and Memory live in the + options sheet. */}
      {/* @container: the Model/Memory pills collapse their value labels via
          container queries when THIS row runs narrow (a ~400px side-panel
          composer), independent of the viewport width. */}
      <div className="@container -mt-4 pt-5 px-2 pb-1.5 flex items-center gap-1 rounded-b-2xl border border-t-0 border-zinc-300 dark:border-zinc-700 bg-zinc-50 dark:bg-zinc-900 overflow-x-auto hide-scrollbar max-[499px]:hidden">
        {projects && (
          <ProjectsDropdown
            projects={projects}
            selectedProjectId={selectedProjectId}
            onSelect={onSelectProject}
            onCreate={onCreateProject}
          />
        )}
        {!compact && (
          <>
            {onModeChange && (
              <ModeSelector
                mode={mode ?? "default"}
                onModeChange={onModeChange}
                disabled={disabled || isStreaming}
              />
            )}
            <ModelEffortSelector
              models={models}
              autoModelLabel={autoModelLabel}
              lockedLabel={modelLockedLabel}
              onSwitchModel={onSwitchModel}
              currentModelId={currentModelId}
              onModelChange={(id) => {
                onModelChange?.(id);
              }}
            />
            <MemoryToggle />
          </>
        )}
      </div>
    </div>
  );
}
