"use client";

import Placeholder from "@tiptap/extension-placeholder";
import { EditorContent, useEditor } from "@tiptap/react";
import StarterKit from "@tiptap/starter-kit";
import type { SuggestionProps } from "@tiptap/suggestion";
import {
  ArrowUp,
  Bot,
  Check,
  ChevronDown,
  ChevronRight,
  FolderClosed,
  FolderPlus,
  Mic,
  Paperclip,
  Plug,
  Plus,
  RotateCcw,
  Shield,
  SlidersHorizontal,
  Terminal,
  TriangleAlert,
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
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import {
  type BuiltinGates,
  type BuiltinOutcome,
  builtinSlashCommands,
  CLOSED_BUILTIN_GATES,
  classifySlashLine,
  isStudioBuiltinCommand,
  type StudioBuiltinCommand,
} from "@/features/agent/composer-builtins";
import {
  getAgentMentions,
  getSlashCommands,
} from "@/features/agent/composer-capabilities";
import { useOptionalRuntimeStatus } from "@/features/agent/runtime-status";
import { useConfirm } from "@/hooks/use-confirm";
import { usePrompt } from "@/hooks/use-prompt";
import {
  classifyAttachment,
  type MediaCapabilities,
} from "@/lib/attachment-inline";
import { ALLOW_ALL_POSTURES } from "@/lib/controller-permissions.mjs";
import { fileKindMeta } from "@/lib/file-meta";
import { saveHarnessPermissions } from "@/lib/harness/client";
import { useDefaultModel } from "@/lib/model-preferences";
import {
  modeAccentClass,
  modeDotClass,
  nextPermissionMode,
  PERMISSION_MODE_OPTIONS,
  permissionModeLabel,
  resolveModeCycleKey,
} from "@/lib/permission-mode";
import {
  type EnterSendBehavior,
  useEnterSendBehavior,
} from "@/lib/profile-preferences";
import type { SessionPermissionMode } from "@/lib/protocol";
import { effortLabel } from "@/lib/reasoning-effort";
import {
  type SessionToolProfile,
  toolProfilePillSuffix,
} from "@/lib/tool-profile";
import { cn } from "@/lib/utils";
import { ClearDraftButton } from "./clear-draft-button";
import {
  ESCAPE_ARMED_HINT,
  useComposerDraftGuards,
} from "./composer-draft-guards";
import {
  fileMenuRows,
  isAttachFileItem,
  isFileMenuItem,
  pathMentionText,
} from "./composer-file-mention";
import { composerEditorStyle, composerFrameClass } from "./composer-frame";
import { isAlwaysNewlineChord, isClearDraftChord } from "./composer-keys";
import {
  type ComposerMenuItem,
  composerText,
  createComposerMentions,
  setComposerText,
} from "./composer-mentions";
import {
  applyComposerPaste,
  PastePlaceholder,
  readPasteClipboard,
} from "./composer-paste";
import { liveModelProvenance, resolveDraftModel } from "./draft-model";
import {
  EffortSheetSection,
  EffortSubmenu,
  effortTriggerLabel,
  ModelPickerShortcut,
} from "./effort-picker";
import {
  McpInsertMenu,
  type McpPickerKind,
  useMcpComposerInsert,
} from "./mcp-composer-insert";
import { ModelSheetSection, ModelSubmenuContent } from "./model-picker";
import { ModelPickerOpener } from "./model-picker-opener";
import {
  ToolProfileSelector,
  ToolProfileSheetRows,
} from "./tool-profile-picker";

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
  /** Holds the text — plus the staged files, which leave the composer with
      it — for the next run (mid-run queuing). */
  onQueue?: (content: string, files?: File[]) => void;
  /** Injects the text — plus any staged image attachments (ADR 0251) — into
      the in-flight run at the next step (mid-run steering). Only meaningful
      while `isStreaming`; absent when the daemon lacks the steer capability
      (mid-run sends then queue and files stay attached). */
  onSteer?: (content: string, files?: File[]) => void;
  /** Draft: the pending model pick ("" = the auto row, i.e. the daemon
   *  default on purpose); null = reset to untouched, so the Studio default
   *  for new chats applies again when the daemon lists it. */
  onModelChange?: (id: string | null) => void;
  /** Live daemon models for the picker; absent = the sentinel only. */
  models?: ComposerModelOption[];
  /** Label for the empty (daemon-picks) entry. */
  autoModelLabel?: string;
  /** Preview an attached file in the canvas panel. */
  onPreviewAttachment?: (file: File) => void;
  disabled?: boolean;
  isStreaming?: boolean;
  /** Keeps the Mode selector enabled while `isStreaming`: the caller defers
      the switch until the run ends (the status strip shows it "(pending)"),
      instead of disabling the pill mid-run. */
  modeSwitchDeferred?: boolean;
  /** A mode switch held until the run ends (the TUI's `mode <target>
      pending`): the pill shows it "· pending" with its colour cue, and ⇧Tab
      cycles onward from it. `mode` stays the daemon-confirmed value. */
  pendingMode?: SessionPermissionMode | null;
  appendText?: string | null;
  onAppendConsumed?: () => void;
  /** Plain-text seed dropped into an empty composer (e.g. a "next step" chip
      that pre-fills a prompt without sending it). Unlike `appendText`, it is
      not quoted and replaces rather than appends. */
  initialText?: string | null;
  /** Files re-staged alongside `initialText` (a queued message pulled back
      for editing brings its attachments with it); consumed together. */
  initialFiles?: File[];
  onInitialTextConsumed?: () => void;
  /** Messages held in the queue strip. On an EMPTY composer with a held
      queue: Enter (idle) sends them via `onResumeQueue`, ↑ pulls them back
      via `onEditAllQueued`, Esc (idle) drops them via `onClearQueue`. */
  queuedCount?: number;
  onResumeQueue?: () => void;
  onEditAllQueued?: () => void;
  onClearQueue?: () => void;
  /** Display-only model label for live-harness sessions (see ModelSelector). */
  modelLockedLabel?: string;
  /** Live-chat model switch: picking forks the chat onto the model (the
   *  daemon fixes a session's model at create). null = auto-routed. */
  onSwitchModel?: (option: ComposerModelOption | null) => void;
  /** The live session's current model id ("" = auto). */
  currentModelId?: string;
  /** Draft: the pending reasoning-effort tier as a WIRE value ("" = auto),
   *  applied when the first send mints the session. */
  effort?: string;
  onEffortChange?: (wire: string) => void;
  /** Live-chat effort switch: picking forks the chat onto the tier (the
   *  daemon fixes a session's effort at create, like its model). */
  onSwitchEffort?: (wire: string) => void;
  /** The live session's EFFECTIVE tier (resolved_model.reasoning_effort;
   *  "" = auto), for the checkmark and the trigger label. */
  currentEffort?: string;
  /** The live session's model `reasoning` flag; false shows the Effort
   *  list's "a tier may be ignored" warning. */
  currentModelReasoning?: boolean;
  /** False when the daemon's model_selection capability is off: hides the
   *  Effort list along with the model list. */
  effortSupported?: boolean;
  /** The session's current permission mode, shown by the Mode selector. */
  mode?: SessionPermissionMode;
  /** Renders the Mode selector (first in the control bar) when provided.
      Surfaces without a mode concept (the thread panel, the mock tour chat)
      simply omit it. */
  onModeChange?: (mode: SessionPermissionMode) => void;
  /** The chat's TOOL PROFILE (the daemon's per-session `profile`, ADR 0291):
      "" = all tools, "no-fs" = the file-less catalog. Shown inside the Mode
      menu as a "Tools" section. Undefined = unknown (a live chat Studio did
      not mint), which renders no line at all. */
  profile?: SessionToolProfile;
  /** Draft only: picks the profile the first send mints with. Absent on a
      live chat — the profile is fixed at create — where a known `profile`
      renders read-only. */
  onProfileChange?: (profile: SessionToolProfile) => void;
  /** Answers a Studio built-in slash command (`/clear /help /session /retry
      /diagnostics /compact`) instead of sending it: picked from the `/` menu
      or typed as the whole message, this fires; `{ ok: true }` (or no
      outcome) clears the composer, a refusal keeps the text and shows its
      warning above the input. Absent, the builtins are not offered and the
      text goes to the agent like any other message. */
  onLocalCommand?: (
    command: StudioBuiltinCommand,
  ) => BuiltinOutcome | undefined;
  /** Which gated built-ins the daemon enables (`/compact` needs manual
      compaction). Fail-closed when omitted. */
  builtinGates?: BuiltinGates;
  /** What the session may send as media (its resolved input modalities).
      Gates staging: a file the model cannot take is refused with a reason
      instead of being staged. Absent = no media gate (text-size limits still
      apply); the send path gates again regardless. */
  mediaCapabilities?: MediaCapabilities;
  /** The forwarded double-Esc counter (`useComposerEscape`): each change is
      one Esc nothing else claimed; the first arms, a second within 1.5 s
      clears the draft. Absent = no Esc-to-clear (the thread panel). */
  escapePress?: number;
  /** Fires when "the composer holds text" flips (and `false` on unmount):
      the surface arms its Esc layering and the leave guard from it. */
  onDraftChange?: (hasText: boolean) => void;
  /** Persists the unsent draft under this identity (a session id, or `"new"`
      for the draft view) so a reload never loses it; absent = transient. */
  draftKey?: string;
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
  inputRef,
}: {
  onFilesSelected?: (files: File[]) => void;
  /** Lifted so the composer's `@` "Attach a file…" row opens the SAME
      native picker; absent, the dropdown owns its own input. */
  inputRef?: React.RefObject<HTMLInputElement | null>;
}) {
  const ownInputRef = useRef<HTMLInputElement>(null);
  const fileInputRef = inputRef ?? ownInputRef;

  return (
    <>
      {/* Any file: images and audio become media parts (gated when staged),
          everything else is inlined as text — the TUI's attach parity. */}
      <input
        ref={fileInputRef}
        type="file"
        multiple
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
const autoModel = (label?: string): ComposerModelOption => ({
  id: AUTO_MODEL_ID,
  label: label ?? "Default model",
});

export interface ComposerModelOption {
  id: string;
  label: string;
  /** The daemon requires provider_id whenever model_id rides a create. */
  providerId?: string;
  /** The inventory's `reasoning` flag: false → the Effort list warns that a
   *  tier may be ignored; absent = unknown (no warning). */
  reasoning?: boolean;
  /** The inventory's `image` flag: the picker row shows an image glyph. */
  image?: boolean;
  /** Context window in tokens (0/absent = unknown, no label on the row). */
  contextLimit?: number;
}

/** The reasoning-effort sentinel as a WIRE value: "" = auto (the create/fork
 *  body omits `reasoning_effort`, so the operator's default applies). The
 *  tiers themselves live in `@/lib/reasoning-effort` (see ./effort-picker). */
const DEFAULT_EFFORT = "";

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
export function ModelEffortSelector({
  onModelChange,
  lockedLabel,
  onSwitchModel,
  currentModelId,
  models,
  autoModelLabel,
  effort,
  onEffortChange,
  onSwitchEffort,
  currentEffort,
  currentModelReasoning,
  effortSupported = true,
}: {
  /** Draft pick ("" = auto row); null = reset to untouched (Studio default). */
  onModelChange?: (id: string | null) => void;
  lockedLabel?: string;
  /** Live-chat switch: picking forks the chat onto the model. */
  onSwitchModel?: (option: ComposerModelOption | null) => void;
  currentModelId?: string;
  models?: ComposerModelOption[];
  autoModelLabel?: string;
  /** Draft: the pending reasoning-effort WIRE value ("" = auto), controlled
   *  by the caller; uncontrolled local state when absent. */
  effort?: string;
  onEffortChange?: (wire: string) => void;
  /** Live-chat switch: picking forks the chat onto the effort tier. */
  onSwitchEffort?: (wire: string) => void;
  /** The live session's resolved_model.reasoning_effort ("" = auto). */
  currentEffort?: string;
  /** The live session's model `reasoning` flag (false → warning row). */
  currentModelReasoning?: boolean;
  /** False when the daemon's model_selection capability is off: the Effort
   *  submenu is hidden with the model list. */
  effortSupported?: boolean;
}) {
  const modelOptions = [autoModel(autoModelLabel), ...(models ?? [])];
  // Draft: null = untouched (the browser-local Studio default for new chats
  // applies when the daemon lists it), "" = the auto row picked on purpose.
  const [model, setModel] = useState<string | null>(null);
  const { defaultModel: studioDefault } = useDefaultModel();
  const draft = resolveDraftModel(model, studioDefault, modelOptions);
  const draftId = draft.id;
  const [localEffort, setLocalEffort] = useState<string>(DEFAULT_EFFORT);
  const [menuOpen, setMenuOpen] = useState(false);
  const switchId = currentModelId ?? AUTO_MODEL_ID;
  const selectedModel = onSwitchModel
    ? (modelOptions.find((m) => m.id === switchId) ?? {
        id: switchId,
        label: switchId || autoModel(autoModelLabel).label,
      })
    : (modelOptions.find((m) => m.id === draftId) ?? modelOptions[0]);
  // The picker header's provenance tag: where the model in force came from.
  const provenance = onSwitchModel
    ? liveModelProvenance(switchId, studioDefault, modelOptions)
    : draft.provenance;
  // Draft: the pending pick (caller-controlled when wired). Live: the
  // daemon's EFFECTIVE tier — never the local pick.
  const draftEffort = effort ?? localEffort;
  const selectedEffort = onSwitchModel ? (currentEffort ?? "") : draftEffort;
  const showEffort =
    effortSupported && (onSwitchModel ? Boolean(onSwitchEffort) : true);
  // The `reasoning` flag of the model the tier would apply to: the live
  // session's (caller-supplied, else its listed option) or the draft's picked
  // option; undefined (auto-routed / unlisted) renders no warning.
  const pickedOption = modelOptions.find(
    (m) => m.id === (onSwitchModel ? switchId : draftId),
  );
  const modelReasoning = onSwitchModel
    ? (currentModelReasoning ?? pickedOption?.reasoning)
    : pickedOption?.reasoning;
  // The TUI's F7 analogue: only the picker wired to a session registers the
  // shortcut (the thread panel's composer is not), so the main chat's picker
  // always wins.
  const shortcutWired = Boolean(onSwitchModel || onEffortChange);

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
    <DropdownMenu open={menuOpen} onOpenChange={setMenuOpen}>
      {shortcutWired && (
        <>
          <ModelPickerShortcut onOpen={() => setMenuOpen(true)} />
          {/* The `/models` and `/effort` built-ins' way in (F7's twin). */}
          <ModelPickerOpener
            surface="desktop"
            onOpen={() => setMenuOpen(true)}
          />
        </>
      )}
      <DropdownMenuTrigger asChild>
        <Button
          size="sm"
          className={GHOST_TRIGGER_CLASS}
          title={
            showEffort
              ? effortTriggerLabel(selectedModel.label, selectedEffort)
              : selectedModel.label
          }
        >
          <span className="max-w-40 truncate @max-md:hidden">
            {selectedModel.label}
          </span>
          {showEffort && (
            <span className="max-w-24 truncate text-muted-foreground @max-md:hidden">
              {effortLabel(selectedEffort)}
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
          <ModelSubmenuContent
            options={modelOptions}
            selectedId={onSwitchModel ? switchId : draftId}
            currentLabel={selectedModel.label}
            provenance={provenance}
            live={Boolean(onSwitchModel)}
            onPick={(m) => {
              // cmdk rows are not Radix items, so the menu is closed here.
              setMenuOpen(false);
              if (onSwitchModel) {
                if (m.id !== switchId)
                  onSwitchModel(m.id === AUTO_MODEL_ID ? null : m);
                return;
              }
              setModel(m.id);
              onModelChange?.(m.id);
            }}
          />
        </DropdownMenuSub>
        {showEffort && (
          <EffortSubmenu
            value={selectedEffort}
            live={Boolean(onSwitchModel)}
            modelReasoning={modelReasoning}
            onPick={(wire) => {
              if (onSwitchModel) {
                if (wire !== selectedEffort) onSwitchEffort?.(wire);
                return;
              }
              setLocalEffort(wire);
              onEffortChange?.(wire);
            }}
          />
        )}
        {!onSwitchModel && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              disabled={model === null && draftEffort === DEFAULT_EFFORT}
              onClick={() => {
                setModel(null);
                setLocalEffort(DEFAULT_EFFORT);
                onModelChange?.(null);
                onEffortChange?.(DEFAULT_EFFORT);
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

/**
 * The operator posture tiers as the Mode menu offers them: plain words for
 * a non-technical user. Values are the daemon's own ladder (rule 13) and
 * saving one goes through the SAME controller document as Settings →
 * Permissions (`saveHarnessPermissions`), which restarts the agent.
 */
const POSTURE_MENU_OPTIONS: readonly {
  value: string;
  label: string;
  description: string;
}[] = [
  {
    value: "strict",
    label: "Strict",
    description: "Asks before every change.",
  },
  {
    value: "trusted",
    label: "Trusted",
    description: "Follows this project's own rules, asks otherwise.",
  },
  {
    value: "auto",
    label: "Auto",
    description: "Works without asking. For unattended runs.",
  },
  {
    value: "yolo",
    label: "Yolo",
    description: "No safeguards. Only on a throwaway machine.",
  },
];

/**
 * The Mode menu's "Safety level" control: the daemon-wide operator posture
 * (the same setting as Settings → Permissions), offered where the user
 * already picks how the agent asks. Managed mode only — in external mode
 * the deployment owns the flag — and only once the controller has reported
 * the saved document. Picking Auto/Yolo confirms first (they waive the ask
 * before every change); the confirm dialog is rendered by the CALLER,
 * outside the menu, because the menu closes (and unmounts its content) on
 * the click. Saving restarts the agent; the runtime re-probe flips the
 * checkmark once the new daemon answers.
 */
function usePostureControl() {
  // Optional: a composer rendered outside the provider (tests, the mock
  // tour, the thread panel) simply offers no safety-level rows.
  const runtime = useOptionalRuntimeStatus();
  const { confirm, ConfirmDialog } = useConfirm();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const permissions = runtime?.permissions ?? null;
  const available = runtime?.mode === "managed" && permissions !== null;
  const current = permissions?.posture ?? "";

  const pick = useCallback(
    async (tier: string) => {
      if (!permissions || tier === current || busy) return;
      if (ALLOW_ALL_POSTURES.includes(tier)) {
        const name = tier === "yolo" ? "Yolo" : "Auto";
        const ok = await confirm({
          title: `Switch to ${name}?`,
          description:
            tier === "yolo"
              ? "The agent will make changes and run commands without asking, with no safeguards at all. Only use this on a throwaway machine. The agent restarts and anything running will stop."
              : "The agent will make changes and run commands without asking first. The agent restarts and anything running will stop.",
          confirmText: `Switch to ${name}`,
          destructive: true,
        });
        if (!ok) return;
      }
      setBusy(true);
      setError(null);
      try {
        await saveHarnessPermissions({ ...permissions, posture: tier });
        await runtime?.refresh();
      } catch (cause) {
        setError(
          cause instanceof Error
            ? cause.message
            : "The safety level could not be changed.",
        );
      } finally {
        setBusy(false);
      }
    },
    [permissions, current, busy, confirm, runtime],
  );

  return { available, current, busy, error, pick, ConfirmDialog };
}

/** The "Safety level" rows inside the Mode menu (see usePostureControl). */
function PostureMenuSection({
  current,
  busy,
  error,
  onPick,
}: {
  current: string;
  busy: boolean;
  error: string | null;
  onPick: (tier: string) => void;
}) {
  return (
    <>
      <DropdownMenuSeparator />
      <DropdownMenuGroup>
        <DropdownMenuLabel className="text-xs text-muted-foreground">
          Safety level
        </DropdownMenuLabel>
        {POSTURE_MENU_OPTIONS.map((option) => (
          <DropdownMenuItem
            key={option.value}
            className="items-start gap-2"
            disabled={busy}
            data-testid={`posture-option-${option.value}`}
            onClick={() => onPick(option.value)}
          >
            <Check
              className={cn(
                "mt-0.5 size-4 shrink-0",
                current === option.value
                  ? "text-foreground"
                  : "text-transparent",
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
        {error ? (
          <p role="alert" className="px-2 py-1 text-xs text-destructive">
            {error}
          </p>
        ) : null}
      </DropdownMenuGroup>
    </>
  );
}

/**
 * The session permission-mode selector (Manual / Plan / Accept edits), the
 * first control in the composer bar. Same pill + container-collapse idiom as
 * the model selector: the current mode wide, the bare word "Mode" narrow,
 * always the full selection on the title. The label carries the mode's
 * colour cue (plan = info, accept edits = success, Manual plain) so the
 * posture reads at a glance; ⇧Tab in the editor cycles it. A switch made
 * mid-run is HELD by the caller until the run ends (the daemon refuses a
 * mid-turn change) and reads "· pending" until the daemon confirms it; a
 * caller without that contract disables the pill while streaming instead.
 */
export function ModeSelector({
  mode,
  onModeChange,
  disabled,
  pending = false,
}: {
  mode: SessionPermissionMode;
  onModeChange: (mode: SessionPermissionMode) => void;
  disabled?: boolean;
  /** The shown `mode` is a held switch, not yet confirmed by the daemon. */
  pending?: boolean;
}) {
  const posture = usePostureControl();
  const label = permissionModeLabel(mode);
  const shown = pending ? `${label} · pending` : label;
  // Plan / Accept edits carry a filled dot in the box tint's hue (the TUI's
  // mode-coloured rail), kept visible where the label collapses to "Mode".
  const dot = modeDotClass(mode);
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            size="sm"
            className={cn(GHOST_TRIGGER_CLASS, modeAccentClass(mode))}
            disabled={disabled}
            title={`Permission mode: ${label}${pending ? " (pending — applies when the run ends)" : ""} — ⇧Tab cycles`}
          >
            {dot && (
              <span
                aria-hidden
                data-testid="mode-dot"
                className={cn("size-2 shrink-0 rounded-full", dot)}
              />
            )}
            <span className="max-w-40 truncate @max-md:hidden">{shown}</span>
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
          {posture.available && (
            <PostureMenuSection
              current={posture.current}
              busy={posture.busy}
              error={posture.error}
              onPick={(tier) => void posture.pick(tier)}
            />
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      {posture.ConfirmDialog}
    </>
  );
}

/**
 * The mobile composer's left-hand options button: a + that opens a bottom
 * sheet with the composer's secondary actions — Add a file, Model —
 * replacing the desktop toolbar row (hidden on mobile). Model
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
  modePending = false,
  profile,
  onProfileChange,
  models,
  autoModelLabel,
  effort,
  onEffortChange,
  onSwitchEffort,
  currentEffort,
  currentModelReasoning,
  effortSupported = true,
  onInsertFromMcp,
}: {
  onFilesSelected: (files: File[]) => void;
  /** Present when the daemon grants `capabilities.mcp`: opens the MCP prompt
   *  or resource picker (the desktop toolbar's "Insert from MCP"). */
  onInsertFromMcp?: (kind: McpPickerKind) => void;
  /** Draft pick ("" = auto row); null = reset to untouched (Studio default). */
  onModelChange?: (id: string | null) => void;
  modelLockedLabel?: string;
  /** Present in a live chat: picking forks the chat onto the model (the
   *  daemon fixes a session's model at create). null = auto-routed. */
  onSwitchModel?: (option: ComposerModelOption | null) => void;
  /** The live session's current model id ("" = auto), for the checkmark. */
  currentModelId?: string;
  mode?: SessionPermissionMode;
  onModeChange?: (mode: SessionPermissionMode) => void;
  modeDisabled?: boolean;
  /** The shown `mode` is a held switch awaiting the run's end ("· pending"). */
  modePending?: boolean;
  /** The chat's tool profile (see ChatInput): rows in the Mode sheet on a
   *  draft, a read-only line on a live chat, "· No FS" on the Mode row. */
  profile?: SessionToolProfile;
  onProfileChange?: (profile: SessionToolProfile) => void;
  models?: ComposerModelOption[];
  autoModelLabel?: string;
  /** Draft: the pending reasoning-effort WIRE value ("" = auto). */
  effort?: string;
  onEffortChange?: (wire: string) => void;
  /** Live chat: picking forks the chat onto the effort tier. */
  onSwitchEffort?: (wire: string) => void;
  /** The live session's resolved_model.reasoning_effort ("" = auto). */
  currentEffort?: string;
  /** The live session's model `reasoning` flag (false → warning row). */
  currentModelReasoning?: boolean;
  /** False when the daemon's model_selection capability is off. */
  effortSupported?: boolean;
}) {
  const [menuOpen, setMenuOpen] = useState(false);
  const [sub, setSub] = useState<"mode" | "model" | "memory" | null>(null);
  // Draft: null = untouched (Studio default applies), "" = auto on purpose.
  const [model, setModel] = useState<string | null>(null);
  const [localEffort, setLocalEffort] = useState<string>(DEFAULT_EFFORT);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const modelOptions = [autoModel(autoModelLabel), ...(models ?? [])];
  const { defaultModel: studioDefault } = useDefaultModel();
  const draft = resolveDraftModel(model, studioDefault, modelOptions);
  const draftId = draft.id;
  const selectedModel =
    modelOptions.find((m) => m.id === draftId) ?? modelOptions[0];
  // Switch mode ignores the local pick state — the session's model is the
  // truth, and a stale/disabled id still labels honestly as itself.
  const switchId = currentModelId ?? AUTO_MODEL_ID;
  const switchSelected = modelOptions.find((m) => m.id === switchId) ?? {
    id: switchId,
    label: switchId || autoModel(autoModelLabel).label,
  };
  // Same two-mode split as the desktop selector: the draft's pending pick
  // (caller-controlled when wired) vs the live session's EFFECTIVE tier.
  const draftEffort = effort ?? localEffort;
  const selectedEffort = onSwitchModel ? (currentEffort ?? "") : draftEffort;
  const showEffort =
    effortSupported && (onSwitchModel ? Boolean(onSwitchEffort) : true);
  const modelReasoning = onSwitchModel
    ? (currentModelReasoning ??
      modelOptions.find((m) => m.id === switchId)?.reasoning)
    : selectedModel.reasoning;
  const withEffort = (label: string) =>
    showEffort ? effortTriggerLabel(label, selectedEffort) : label;

  const menuRow =
    "flex w-full items-center gap-3 px-4 py-3 text-sm transition-colors hover:bg-muted/50 disabled:opacity-50";

  return (
    <>
      <input
        ref={fileInputRef}
        type="file"
        multiple
        className="hidden"
        onChange={(event) => {
          const files = Array.from(event.target.files ?? []);
          event.target.value = "";
          if (files.length > 0) onFilesSelected(files);
        }}
      />
      {/* `/models` / `/effort` on a phone: open the sheet on its Model page. */}
      {(onSwitchModel || onModelChange) && (
        <ModelPickerOpener
          surface="mobile"
          onOpen={() => {
            setSub("model");
            setMenuOpen(true);
          }}
        />
      )}
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
            {onInsertFromMcp && (
              <>
                <button
                  type="button"
                  className={menuRow}
                  onClick={() => {
                    setMenuOpen(false);
                    onInsertFromMcp("prompt");
                  }}
                >
                  <Plug className="size-4 text-muted-foreground" />
                  Insert an MCP prompt
                </button>
                <button
                  type="button"
                  className={menuRow}
                  onClick={() => {
                    setMenuOpen(false);
                    onInsertFromMcp("resource");
                  }}
                >
                  <Plug className="size-4 text-muted-foreground" />
                  Insert an MCP resource
                </button>
              </>
            )}
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
                <span
                  className={cn(
                    "inline-flex items-center gap-1.5 text-muted-foreground",
                    modeAccentClass(mode ?? "default"),
                  )}
                >
                  {modeDotClass(mode ?? "default") && (
                    <span
                      aria-hidden
                      data-testid="mode-dot"
                      className={cn(
                        "size-2 shrink-0 rounded-full",
                        modeDotClass(mode ?? "default"),
                      )}
                    />
                  )}
                  {permissionModeLabel(mode ?? "default")}
                  {modePending ? " · pending" : ""}
                  {toolProfilePillSuffix(profile ?? "")}
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
                  ? withEffort(switchSelected.label)
                  : (modelLockedLabel ?? withEffort(selectedModel.label))}
              </span>
              {(!modelLockedLabel || onSwitchModel) && (
                <ChevronRight className="size-4 text-muted-foreground/60" />
              )}
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
            <ToolProfileSheetRows
              profile={profile}
              onProfileChange={
                onProfileChange
                  ? (next) => {
                      onProfileChange(next);
                      setSub(null);
                    }
                  : undefined
              }
            />
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
            <ModelSheetSection
              options={modelOptions}
              selectedId={onSwitchModel ? switchId : draftId}
              currentLabel={
                onSwitchModel ? switchSelected.label : selectedModel.label
              }
              provenance={
                onSwitchModel
                  ? liveModelProvenance(switchId, studioDefault, modelOptions)
                  : draft.provenance
              }
              live={Boolean(onSwitchModel)}
              onPick={(m) => {
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
            {showEffort && (
              <EffortSheetSection
                value={selectedEffort}
                live={Boolean(onSwitchModel)}
                modelReasoning={modelReasoning}
                onPick={(wire) => {
                  if (onSwitchModel) {
                    setSub(null);
                    if (wire !== selectedEffort) onSwitchEffort?.(wire);
                    return;
                  }
                  setLocalEffort(wire);
                  onEffortChange?.(wire);
                }}
              />
            )}
            {!onSwitchModel && (
              <>
                <div className="mx-4 my-1 h-px bg-border" />
                <button
                  type="button"
                  disabled={model === null && draftEffort === DEFAULT_EFFORT}
                  onClick={() => {
                    setModel(null);
                    setLocalEffort(DEFAULT_EFFORT);
                    onModelChange?.(null);
                    onEffortChange?.(DEFAULT_EFFORT);
                    setSub(null);
                  }}
                  className={cn(menuRow, "text-muted-foreground")}
                >
                  <RotateCcw className="size-4" />
                  Reset to default
                </button>
              </>
            )}
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
  "Pull in tools and expertise by including @agent, @file or +Files";

/**
 * The `@` menu: the file rows first — "Attach a file…" while the query could
 * still spell "file", "Mention path @…" for a path-shaped token — then the
 * agent list filtered by handle prefix or name substring. Exported for its
 * unit test.
 */
export function agentMenuItems(query: string): ComposerMenuItem[] {
  const q = query.toLowerCase();
  const agents = getAgentMentions()
    .filter((a) => a.handle.startsWith(q) || a.name.toLowerCase().includes(q))
    .map((a) => ({
      id: a.handle,
      label: a.handle,
      primary: a.name,
      secondary: a.description,
    }));
  return [...fileMenuRows(query), ...agents];
}

/**
 * Filter the slash-command list for the `/`-command menu by name prefix.
 * Studio's own builtins (`/clear /help /session /retry /diagnostics
 * /compact`, in that fixed order, `gates` hiding the capability-gated ones)
 * come first and shadow a daemon command of the same name, so what the menu
 * shows is what fires; `builtins: false` (a surface with no local-command
 * handler) lists the daemon's commands only. Exported for its unit test.
 */
export function commandMenuItems(
  query: string,
  options: { builtins?: boolean; gates?: BuiltinGates } = {},
): ComposerMenuItem[] {
  const q = query.toLowerCase();
  const builtins =
    options.builtins === false
      ? []
      : builtinSlashCommands(options.gates ?? CLOSED_BUILTIN_GATES).filter(
          (c) => c.name.startsWith(q),
        );
  const shadowed = new Set<string>(builtins.map((c) => c.name));
  const daemon = getSlashCommands().filter(
    (c) => c.name.startsWith(q) && !shadowed.has(c.name),
  );
  return [
    ...builtins.map((c) => ({
      id: c.name,
      label: c.name,
      primary: `/${c.name}`,
      secondary: c.description,
      builtin: true,
    })),
    ...daemon.map((c) => ({
      id: c.name,
      label: c.name,
      primary: `/${c.name}`,
      secondary: c.description,
    })),
  ];
}

export type ComposerSubmission =
  /** Ordinary text (or a daemon workspace command): steer/queue/send. */
  | { readonly action: "pass" }
  /** A bare Studio built-in: run it locally, never send it. */
  | { readonly action: "builtin"; readonly name: StudioBuiltinCommand }
  /** A held (arguments, second line) or gated-off built-in: keep the text
   *  in the editor and show the warning. */
  | { readonly action: "hold"; readonly warning: string };

/**
 * The composer's built-in interception, extracted pure: what a submission
 * does BEFORE the steer/queue/send branches. Only where a handler is wired
 * (`hasHandler`); otherwise every text passes to the agent. Deliberately
 * independent of `isStreaming` — a built-in typed mid-run is intercepted the
 * same way, never queued or steered. Exported for its unit test.
 */
export function resolveComposerSubmission(input: {
  text: string;
  gates?: BuiltinGates;
  hasHandler: boolean;
  isStreaming?: boolean;
}): ComposerSubmission {
  if (!input.hasHandler) return { action: "pass" };
  const classified = classifySlashLine(
    input.text,
    input.gates ?? CLOSED_BUILTIN_GATES,
  );
  switch (classified.kind) {
    case "builtin":
      return { action: "builtin", name: classified.name };
    case "held":
    case "gated":
      return { action: "hold", warning: classified.reason };
    default:
      return { action: "pass" };
  }
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
 * - ⌘Enter / Ctrl+Enter (`mod`): ALWAYS a newline — idle or streaming, with
 *   or without text, whatever the preference — the unconditional form the
 *   TUI binds to ctrl+j (the editor's own Mod-Enter hardBreak inserts it).
 */
export function resolveComposerAction(input: {
  shift: boolean;
  /** ⌘ or Ctrl held with Enter. Absent = not held. */
  mod?: boolean;
  isStreaming: boolean;
  behavior: EnterSendBehavior;
}): ComposerEnterAction {
  if (input.mod) return "newline";
  if (!input.isStreaming) return input.shift ? "newline" : "send";
  // "Queue only" (the client-level never-steer switch, mecatui --no-steer):
  // Enter AND Shift+Enter queue — there is no opposite to invert into.
  if (input.behavior === "queue-only") return "queue";
  if (!input.shift) return input.behavior;
  return input.behavior === "queue" ? "steer" : "queue";
}

/** What a key does to the held queue from an EMPTY composer. */
export type EmptyComposerQueueAction = "resume" | "edit" | "clear";

/**
 * The empty-composer queue keys, extracted pure (the TUI's paused-queue
 * gestures): only with something queued and no autocomplete menu open —
 *
 * - Enter, idle: send the held queue now (`resume`).
 * - ↑, streaming or idle: pull the held queue back for editing (`edit`).
 * - Esc, idle: drop the held queue (`clear`) — while streaming Esc keeps
 *   its run-cancel meaning (the global close.esc), so null.
 *
 * The caller has already established the composer is empty; a non-empty
 * composer keeps every key's ordinary editing meaning.
 */
export function resolveEmptyComposerKey(input: {
  key: string;
  queuedCount: number;
  isStreaming: boolean;
  menuOpen: boolean;
}): EmptyComposerQueueAction | null {
  if (input.menuOpen || input.queuedCount <= 0) return null;
  switch (input.key) {
    case "Enter":
      return input.isStreaming ? null : "resume";
    case "ArrowUp":
      return "edit";
    case "Escape":
      return input.isStreaming ? null : "clear";
    default:
      return null;
  }
}

/** Open autocomplete menu state, mirrored from TipTap's suggestion lifecycle. */
interface ComposerMenu {
  kind: "agent" | "command";
  items: ComposerMenuItem[];
  index: number;
  /** The text typed after the trigger char (the no-match hint names it). */
  query: string;
  select: (item: ComposerMenuItem) => void;
}

/** The muted source tag on a `/` row: Studio's own or the daemon's. */
export function commandSourceTag(item: ComposerMenuItem): string {
  return item.builtin ? "built-in" : "workspace";
}

/**
 * The `/` palette's empty state, shown INSTEAD of nothing when the typed
 * token matches no command: names the token and says what Enter does (the
 * text goes to the agent unchanged). Null for the `@` menu and for an
 * empty query (the bare `/` with no commands at all shows no popover).
 */
export function commandNoMatchHint(menu: {
  kind: "agent" | "command";
  items: readonly unknown[];
  query: string;
}): string | null {
  if (menu.kind !== "command" || menu.items.length > 0 || !menu.query) {
    return null;
  }
  return `No command matches /${menu.query} — Enter sends it as text`;
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
  rows,
  compact = false,
  mobileDocked = false,
  focusKey,
  onSend,
  onQueue,
  onSteer,
  onModelChange,
  disabled = false,
  isStreaming = false,
  modeSwitchDeferred = false,
  pendingMode = null,
  appendText,
  onAppendConsumed,
  initialText,
  initialFiles,
  onInitialTextConsumed,
  queuedCount = 0,
  onResumeQueue,
  onEditAllQueued,
  onClearQueue,
  modelLockedLabel,
  onSwitchModel,
  currentModelId,
  effort,
  onEffortChange,
  onSwitchEffort,
  currentEffort,
  currentModelReasoning,
  effortSupported,
  models,
  autoModelLabel,
  onPreviewAttachment,
  mode,
  onModeChange,
  profile,
  onProfileChange,
  onLocalCommand,
  builtinGates,
  mediaCapabilities,
  escapePress,
  onDraftChange,
  draftKey,
}: ChatInputProps) {
  const placeholder = placeholderProp ?? DEFAULT_PLACEHOLDER;
  // Plain-text mirror of the editor, kept in sync via onUpdate. Used only for
  // "is there something to send" checks and the streaming border state;
  // the editor document is the source of truth for the message itself.
  const [text, setText] = useState("");
  const [attachedFiles, setAttachedFiles] = useState<File[]>([]);
  // A built-in's local warning (held arguments, a gated-off command, a
  // refused dispatch), shown above the input until the next edit.
  const [notice, setNotice] = useState<string | null>(null);
  const [isDragOver, setIsDragOver] = useState(false);
  const [isWindowDrag, setIsWindowDrag] = useState(false);
  const dragCountRef = useRef(0);
  const windowDragCountRef = useRef(0);

  // Autocomplete menu, mirrored from TipTap's suggestion lifecycle so we can
  // render the same full-width popover the pre-TipTap composer used. `menuRef`
  // gives the suggestion keydown handler a synchronous read of current state.
  const [menu, setMenu] = useState<ComposerMenu | null>(null);

  // The hidden file input behind "+", shared with the `@` menu's "Attach a
  // file…" row so both open the one native picker.
  const attachInputRef = useRef<HTMLInputElement>(null);
  // The staging gate (the TUI's `attach:` refusal, at stage time): a file
  // the session's modalities reject — or a text file too big to inline — is
  // never staged; its reason shows above the input like a built-in's
  // warning. With no gate supplied only the size limits apply.
  const stageFiles = useCallback(
    (incoming: File[]) => {
      const gate = mediaCapabilities ?? { image: true, audio: true };
      const accepted: File[] = [];
      const reasons: string[] = [];
      for (const file of incoming) {
        const verdict = classifyAttachment(file, gate);
        if (verdict.kind === "rejected") reasons.push(verdict.reason);
        else accepted.push(file);
      }
      if (accepted.length > 0) {
        setAttachedFiles((prev) => [...prev, ...accepted]);
      }
      if (reasons.length > 0) setNotice(reasons.join(" "));
    },
    [mediaCapabilities],
  );

  // The Studio-local command handler, read through a ref so the suggestion
  // plugins (built once) always see the latest prop without rebuilding the
  // editor's extensions. Updated in an effect, never during render.
  const onLocalCommandRef = useRef(onLocalCommand);
  useEffect(() => {
    onLocalCommandRef.current = onLocalCommand;
  }, [onLocalCommand]);
  // The palette's gate set, read per keystroke by the suggestion plugin
  // (which lives outside React's render), so a ref rather than a dep.
  const builtinGatesRef = useRef(builtinGates);
  builtinGatesRef.current = builtinGates;

  // Picking a menu row. A Studio builtin (`/help`) acts on selection: the
  // composer clears and the handler runs, so ONE Enter on the menu is enough
  // and no chip is ever inserted. Every other row inserts its atomic chip via
  // the suggestion's `command`.
  const selectMenuItem = useCallback(
    (
      kind: "agent" | "command",
      props: SuggestionProps<ComposerMenuItem>,
      item: ComposerMenuItem,
    ) => {
      if (kind === "agent" && isAttachFileItem(item)) {
        // "Attach a file…": no chip — drop the `@` token, open the picker.
        setMenu(null);
        props.editor.chain().focus().deleteRange(props.range).run();
        attachInputRef.current?.click();
        return;
      }
      const pathText = kind === "agent" ? pathMentionText(item) : null;
      if (pathText !== null) {
        // "Mention path": plain text the model reads (and can Read from the
        // workspace) — never a chip, and no completion is claimed.
        setMenu(null);
        props.editor
          .chain()
          .focus()
          .insertContentAt(props.range, { type: "text", text: `${pathText} ` })
          .run();
        return;
      }
      const handler = onLocalCommandRef.current;
      if (kind === "command" && handler && isStudioBuiltinCommand(item.id)) {
        setMenu(null);
        const outcome = handler(item.id);
        if (outcome && outcome.ok === false) {
          // Refused: show the full command with the reason above it, so
          // what the warning names is what the field holds.
          setComposerText(props.editor, `/${item.id}`);
          setNotice(outcome.warning);
          return;
        }
        props.editor.commands.clearContent();
        setText("");
        if (outcome?.insertText) {
          // `/agents`: type the `@` into the emptied field so the agent
          // roster opens (the suggestion plugin reads the document).
          props.editor.chain().focus().insertContent(outcome.insertText).run();
        }
        return;
      }
      props.command(item);
    },
    [],
  );

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
          query: props.query,
          select: (item) => selectMenuItem(kind, props, item),
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
            query: props.query,
            select: (item) => selectMenuItem(kind, props, item),
          };
        });
      },
      onExit: () => setMenu((m) => (m && m.kind === kind ? null : m)),
    }),
    [selectMenuItem],
  );

  const mentions = useMemo(
    () =>
      createComposerMentions({
        agentItems: agentMenuItems,
        // Builtins only where a handler can answer them; otherwise the menu
        // is the daemon's list alone and `/help` would go to the agent.
        commandItems: (query) =>
          commandMenuItems(query, {
            builtins: onLocalCommandRef.current != null,
            gates: builtinGatesRef.current,
          }),
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
      // The `[Pasted text #N]` chip a large paste is staged behind; its
      // renderText is the payload, so composerText expands it on send.
      PastePlaceholder,
      ...mentions,
    ],
    onUpdate: ({ editor }) => {
      setText(editor.getText({ blockSeparator: "\n" }));
      setNotice(null);
    },
  });

  // "Insert from MCP" (the TUI's f8 prompts / ctrl+r resources pickers):
  // capability-gated on the daemon's `mcp`; the picked text is appended
  // after the draft for review, never sent.
  const mcpInsert = useMcpComposerInsert(editor);

  // The one-shot draft clear (the TUI's ctrl+u ClearPrompt): editor content
  // — `[Pasted text #N]` chips included — the text mirror, the staged files
  // and a built-in's warning, all at once. Shared by the ⌘⇧U chord, the ×
  // button beside Send and the double-Esc guard below; the emptied text
  // mirror also drops the persisted draft (the guards' text effect).
  const clearDraft = useCallback(() => {
    editor?.commands.clearContent();
    setText("");
    setAttachedFiles([]);
    setNotice(null);
  }, [editor]);
  const clearDraftFromButton = useCallback(() => {
    clearDraft();
    editor?.commands.focus();
  }, [clearDraft, editor]);

  // The draft guards (the TUI's esc-esc clear and two-step quit, as their
  // web analogues): a double Esc empties an idle draft, the unsent text is
  // persisted under `draftKey`, and `onDraftChange` feeds the leave guard.
  const { escapeArmed } = useComposerDraftGuards({
    editor,
    text,
    setText,
    escapePress,
    draftKey,
    initialTextPending: Boolean(initialText),
    onDraftChange,
    clearComposer: clearDraft,
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
    if (!editor) return;
    const hasFiles = (initialFiles?.length ?? 0) > 0;
    if (!initialText && !hasFiles) return;
    if (initialText) {
      setComposerText(editor, initialText);
      setText(editor.getText({ blockSeparator: "\n" }));
    }
    // A pulled-back queued message brings its staged files with it.
    if (initialFiles && hasFiles) {
      setAttachedFiles((prev) => [...prev, ...initialFiles]);
    }
    onInitialTextConsumed?.();
    requestAnimationFrame(() => editor.commands.focus("end"));
  }, [initialText, initialFiles, onInitialTextConsumed, editor]);

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
      // A Studio built-in is answered here whatever the action — typed
      // mid-stream it runs (or is refused with a warning), never queues or
      // steers. Only where the surface wired a handler; otherwise the text
      // goes to the agent like any other message.
      const submission = resolveComposerSubmission({
        text: trimmed,
        gates: builtinGates,
        hasHandler: onLocalCommand != null,
      });
      if (submission.action === "hold") {
        setNotice(submission.warning);
        return;
      }
      if (submission.action === "builtin" && onLocalCommand) {
        const outcome = onLocalCommand(submission.name);
        if (outcome && outcome.ok === false) {
          setNotice(outcome.warning);
          return;
        }
        editor?.commands.clearContent();
        setText("");
        if (outcome?.insertText && editor) {
          // `/agents` typed out: the `@` opens the roster in the emptied field.
          editor.chain().focus().insertContent(outcome.insertText).run();
        }
        return;
      }
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
        // The staged files travel with the queued text (and come back with
        // it when the row is edited).
        onQueue(trimmed, files);
        editor?.commands.clearContent();
        setText("");
        setAttachedFiles([]);
        return;
      }
      onSend?.(trimmed, files);
      editor?.commands.clearContent();
      setText("");
      setAttachedFiles([]);
    },
    [
      editor,
      disabled,
      onQueue,
      onSteer,
      onSend,
      onLocalCommand,
      builtinGates,
      attachedFiles,
    ],
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

  // The permission mode as SHOWN: a held mid-run switch (`pendingMode`) over
  // the daemon-confirmed `mode`. ⇧Tab cycles onward from what is shown and
  // the pill/box tint follow it. The cycle key and the pill share ONE
  // enablement, so the key is never live where the pill is greyed out.
  const shownMode: SessionPermissionMode = pendingMode ?? mode ?? "default";
  const modePending =
    pendingMode != null && pendingMode !== (mode ?? "default");
  const canChangeMode =
    !!onModeChange && !disabled && (!isStreaming || modeSwitchDeferred);

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
      // ⇧Tab cycles the permission mode (the TUI's shift+tab) — from the
      // editor only, and never over an open `/` or `@` menu, where Tab picks
      // the highlighted row (`resolveModeCycleKey` owns the table). A held
      // mid-run switch is the caller's; a surface that can't change the mode
      // leaves the key to the browser's reverse-focus.
      if (
        resolveModeCycleKey({
          key: event.key,
          shiftKey: event.shiftKey,
          metaKey: event.metaKey,
          ctrlKey: event.ctrlKey,
          altKey: event.altKey,
          menuOpen: menu != null,
          canChangeMode,
        })
      ) {
        event.preventDefault();
        event.stopPropagation();
        onModeChange?.(nextPermissionMode(shownMode));
        return;
      }
      // ⌘⇧U empties the whole draft (the TUI's ctrl+u ClearPrompt) — text,
      // paste chips, staged files — from the editor only; the × button and
      // the double-Esc guard are the same clear. Idle or streaming: it never
      // touches the run. Ahead of the menu: clearing a `/foo` draft closes
      // its menu with it.
      if (isClearDraftChord(event)) {
        event.preventDefault();
        event.stopPropagation();
        clearDraft();
        return;
      }
      if (menu) {
        if (handleMenuNavKey(event, menu, setMenu)) {
          event.preventDefault();
          event.stopPropagation();
        }
        return;
      }
      // The held-queue gestures live on an EMPTY composer only (Enter sends
      // the queue, ↑ pulls it back, Esc clears it); a bare key, no chords.
      if (
        queuedCount > 0 &&
        !event.shiftKey &&
        !event.metaKey &&
        !event.ctrlKey &&
        !event.altKey &&
        (editor ? composerText(editor) : "") === ""
      ) {
        const queueAction = resolveEmptyComposerKey({
          key: event.key,
          queuedCount,
          isStreaming,
          menuOpen: false,
        });
        if (queueAction) {
          // preventDefault also keeps the global close.esc from firing on
          // the same Escape.
          event.preventDefault();
          event.stopPropagation();
          if (queueAction === "resume") onResumeQueue?.();
          else if (queueAction === "edit") onEditAllQueued?.();
          else onClearQueue?.();
          return;
        }
      }
      if (event.key !== "Enter") return;
      // ⌘Enter / Ctrl+Enter is ALWAYS a new line (the TUI's ctrl+j) — idle or
      // streaming, with or without text: fall through to the editor's own
      // Mod-Enter hardBreak without preventDefault, never into send/queue/
      // steer (`resolveComposerAction` says the same for `mod`).
      if (isAlwaysNewlineChord(event)) return;
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
  }, [
    editor,
    menu,
    isStreaming,
    actOnEnter,
    clearDraft,
    queuedCount,
    onResumeQueue,
    onEditAllQueued,
    onClearQueue,
    canChangeMode,
    shownMode,
    onModeChange,
  ]);

  // Paste (the TUI's ctrl+v). Clipboard files with no text (a screenshot)
  // stage through the same gate as the picker and drop; a LARGE text paste
  // (≥ 2000 chars alone or with the current text, or ≥ 30 lines) becomes one
  // atomic `[Pasted text #N]` chip that composerText expands on send, steer
  // and queue; anything smaller falls through to ProseMirror's own paste.
  // Capture-phase on the editor DOM like the keydown listener above, so a
  // handled paste stops before ProseMirror's bubble-phase handler would
  // insert the text too. Inert while disabled (the editor is read-only).
  const pasteCounterRef = useRef(0);
  useEffect(() => {
    // N restarts with an empty composer (a send, a wipe); within one draft
    // it is monotonic, so a deleted chip leaves a numbering gap, as in the TUI.
    if (text === "") pasteCounterRef.current = 0;
  }, [text]);
  useEffect(() => {
    const dom = editor?.view.dom;
    if (!editor || !dom) return;
    const onPaste = (event: ClipboardEvent) => {
      if (disabled) return;
      const outcome = applyComposerPaste(
        editor,
        readPasteClipboard(event.clipboardData),
        {
          stageFiles,
          nextNumber: () => ++pasteCounterRef.current,
        },
      );
      if (outcome === "default") return;
      event.preventDefault();
      event.stopPropagation();
    };
    dom.addEventListener("paste", onPaste, true);
    return () => dom.removeEventListener("paste", onPaste, true);
  }, [editor, disabled, stageFiles]);

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
        // Any file: images/audio as media parts, the rest inlined as text;
        // the staging gate refuses what this session cannot take.
        const droppedFiles = Array.from(e.dataTransfer.files);
        if (droppedFiles.length > 0) stageFiles(droppedFiles);
      }}
      className={cn(
        "relative rounded-2xl bg-zinc-50 dark:bg-zinc-900",
        mobileDocked && "max-[499px]:rounded-none max-[499px]:bg-transparent",
      )}
    >
      {/* Autocomplete popover (agent @-mentions or slash commands), floating
          above the input box. Driven by TipTap's suggestion lifecycle. */}
      {menu && menu.items.length === 0 && commandNoMatchHint(menu) && (
        // The `/` palette's empty state: not a row (nothing to pick — Enter
        // falls through to the plain send), just the hint.
        <div className="absolute bottom-full left-0 right-0 z-30 mb-2 overflow-hidden rounded-xl border border-border bg-popover text-popover-foreground shadow-xl">
          <p
            role="status"
            data-testid="composer-command-no-match"
            className="px-3 py-2 text-xs text-muted-foreground"
          >
            {commandNoMatchHint(menu)}
          </p>
        </div>
      )}
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
                  <>
                    {item.builtin && (
                      <Terminal
                        className="size-4 shrink-0 text-muted-foreground"
                        aria-label="Studio command"
                      />
                    )}
                    <span className="font-mono text-sm">/{item.id}</span>
                  </>
                ) : (
                  <>
                    {isFileMenuItem(item) ? (
                      <Paperclip
                        className="size-4 shrink-0 text-muted-foreground"
                        aria-label="File"
                      />
                    ) : (
                      <Bot className="size-4 shrink-0 text-muted-foreground" />
                    )}
                    <span className="shrink-0 text-sm font-medium">
                      {item.primary}
                    </span>
                  </>
                )}
                <span className="truncate text-xs text-muted-foreground">
                  {item.secondary}
                </span>
                {menu.kind === "command" && (
                  // Where the row comes from: Studio itself (runs locally)
                  // or the daemon's workspace commands (sent as a prompt).
                  <span
                    data-testid="composer-command-source"
                    className="ml-auto shrink-0 text-[10px] uppercase tracking-wide text-muted-foreground/80"
                  >
                    {commandSourceTag(item)}
                  </span>
                )}
              </button>
            ))}
          </div>
        </div>
      )}
      {/* A built-in's local warning: the typed text stays in the field. */}
      {notice && (
        <p
          role="status"
          data-testid="composer-notice"
          className="mb-1.5 flex items-center gap-1.5 px-1 text-xs text-warning"
        >
          <TriangleAlert className="size-3.5 shrink-0" aria-hidden="true" />
          {notice}
        </p>
      )}
      {/* The double-Esc arm (the TUI's esc-esc): a second Esc inside the
          window empties the draft; any edit disarms. */}
      {escapeArmed && (
        <p
          role="status"
          aria-live="polite"
          data-testid="composer-escape-armed"
          className="mb-1.5 px-1 text-xs text-muted-foreground"
        >
          {ESCAPE_ARMED_HINT}
        </p>
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
          // One state at a time — drag-over > window drag > streaming with a
          // draft > mode tint (Plan / Accept edits: the TUI recolours its
          // input by mode; Manual keeps the plain border). The table is
          // composerFrameClass; a surface with no Mode selector never tints.
          composerFrameClass({
            mode: onModeChange ? shownMode : undefined,
            isDragOver,
            isWindowDrag,
            isStreaming,
            hasText,
          }),
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
              onFilesSelected={stageFiles}
              onModelChange={onModelChange}
              modelLockedLabel={modelLockedLabel}
              onSwitchModel={onSwitchModel}
              currentModelId={currentModelId}
              effort={effort}
              onEffortChange={onEffortChange}
              onSwitchEffort={onSwitchEffort}
              currentEffort={currentEffort}
              currentModelReasoning={currentModelReasoning}
              effortSupported={effortSupported}
              mode={shownMode}
              onModeChange={onModeChange}
              modeDisabled={disabled || (isStreaming && !modeSwitchDeferred)}
              modePending={modePending}
              profile={profile}
              onProfileChange={onProfileChange}
              onInsertFromMcp={
                mcpInsert.available && !disabled ? mcpInsert.open : undefined
              }
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
            <EditorContent
              editor={editor}
              className="composer-editor"
              // Resting rows: the stylesheet's three unless `rows`/`compact`
              // ask otherwise (--composer-min-rows; the eight-row scroll cap
              // and the mobile one-row pin live in globals.css).
              style={composerEditorStyle({ rows, compact })}
            />
          </div>
          {/* Mobile: × clears the whole draft (text + staged files) beside
              the mic/send slot; hidden while there is nothing to clear. */}
          <div className="hidden max-[499px]:block">
            <ClearDraftButton
              hasDraft={hasText || attachedFiles.length > 0}
              disabled={disabled}
              onClear={clearDraftFromButton}
            />
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
            onFilesSelected={stageFiles}
            inputRef={attachInputRef}
          />
          <McpInsertMenu insert={mcpInsert} disabled={disabled} />
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
          {/* Right: × clears the whole draft (the TUI's ctrl+u; also ⌘⇧U
              and Esc Esc), then Send. The × shows only with something staged. */}
          <div className="ml-auto flex items-center gap-1">
            <ClearDraftButton
              hasDraft={hasText || attachedFiles.length > 0}
              disabled={disabled}
              onClear={clearDraftFromButton}
            />
            <Button
              size="icon"
              className="size-8 rounded-full bg-brand text-brand-foreground hover:bg-brand/90 disabled:opacity-50"
              onClick={handleSend}
              disabled={disabled || !hasText}
              aria-label="Send message"
            >
              <ArrowUp className="size-4" />
            </Button>
          </div>
        </div>
      </div>
      {/* Toolbar sits BEHIND the input box: negative top margin pulls it up
          so its top edge overlaps the input box's bottom rounded corners,
          making the two boxes appear to share a single outline. */}
      {/* Hidden on mobile: Model lives in the + options sheet. */}
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
                mode={shownMode}
                onModeChange={onModeChange}
                disabled={disabled || (isStreaming && !modeSwitchDeferred)}
                pending={modePending}
              />
            )}
            {onModeChange && (
              <ToolProfileSelector
                profile={profile}
                onProfileChange={onProfileChange}
                disabled={disabled}
                className={GHOST_TRIGGER_CLASS}
              />
            )}
            <ModelEffortSelector
              models={models}
              autoModelLabel={autoModelLabel}
              lockedLabel={modelLockedLabel}
              onSwitchModel={onSwitchModel}
              currentModelId={currentModelId}
              effort={effort}
              onEffortChange={onEffortChange}
              onSwitchEffort={onSwitchEffort}
              currentEffort={currentEffort}
              currentModelReasoning={currentModelReasoning}
              effortSupported={effortSupported}
              onModelChange={(id) => {
                onModelChange?.(id);
              }}
            />
          </>
        )}
      </div>
      {mcpInsert.dialogs}
    </div>
  );
}
