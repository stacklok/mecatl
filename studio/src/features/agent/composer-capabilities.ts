import { listHarnessAgents, listHarnessCommands } from "@/lib/harness/client";
import { type BuiltinGates, builtinSlashCommands } from "./composer-builtins";

/**
 * What the chat composer can pull into a message: the `@agent` mentions and
 * the `/slash` commands its autocomplete offers.
 *
 * Both come from the daemon — the resolved subagent inventory
 * (`GET /v1/agents`, refreshed by the runtime-status provider on every
 * (re)connect, because a daemon restart can change the roster) and the
 * session's slash commands (`GET /v1/commands?session_id=…`, refreshed once a
 * chat's daemon session id is known).
 *
 * A module-level registry rather than React state: tiptap's suggestion
 * plugins read these lists per keystroke from plain callbacks that live
 * outside the component tree.
 */

export interface AgentMention {
  /** Handle inserted after `@` (the agent slug). */
  readonly handle: string;
  readonly name: string;
  readonly description: string;
}

export interface SlashCommand {
  /** Name inserted after `/`. */
  readonly name: string;
  readonly description: string;
}

/**
 * Studio-local `/commands` — the analogue of the TUI's always-present
 * built-ins (cmd/mecatui/ui/builtins.go), defined in composer-builtins.ts.
 * They are prepended to the composer's `/` menu ahead of the daemon's list
 * and intercepted on send, so `/clear` clears the chat instead of reaching
 * the model. A daemon command of the same name is shadowed (dropped from the
 * menu) so what the menu shows is what fires. The daemon list itself is
 * untouched. Re-exported here so the palette reads both layers from one
 * module.
 */
export {
  type BuiltinGates,
  builtinSlashCommands,
  isStudioBuiltinCommand,
  STUDIO_BUILTIN_COMMANDS,
  type StudioBuiltinCommand,
} from "./composer-builtins";

/** Turns an agent name into an `@`-mention handle, e.g. "Code Reviewer" → "code-reviewer". */
function toHandle(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

let agentMentions: readonly AgentMention[] = [];
let slashCommands: readonly SlashCommand[] = [];

export function getAgentMentions(): readonly AgentMention[] {
  return agentMentions;
}

export function getSlashCommands(): readonly SlashCommand[] {
  return slashCommands;
}

/**
 * Both palette layers in display order: Studio's built-ins first (gated on
 * the daemon's capabilities), then the daemon's per-session commands, minus
 * any the built-ins shadow — so what the menu shows is what fires.
 */
export function getAllSlashCommands(
  gates: BuiltinGates,
): readonly (SlashCommand & { readonly builtin?: true })[] {
  const builtins = builtinSlashCommands(gates);
  const shadowed = new Set<string>(builtins.map((command) => command.name));
  return [
    ...builtins,
    ...slashCommands.filter((command) => !shadowed.has(command.name)),
  ];
}

/**
 * Re-reads the capability lists from the daemon. Agents always; slash
 * commands only when a daemon session id is given, because `GET /v1/commands`
 * is keyed by `session_id` (the daemon resolves the session's placement — the
 * browser never names a workspace). A list failing leaves the previous value
 * in place — an autocomplete that briefly lags a restart beats one that
 * flickers empty on every transient error.
 */
export async function refreshComposerCapabilities(
  sessionId?: string,
): Promise<void> {
  await Promise.allSettled([
    refreshAgentMentions(),
    ...(sessionId ? [refreshSlashCommands(sessionId)] : []),
  ]);
}

/** Re-reads the `@agent` mention roster (`GET /v1/agents`). */
export async function refreshAgentMentions(): Promise<void> {
  try {
    const agents = await listHarnessAgents();
    agentMentions = agents.map((agent) => ({
      handle: toHandle(agent.name),
      name: agent.name,
      description: agent.description,
    }));
  } catch {
    // keep the previous roster
  }
}

/**
 * Re-reads the `/slash` command list for one daemon session
 * (`GET /v1/commands?session_id=…`). Call it once the chat's daemon session
 * id is known (and again after a reconnect), since commands are per-session.
 */
export async function refreshSlashCommands(sessionId: string): Promise<void> {
  try {
    const commands = await listHarnessCommands(sessionId);
    slashCommands = commands
      .filter((command) => command.name)
      .map((command) => ({
        name: command.name,
        description: command.description,
      }));
  } catch {
    // keep the previous list
  }
}
