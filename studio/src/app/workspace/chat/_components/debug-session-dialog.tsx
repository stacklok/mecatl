"use client";

import { Loader2 } from "lucide-react";
import { useCallback, useEffect, useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { useDiagnosticsReport } from "@/features/agent/hooks/use-diagnostics-report";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import { debugOpeningPrompt } from "@/lib/harness/debug";
import { fetchHarnessMcpServerNames } from "@/lib/harness/mcp-sources";
import { shortSessionHandle } from "@/lib/protocol/session-handle";

/**
 * The ADR-0254 consent disclosure, word for word: invoking the debugger sends
 * the target's STORED transcript and event evidence — secrets included — to
 * the model, even though the target itself is never modified.
 */
export const DEBUG_SESSION_CONSENT =
  "This creates a separate diagnostic chat bound to this session. " +
  "The session's stored transcript and event evidence — including " +
  "anything sensitive it contains — will be sent to the model as " +
  "debugging evidence. The session itself is read-only to the " +
  "debugger and is never modified.";

/** Why attaching a server never turns the debugger into an auto-publisher. */
export const DEBUG_MCP_APPROVAL_HINT =
  "Every debugger MCP call asks for your approval, even under auto or yolo posture. Allow once approves one call; Always allow is not learned for these calls.";

/** Shown when the daemon lists no configured MCP server. */
export const NO_MCP_SERVER_NOTE =
  "No MCP server is configured on this daemon — connect one in Settings → MCP tools.";

/** Shown when the daemon could not list its servers (an older daemon). */
export const MCP_LIST_UNAVAILABLE_NOTE =
  "Studio could not list this daemon's MCP servers. Enter the configured names; the daemon refuses a name it does not know.";

/** The runtime-context option's label (the TUI appends the same report). */
export const RUNTIME_CONTEXT_LABEL =
  "Include a Studio/daemon diagnostics report as runtime context";

/** What the runtime-context report is — and is not. */
export const RUNTIME_CONTEXT_NOTE =
  "The same sanitized report /diagnostics sends: client build, daemon identity, mode, posture and endpoint class — nothing from this chat. The debugger is told it is context about this client, never evidence about the target.";

/** Heading over the opening message the debug chat submits on its own. */
export const OPENING_MESSAGE_HEADING = "Opening message, sent automatically";

/** The consent the operator gives: target, evidence, servers, first turn. */
export interface DebugSessionChoice {
  /** The attached reporting-server names ([] = none). */
  mcpServers: string[];
  /** Append the Studio/daemon diagnostics report to the opening message. */
  includeRuntimeContext: boolean;
}

/** What the workspace receives after the consent: the choice, resolved. */
export interface DebugSessionRequest {
  mcpServers: string[];
  /** The composed diagnostics report, or null when not requested / failed. */
  runtimeContext: string | null;
}

/** The chat the consent is about; the title is what the sidebar shows. */
export interface DebugSessionTarget {
  id: string;
  title: string;
}

/**
 * Splits a typed server list on commas and whitespace, trims, drops empties
 * and duplicates (the daemon rejects a duplicate name with 400).
 */
export function parseServerNames(text: string): string[] {
  const out: string[] = [];
  for (const raw of text.split(/[,\s]+/)) {
    const name = raw.trim();
    if (name && !out.includes(name)) out.push(name);
  }
  return out;
}

/** What the dialog knows about the daemon's configured MCP servers. */
export type McpServerListing =
  | { status: "loading" }
  | { status: "ready"; names: string[] }
  | { status: "unavailable" };

export interface DebugSessionDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Whether the daemon accepts `debug_mcp_servers` (`capabilities.debug_mcp`).
   *  False hides the whole attach section — a plain debug session. */
  mcpSupported: boolean;
  /** The daemon's configured MCP servers (GET /v1/mcp/sources): offered as
   *  checkboxes when listed; a text input when the daemon could not list. */
  servers: McpServerListing;
  /** The chat being debugged — named in the title with its 12-column handle
   *  (the same literal mecatui shows), so the consent is unambiguous. */
  target?: DebugSessionTarget | null;
  /** Confirms the consent with the chosen servers and the report option. */
  onConfirm: (choice: DebugSessionChoice) => void;
}

/**
 * The "Debug with AI" consent dialog (ADR 0254): the mandated disclosure,
 * and — only when the daemon's `debug_mcp` capability is on — the choice of
 * already-configured server-global MCP servers the debugger may borrow, so
 * it can (after fresh approval of every call) act on what it finds, e.g.
 * publish the issue it drafted. The TUI's `--debug-mcp NAME` flag, as a
 * picker.
 */
export function DebugSessionDialog({
  open,
  onOpenChange,
  mcpSupported,
  servers,
  target,
  onConfirm,
}: DebugSessionDialogProps) {
  const [selected, setSelected] = useState<string[]>([]);
  const [typed, setTyped] = useState("");
  // On by default: the TUI always appends its runtime context, and the
  // report carries nothing from the target — opting OUT is the exception.
  const [includeRuntimeContext, setIncludeRuntimeContext] = useState(true);
  const baseId = useId();

  // A fresh choice per opening: a selection made for one target must not
  // silently ride into the next debug session.
  useEffect(() => {
    if (open) {
      setSelected([]);
      setTyped("");
      setIncludeRuntimeContext(true);
    }
  }, [open]);

  const toggle = (name: string, checked: boolean) =>
    setSelected((prev) =>
      checked
        ? prev.includes(name)
          ? prev
          : [...prev, name]
        : prev.filter((n) => n !== name),
    );

  const chosen = !mcpSupported
    ? []
    : servers.status === "unavailable"
      ? parseServerNames(typed)
      : selected;

  const attachSection = mcpSupported ? (
    <fieldset className="space-y-2 rounded-md border border-border p-3">
      <legend className="px-1 text-sm font-medium">
        Attach debugger MCP servers
      </legend>
      {servers.status === "loading" && (
        <p
          className="flex items-center gap-2 text-xs text-muted-foreground"
          role="status"
        >
          <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
          Listing configured MCP servers…
        </p>
      )}
      {servers.status === "ready" && servers.names.length === 0 && (
        <p className="text-xs text-muted-foreground">{NO_MCP_SERVER_NOTE}</p>
      )}
      {servers.status === "ready" && servers.names.length > 0 && (
        <ul className="space-y-1.5">
          {servers.names.map((name, index) => {
            const id = `${baseId}-server-${index}`;
            return (
              <li key={name} className="flex items-center gap-2">
                <Checkbox
                  id={id}
                  checked={selected.includes(name)}
                  onCheckedChange={(value) => toggle(name, value === true)}
                />
                <Label htmlFor={id} className="font-mono text-xs font-normal">
                  {name}
                </Label>
              </li>
            );
          })}
        </ul>
      )}
      {servers.status === "unavailable" && (
        <div className="space-y-1.5">
          <Label htmlFor={`${baseId}-names`} className="text-xs">
            Server names (comma-separated)
          </Label>
          <Input
            id={`${baseId}-names`}
            value={typed}
            onChange={(event) => setTyped(event.target.value)}
            placeholder="github, slack"
            autoComplete="off"
            spellCheck={false}
          />
          <p className="text-xs text-muted-foreground">
            {MCP_LIST_UNAVAILABLE_NOTE}
          </p>
        </div>
      )}
      <p className="text-xs text-muted-foreground" role="note">
        {DEBUG_MCP_APPROVAL_HINT}
      </p>
    </fieldset>
  ) : null;

  const targetTitle = target ? target.title.trim() || "Untitled chat" : "";
  const targetHandle = target ? shortSessionHandle(target.id) : "";
  const runtimeContextId = `${baseId}-runtime-context`;
  // The first turn the debug chat submits on its own, spelled here so the
  // operator reads it BEFORE it leaves (the report is appended at send time
  // when the switch is on; its shape is the note's business).
  const openingMessage = debugOpeningPrompt({ servers: chosen });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {target ? `Debug with AI: ${targetTitle}` : "Debug with AI"}
          </DialogTitle>
          <DialogDescription>{DEBUG_SESSION_CONSENT}</DialogDescription>
        </DialogHeader>
        {target && (
          <p className="text-xs text-muted-foreground">
            Target chat handle:{" "}
            <code className="rounded bg-muted px-1 py-0.5 font-mono text-foreground">
              {targetHandle}
            </code>{" "}
            — the same handle mecatui shows for this chat.
          </p>
        )}
        {attachSection}
        <div className="flex items-start gap-3 rounded-md border border-border p-3">
          <Switch
            id={runtimeContextId}
            checked={includeRuntimeContext}
            onCheckedChange={(value) =>
              setIncludeRuntimeContext(value === true)
            }
            aria-describedby={`${runtimeContextId}-note`}
          />
          <div className="min-w-0 space-y-1">
            <Label htmlFor={runtimeContextId} className="text-sm font-medium">
              {RUNTIME_CONTEXT_LABEL}
            </Label>
            <p
              id={`${runtimeContextId}-note`}
              className="text-xs text-muted-foreground"
            >
              {RUNTIME_CONTEXT_NOTE}
            </p>
          </div>
        </div>
        <div className="space-y-1">
          <p className="text-xs font-medium text-muted-foreground">
            {OPENING_MESSAGE_HEADING}
          </p>
          <p
            className="rounded-md bg-muted px-2 py-1.5 text-xs whitespace-pre-wrap"
            data-testid="debug-opening-message"
          >
            {openingMessage}
            {includeRuntimeContext && (
              <span className="text-muted-foreground">
                {"\n\n"}+ the diagnostics report, fenced as runtime context
              </span>
            )}
          </p>
        </div>
        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
          >
            Cancel
          </Button>
          <Button
            type="button"
            onClick={() =>
              onConfirm({ mcpServers: chosen, includeRuntimeContext })
            }
          >
            Send evidence &amp; debug
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * Owns the dialog for the workspace (the `useConfirm` shape):
 * `request(id, title)` opens it for one target; a confirm closes it, composes
 * the diagnostics report when the operator kept that option on (the same
 * sanitized report `/diagnostics` sends — a probe failure just drops it,
 * never blocks the debug session), and hands the caller the target with the
 * resolved request. Reads the daemon's `debug_mcp` capability off the
 * runtime status and lists its configured MCP servers on open — a daemon
 * that cannot list them falls back to typed names.
 */
export function useDebugSessionDialog({
  onCreate,
}: {
  onCreate: (targetSessionId: string, request: DebugSessionRequest) => void;
}) {
  const { serverCapabilities } = useRuntimeStatus();
  const { compose } = useDiagnosticsReport();
  const mcpSupported = serverCapabilities.debug_mcp === true;
  const [target, setTarget] = useState<DebugSessionTarget | null>(null);
  const [servers, setServers] = useState<McpServerListing>({
    status: "loading",
  });

  const open = target !== null;
  useEffect(() => {
    if (!open || !mcpSupported) return;
    const controller = new AbortController();
    setServers({ status: "loading" });
    fetchHarnessMcpServerNames(controller.signal)
      .then((names) => {
        if (!controller.signal.aborted) setServers({ status: "ready", names });
      })
      .catch(() => {
        if (!controller.signal.aborted) setServers({ status: "unavailable" });
      });
    return () => controller.abort();
  }, [open, mcpSupported]);

  const requestDebugSession = useCallback(
    (id: string, title = "") => setTarget({ id, title }),
    [],
  );
  const handleOpenChange = useCallback((next: boolean) => {
    if (!next) setTarget(null);
  }, []);
  const handleConfirm = useCallback(
    (choice: DebugSessionChoice) => {
      if (target === null) return;
      const targetId = target.id;
      setTarget(null);
      const request = (runtimeContext: string | null) =>
        onCreate(targetId, { mcpServers: choice.mcpServers, runtimeContext });
      if (!choice.includeRuntimeContext) {
        request(null);
        return;
      }
      // No session context on purpose: the report describes THIS client and
      // its daemon, not the target — and the debug session's own model is
      // unknown until the daemon has created it.
      void compose()
        .then((report) => request(report.trim() ? report : null))
        .catch(() => request(null));
    },
    [target, onCreate, compose],
  );

  const debugSessionDialog = (
    <DebugSessionDialog
      open={open}
      onOpenChange={handleOpenChange}
      mcpSupported={mcpSupported}
      servers={servers}
      target={target}
      onConfirm={handleConfirm}
    />
  );
  return { requestDebugSession, debugSessionDialog };
}
