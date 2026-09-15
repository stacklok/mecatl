import { listHarnessAgents, listHarnessCommands } from "@/lib/harness/client";

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
