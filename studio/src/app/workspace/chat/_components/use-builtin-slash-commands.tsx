"use client";

import { useRouter } from "next/navigation";
import {
  type ReactElement,
  useCallback,
  useMemo,
  useRef,
  useState,
} from "react";
import { toast } from "sonner";
import {
  type BuiltinGates,
  type BuiltinOutcome,
  gatedReason,
  isBuiltinGatedOff,
  type StudioBuiltinCommand,
} from "@/features/agent/composer-builtins";
import {
  DEBUG_ASK_ALREADY_PENDING,
  DEBUG_ASK_NONE_YET,
} from "@/features/agent/debug-ask";
import { useDiagnosticsReport } from "@/features/agent/hooks/use-diagnostics-report";
import type { WorkspaceEnrollmentView } from "@/features/agent/hooks/use-workspace-enrollment";
import { requestOpenMcpPicker } from "../../_components/mcp-composer-insert";
import { requestOpenModelPicker } from "../../_components/model-picker-opener";
import { requestOpenMcpPanel } from "./mcp-panel";

/** Where the `/help` built-in goes: the shortcuts + features reference. */
const HELP_ROUTE = "/workspace/shortcuts";

import {
  SessionDetailsDialog,
  type SessionDetailsExtras,
} from "./session-details-dialog";
import { SoulDialog } from "./soul-dialog";
import { NOT_IDLE_HINT } from "./workspace-enrollment-notice";

/** Where the page-backed built-ins go (the TUI's panels are Studio pages). */
export const SKILLS_ROUTE = "/workspace/skills";
export const SCHEDULES_ROUTE = "/workspace/schedules";
export const MEMORY_SETTINGS_ROUTE = "/workspace/settings/memory";
export const LEARNING_SETTINGS_ROUTE = "/workspace/settings/learning";

/** The chat-workspace state the built-ins act on. */
export interface BuiltinSlashDeps {
  /** The selected daemon session id; null for a draft or the mock chat. */
  sessionId: string | null;
  isStreaming: boolean;
  /** True while the chat hook holds a failure — live, or a `failed` session
   *  rehydrated after a reload. The only state `/retry` acts in: outside
   *  it there is no failed step for the daemon to re-drive, and Studio
   *  never re-sends a successful turn's prompt. */
  hasFailedStep: boolean;
  /** `serverCapabilities.manual_compaction === true` (ADR 0244). */
  compactSupported: boolean;
  /** Settings → Labs "Developer tools": offers `/debug-ask`. */
  developerTools?: boolean;
  /** Parks the FAKE permission ask (use-agent-chat's injectDebugApproval);
   *  false when an ask is already pending (the TUI's dedupe). */
  onInjectDebugAsk?: () => boolean;
  onCompact: () => void;
  onRetry: () => void;
  onSend: (content: string) => void;
  onClearQueue: () => void;
  /** Runs the Clear conversation handoff (use-clear-conversation.ts): the
   *  composer blocks, the daemon cancels a running source itself and mints
   *  the empty-history successor, the UI moves there once it answered. */
  onClearConversation: () => void | Promise<void>;
  /** The session's resolved provider/model (GET-session echo), for the
   *  diagnostics report; null with no session or an older daemon. The
   *  effective reasoning-effort tier rides along when the snapshot has it. */
  resolvedModel: {
    providerId: string;
    modelId: string;
    reasoningEffort?: string;
  } | null;
  /** The composer's permission mode (Studio vocabulary). */
  permissionMode: string;
  /** Inventory-row facts the `/session` dialog shows beyond the snapshot:
   *  the `copy_id` capability and the row's last-write time. */
  sessionDetails?: SessionDetailsExtras;
  /** The daemon's compatibility capabilities (runtime-status
   *  `serverCapabilities`, SNAKE_CASE wire keys): gates the
   *  capability-gated built-ins in the palette and on send. */
  serverCapabilities?: Readonly<Record<string, unknown>>;
  /** The daemon-reported effective posture (runtime-status `posture`; ""
   *  on an older daemon, which hides `/posture`). */
  posture?: string;
  /** Opens the Rename prompt for the selected chat; absent on a draft or a
   *  row the daemon marks unrenamable (`/title` then says so). */
  onRename?: () => void | Promise<void>;
  /** The workspace-services enrollment view (`/tools-connect`,
   *  `/tools-cancel` drive its connect / retry / cancel). */
  enrollment?: WorkspaceEnrollmentView | null;
}

export const RETRY_WHILE_STREAMING =
  "/retry is unavailable while a run is active";
export const RETRY_NOTHING_FAILED =
  "Nothing to retry — the last step did not fail";
export const SESSION_NONE_YET = "No session yet — send a message to start one";
export const COMPACT_WHILE_STREAMING =
  "Wait for the run to finish before compacting";
export const COMPACT_NONE_YET = "No session yet — nothing to compact";
export const DIAGNOSTICS_WHILE_STREAMING =
  "Wait for the run to finish before sending diagnostics";
export const TITLE_NONE_YET =
  "No session yet — send a message, then rename the chat";
export const TITLE_NOT_ALLOWED = "This chat cannot be renamed";
export const MCP_PANEL_NONE_YET =
  "No session yet — the MCP panel belongs to a chat";
export const MCP_PICKER_UNAVAILABLE =
  "Insert from MCP is not available in this composer";
export const MODEL_PICKER_UNAVAILABLE =
  "The model picker is not available in this chat";
export const ENROLLMENT_CONNECTED = "Workspace services are already connected";
const ENROLLMENT_NOT_REQUIRED =
  "This deployment needs no workspace services setup";
export const ENROLLMENT_ALREADY_PENDING =
  "Setup is already in progress — /tools-cancel stops it";
export const ENROLLMENT_NONE_PENDING =
  "No workspace services setup is in progress";
export const ENROLLMENT_NONE_YET =
  "No session yet — send a message, then connect workspace services";
/** The `/posture` toast line; the posture word follows. */
export const POSTURE_PREFIX = "Daemon posture: ";

const ok: BuiltinOutcome = { ok: true };
const refuse = (warning: string): BuiltinOutcome => ({ ok: false, warning });

/**
 * The palette gates for one deps snapshot — pure, so the memo below and the
 * dispatcher's own gate check read the SAME table (composer-builtins.ts):
 * a row the palette shows is never refused as unavailable. The capability
 * document rides along verbatim (the table reads the wire keys); the
 * `/posture` gate reads `capabilities.posture`, so the narrowed `posture`
 * dep is folded in for a caller that passes only the word. A caller passing
 * neither gets no document at all — the same fail-closed reading as an
 * empty one. The developer-tools gate is carried only while ON (absent
 * reads as off), so a daemon-only gates object stays exactly
 * `{ manualCompaction }`.
 */
export function builtinGatesFor(
  deps: Pick<
    BuiltinSlashDeps,
    "compactSupported" | "developerTools" | "serverCapabilities" | "posture"
  >,
): BuiltinGates {
  return {
    manualCompaction: deps.compactSupported,
    ...(deps.developerTools === true ? { developerTools: true } : {}),
    ...(deps.serverCapabilities || deps.posture
      ? {
          capabilities: {
            ...(deps.serverCapabilities ?? {}),
            ...(deps.posture ? { posture: deps.posture } : {}),
          },
        }
      : {}),
  };
}

/** True when the daemon's capabilities hide `name` from the palette. */
function isCapabilityOff(
  name: StudioBuiltinCommand,
  deps: BuiltinSlashDeps,
): boolean {
  return isBuiltinGatedOff(name, builtinGatesFor(deps));
}

/**
 * Answers the composer's Studio built-ins (`/clear /help /session /retry
 * /diagnostics /compact`) for the chat workspace — the web analogue of the
 * TUI's builtins.go dispatch. A refusal keeps the typed text in the composer
 * with the returned warning; `ok` clears it. Every built-in runs even while
 * a run streams (it is never queued or steered); the ones that need an idle
 * session say so instead.
 *
 * `/diagnostics` is the ONE built-in that reaches the model: it sends the
 * sanitized report (diagnostics-report.ts) as an ordinary prompt.
 */
export function useBuiltinSlashCommands(deps: BuiltinSlashDeps): {
  builtinGates: BuiltinGates;
  handleSlashBuiltin: (name: StudioBuiltinCommand) => BuiltinOutcome;
  /** Render inside the workspace so `/session` has somewhere to open. */
  sessionDetailsDialog: ReactElement;
  /** Render inside the workspace so `/soul` has somewhere to open. */
  soulDialog: ReactElement;
  /** Opens the same dialog from a menu item or the ⌘I shortcut; on a draft
   *  (no daemon session yet) it says so in a toast instead. */
  openSessionDetails: () => void;
} {
  const router = useRouter();
  // The one report composer (shared with the About card's "Send to a new
  // chat"); it reads runtime status itself and is referentially stable.
  const { compose } = useDiagnosticsReport();
  // Read through a ref so the dispatcher stays stable across chat state
  // churn (the composer re-subscribes its keydown listener on every change).
  const depsRef = useRef(deps);
  depsRef.current = deps;

  const [detailsOpen, setDetailsOpen] = useState(false);
  const [detailsSessionId, setDetailsSessionId] = useState<string | null>(null);
  // `/soul`: the daemon's resolved soul, read when the dialog opens.
  const [soulOpen, setSoulOpen] = useState(false);

  // The developer-tools gate is carried only while ON (absent reads as off),
  // so a daemon-only gates object stays exactly `{ manualCompaction }`.
  // biome-ignore lint/correctness/useExhaustiveDependencies: builtinGatesFor reads exactly these four deps
  const builtinGates = useMemo<BuiltinGates>(
    () => builtinGatesFor(deps),
    [
      deps.compactSupported,
      deps.developerTools,
      deps.serverCapabilities,
      deps.posture,
    ],
  );

  const handleSlashBuiltin = useCallback(
    (name: StudioBuiltinCommand): BuiltinOutcome => {
      const d = depsRef.current;
      switch (name) {
        case "help":
          router.push(HELP_ROUTE);
          return ok;
        case "clear": {
          if (!d.sessionId) {
            // A draft has no daemon session: dropping the composer text
            // (the caller's `ok`) and the held queue IS the clear.
            d.onClearQueue();
            return ok;
          }
          // Never refused while a run streams: the handoff is the TUI's
          // cancel-and-settle — the daemon stops the run, then mints the
          // successor — and the workspace blocks input until it answers.
          void d.onClearConversation();
          return ok;
        }
        case "session":
          if (!d.sessionId) return refuse(SESSION_NONE_YET);
          setDetailsSessionId(d.sessionId);
          setDetailsOpen(true);
          return ok;
        case "retry":
          if (d.isStreaming) return refuse(RETRY_WHILE_STREAMING);
          if (!d.hasFailedStep) return refuse(RETRY_NOTHING_FAILED);
          d.onRetry();
          return ok;
        case "compact":
          if (!d.compactSupported) return refuse(gatedReason("compact"));
          if (d.isStreaming) return refuse(COMPACT_WHILE_STREAMING);
          if (!d.sessionId) return refuse(COMPACT_NONE_YET);
          d.onCompact();
          return ok;
        case "diagnostics": {
          if (d.isStreaming) return refuse(DIAGNOSTICS_WHILE_STREAMING);
          const { resolvedModel, permissionMode, onSend: send } = d;
          // The identity probe (ADR 0245) runs inside compose; an older
          // daemon or a transient fault reads as a lookup class and
          // "unavailable" rows — it never blocks the report.
          void compose({ resolvedModel, permissionMode }).then(send);
          return ok;
        }
        case "debug-ask": {
          // Developer tools: a FAKE ask, parked locally (never sent). The
          // panel it shows in belongs to a chat, so a draft is refused, and
          // one fake ask never queues behind another ask (the TUI's dedupe).
          if (!d.developerTools || !d.onInjectDebugAsk) {
            return refuse(gatedReason("debug-ask"));
          }
          if (!d.sessionId) return refuse(DEBUG_ASK_NONE_YET);
          return d.onInjectDebugAsk() ? ok : refuse(DEBUG_ASK_ALREADY_PENDING);
        }
        case "title":
          if (!d.sessionId) return refuse(TITLE_NONE_YET);
          if (!d.onRename) return refuse(TITLE_NOT_ALLOWED);
          void d.onRename();
          return ok;
        case "mcp": {
          // The panel is the chat view's: a draft has none to open, so the
          // window-event request would go unanswered.
          if (isCapabilityOff(name, d)) return refuse(gatedReason(name));
          if (!d.sessionId) return refuse(MCP_PANEL_NONE_YET);
          requestOpenMcpPanel();
          return ok;
        }
        case "prompts":
        case "resources": {
          if (isCapabilityOff(name, d)) return refuse(gatedReason(name));
          const kind = name === "prompts" ? "prompt" : "resource";
          return requestOpenMcpPicker(kind)
            ? ok
            : refuse(MCP_PICKER_UNAVAILABLE);
        }
        case "agents":
          // The composer types the `@` into the emptied field, which opens
          // the agent roster (the suggestion plugin reads the document).
          if (isCapabilityOff(name, d)) return refuse(gatedReason(name));
          return { ok: true, insertText: "@" };
        case "skills":
          if (isCapabilityOff(name, d)) return refuse(gatedReason(name));
          router.push(SKILLS_ROUTE);
          return ok;
        case "soul":
          if (isCapabilityOff(name, d)) return refuse(gatedReason(name));
          setSoulOpen(true);
          return ok;
        case "usermodel":
        case "dream":
          if (isCapabilityOff(name, d)) return refuse(gatedReason(name));
          router.push(MEMORY_SETTINGS_ROUTE);
          return ok;
        case "reflections":
        case "reflect":
          if (isCapabilityOff(name, d)) return refuse(gatedReason(name));
          router.push(LEARNING_SETTINGS_ROUTE);
          return ok;
        case "learning":
          router.push(LEARNING_SETTINGS_ROUTE);
          return ok;
        case "models":
        case "effort":
          if (isCapabilityOff(name, d)) return refuse(gatedReason(name));
          return requestOpenModelPicker()
            ? ok
            : refuse(MODEL_PICKER_UNAVAILABLE);
        case "schedule":
          if (isCapabilityOff(name, d)) return refuse(gatedReason(name));
          router.push(SCHEDULES_ROUTE);
          return ok;
        case "tools-connect": {
          if (isCapabilityOff(name, d)) return refuse(gatedReason(name));
          const e = d.enrollment;
          if (!e?.supported) return refuse(ENROLLMENT_NONE_YET);
          if (e.phase === "connected") return refuse(ENROLLMENT_CONNECTED);
          if (e.phase === "not_required")
            return refuse(ENROLLMENT_NOT_REQUIRED);
          if (e.phase === "pending") return refuse(ENROLLMENT_ALREADY_PENDING);
          if (!e.idle) return refuse(NOT_IDLE_HINT);
          // A failed enrollment retries (the notice's Retry); anything else
          // connects afresh — the same two actions the notice offers.
          if (e.phase === "failed") e.retry();
          else e.connect();
          return ok;
        }
        case "tools-cancel": {
          if (isCapabilityOff(name, d)) return refuse(gatedReason(name));
          const e = d.enrollment;
          if (!e?.supported || e.phase !== "pending") {
            return refuse(ENROLLMENT_NONE_PENDING);
          }
          if (!e.idle) return refuse(NOT_IDLE_HINT);
          e.cancel();
          return ok;
        }
        case "posture":
          if (!d.posture) return refuse(gatedReason(name));
          toast.info(`${POSTURE_PREFIX}${d.posture}`);
          return ok;
        default:
          return ok;
      }
    },
    [router, compose],
  );

  // The menu item and ⌘I share `/session`'s dialog; unlike the built-in they
  // have no composer to leave a warning in, so a draft gets a toast.
  const openSessionDetails = useCallback(() => {
    const d = depsRef.current;
    if (!d.sessionId) {
      toast.info(SESSION_NONE_YET);
      return;
    }
    setDetailsSessionId(d.sessionId);
    setDetailsOpen(true);
  }, []);

  const sessionDetailsDialog = (
    <SessionDetailsDialog
      sessionId={detailsSessionId}
      open={detailsOpen}
      onOpenChange={setDetailsOpen}
      extras={deps.sessionDetails}
    />
  );

  const soulDialog = <SoulDialog open={soulOpen} onOpenChange={setSoulOpen} />;

  return {
    builtinGates,
    handleSlashBuiltin,
    sessionDetailsDialog,
    soulDialog,
    openSessionDetails,
  };
}
