"use client";

import type { Editor } from "@tiptap/react";
import { FileText, MessageSquareQuote, Plug } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Kbd } from "@/components/ui/kbd";
import { useOptionalRuntimeStatus } from "@/features/agent/runtime-status";
import { useShortcutBindings } from "@/lib/shortcuts/keymap";
import { keycaps } from "@/lib/shortcuts/registry";
import { useShortcut } from "@/lib/shortcuts/use-shortcuts";
import { appendComposerText } from "./composer-insert";
import { McpPromptPicker } from "./mcp-prompt-picker";
import { McpResourcePicker } from "./mcp-resource-picker";

/**
 * "Insert from MCP" — the composer's entry to mecatui's f8 prompts picker and
 * ctrl+r resources picker. Owned by `ChatInput` through `useMcpComposerInsert`,
 * which holds the open picker, registers the two Tools shortcuts
 * (`mcp.prompts`, `mcp.resources`) and appends the picked text AFTER the
 * current draft (`appendComposerText`) — reviewed, never sent.
 *
 * Capability-gated on the daemon's `capabilities.mcp`: without it the menu
 * renders nothing, the shortcuts stay unregistered (the chords keep their
 * native meaning) and an outside open request is refused, so a daemon that
 * grants only `mcp_connector_status` never receives a prompt or resource RPC
 * (the TUI invariant).
 */

export type McpPickerKind = "prompt" | "resource";

export const MCP_INSERT_LABEL = "Insert from MCP";
const MCP_INSERT_PROMPT_ITEM = "Prompt…";
const MCP_INSERT_RESOURCE_ITEM = "Resource…";

// ── Open requests from outside the composer ─────────────────────────────────
//
// A slash built-in or another surface asks the composer to open a picker
// through `requestOpenMcpPicker`. The composer that most recently mounted or
// held focus is the one that answers (a thread-panel composer next to the
// main one takes over while it has the caret), so exactly one dialog opens.

let currentOpener: ((kind: McpPickerKind) => void) | null = null;

/**
 * Opens the given picker in the active composer. False when no composer is
 * mounted or the daemon lacks the `mcp` capability — the caller then says so
 * instead of silently doing nothing.
 */
export function requestOpenMcpPicker(kind: McpPickerKind): boolean {
  if (!currentOpener) return false;
  currentOpener(kind);
  return true;
}

/** Test seam: forgets the registered composer. */
export function resetMcpPickerOpener(): void {
  currentOpener = null;
}

export interface McpComposerInsert {
  /** The daemon grants `capabilities.mcp`: the entry and shortcuts are live. */
  available: boolean;
  /** Opens one picker (no-op while unavailable). */
  open: (kind: McpPickerKind) => void;
  /** The two picker dialogs; render once inside the composer. */
  dialogs: React.ReactNode;
}

export function useMcpComposerInsert(editor: Editor | null): McpComposerInsert {
  // A composer rendered outside the workspace shell (a display-only surface,
  // a unit test) has no daemon capabilities: the entry simply stays hidden.
  const runtime = useOptionalRuntimeStatus();
  const available = runtime?.serverCapabilities.mcp === true;
  const [picker, setPicker] = useState<McpPickerKind | null>(null);

  const open = useCallback(
    (kind: McpPickerKind) => {
      if (!available) return;
      setPicker(kind);
    },
    [available],
  );

  // Claim outside open requests: on mount, and again whenever this editor
  // takes focus, so the composer under the caret is the one that answers.
  useEffect(() => {
    if (!available) return;
    const claim = () => {
      currentOpener = open;
    };
    const onFocusIn = () => {
      if (editor?.isFocused) claim();
    };
    claim();
    document.addEventListener("focusin", onFocusIn);
    return () => {
      document.removeEventListener("focusin", onFocusIn);
      if (currentOpener === open) currentOpener = null;
    };
  }, [available, open, editor]);

  useShortcut("mcp.prompts", () => open("prompt"), { enabled: available });
  useShortcut("mcp.resources", () => open("resource"), { enabled: available });

  const insert = useCallback(
    (text: string) => {
      if (!editor || !text) return;
      appendComposerText(editor, text);
    },
    [editor],
  );

  const onOpenChange = useCallback((next: boolean) => {
    if (!next) setPicker(null);
  }, []);

  const dialogs = useMemo(
    () =>
      available ? (
        <>
          <McpPromptPicker
            open={picker === "prompt"}
            onOpenChange={onOpenChange}
            onInsert={insert}
          />
          <McpResourcePicker
            open={picker === "resource"}
            onOpenChange={onOpenChange}
            onInsert={insert}
          />
        </>
      ) : null,
    [available, picker, onOpenChange, insert],
  );

  return useMemo(
    () => ({ available, open, dialogs }),
    [available, open, dialogs],
  );
}

/**
 * The desktop toolbar entry: a plug button opening a two-item menu. Renders
 * nothing while the daemon lacks the `mcp` capability.
 */
export function McpInsertMenu({
  insert,
  disabled = false,
}: {
  insert: McpComposerInsert;
  disabled?: boolean;
}) {
  if (!insert.available) return null;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          size="icon"
          className="size-8 rounded-full border-0 bg-transparent text-muted-foreground shadow-none hover:bg-muted/60"
          disabled={disabled}
          aria-label={MCP_INSERT_LABEL}
          title={MCP_INSERT_LABEL}
        >
          <Plug className="size-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-64">
        <DropdownMenuItem onSelect={() => insert.open("prompt")}>
          <MessageSquareQuote className="size-4 text-muted-foreground" />
          <span className="flex-1">{MCP_INSERT_PROMPT_ITEM}</span>
          <ShortcutHint id="mcp.prompts" />
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => insert.open("resource")}>
          <FileText className="size-4 text-muted-foreground" />
          <span className="flex-1">{MCP_INSERT_RESOURCE_ITEM}</span>
          <ShortcutHint id="mcp.resources" />
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** The EFFECTIVE chord (Settings → Keyboard overrides included) as keycaps. */
function ShortcutHint({ id }: { id: string }) {
  const { bindings } = useShortcutBindings();
  const binding = bindings.find((b) => b.id === id);
  if (!binding) return null;
  return (
    <span className="flex items-center gap-0.5" aria-hidden="true">
      {keycaps(binding.effectiveCombo).map((cap) => (
        <Kbd key={cap} size="sm">
          {cap}
        </Kbd>
      ))}
    </span>
  );
}
