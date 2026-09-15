/**
 * Daemon inventory and identity over the mecatl SDK: the liveness probe, the
 * compatibility document, and the read-only lists (models, agents, slash
 * commands, skills, the user-model memory index).
 */

import type { ServerCapabilities } from "@stacklok-oss/mecatl-sdk";

import {
  getHarnessClient,
  harness,
  isUnsupportedByDaemon,
  toHarnessError,
} from "./sdk";

export interface HarnessStatus {
  live: boolean;
  detail: string;
}

/**
 * Cheap liveness probe. A deployed instance has no daemon on loopback, so this
 * failing is the expected path there — callers fall back to mock behaviour.
 * Never throws: every failure is folded into `{live:false, detail}`.
 */
export async function probeHarness(
  signal?: AbortSignal,
): Promise<HarnessStatus> {
  try {
    await getHarnessClient().models.list(
      { $typeName: "mecatl.v1.ListModelsRequest" },
      { signal },
    );
    return { live: true, detail: "connected" };
  } catch (error) {
    const translated = toHarnessError(error);
    return {
      live: false,
      detail:
        translated instanceof Error ? translated.message : String(translated),
    };
  }
}

/**
 * The daemon's compatibility document (GET /v1/compatibility, ADR 0248):
 * the API major, the operator-enabled server capabilities (no probe session
 * needed), the open feature registry, and the operator's deployment label.
 * Returns null against an older daemon without the endpoint.
 *
 * `capabilities` keeps the daemon's SNAKE_CASE wire keys (`storage_health`,
 * `manual_dream.project_memory.generate`, …) — the vocabulary every UI gate
 * reads — projected from the SDK's camelCase `ServerCapabilities`.
 */
export interface HarnessCompatibility {
  apiMajor: number;
  features: string[];
  capabilities: Record<string, unknown>;
  /** The operator's deployment label from `ServerCompatibility.deployment`;
   *  "" when the daemon sets none. */
  deployment: string;
}

/** Projects the SDK's camelCase capabilities onto the daemon's wire keys. */
export function toWireCapabilities(
  capabilities: ServerCapabilities | undefined,
): Record<string, unknown> {
  if (!capabilities) return {};
  const { manualDream, ...flags } = capabilities;
  const wire: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(flags)) {
    wire[snakeCase(key)] = value;
  }
  if (manualDream) {
    const targets: Record<string, unknown> = {};
    for (const [target, entry] of Object.entries(manualDream)) {
      if (!entry) continue;
      targets[snakeCase(target)] = {
        generate: entry.generate,
        decide: entry.decide,
        unavailable_reason: entry.unavailableReason ?? "",
      };
    }
    wire.manual_dream = targets;
  }
  return wire;
}

function snakeCase(key: string): string {
  return key.replace(/[A-Z]/g, (letter) => `_${letter.toLowerCase()}`);
}

export async function fetchHarnessCompatibility(
  signal?: AbortSignal,
): Promise<HarnessCompatibility | null> {
  try {
    const doc = await harness(() =>
      getHarnessClient().server.compatibility({ signal }),
    );
    return {
      apiMajor: doc.apiMajor,
      features: [...doc.features],
      capabilities: toWireCapabilities(doc.capabilities),
      deployment: doc.deployment ?? "",
    };
  } catch (error) {
    if (isUnsupportedByDaemon(error)) return null; // pre-ADR-0248 daemon
    throw error;
  }
}

// ── Memory (user model) ─────────────────────────────────────────────────────

/**
 * Reads the harness user model: durable facts the agent has stored about the
 * operator, cross-project. The API returns the INDEX only — key plus one-line
 * description — and never entry values, which the agent loads with RecallUser.
 *
 * There is no write endpoint: the agent curates memory through injection-scanned
 * tool calls, so this is read-only by construction, not by choice.
 */
export interface HarnessUserModel {
  entries: { key: string; description: string }[];
  /** Aggregate byte length of the rendered entries. */
  sizeBytes: number;
  /** The proto index carries no digest; always "" (nothing renders it). */
  sha256: string;
}

export async function fetchHarnessUserModel(
  signal?: AbortSignal,
): Promise<HarnessUserModel> {
  const response = await harness(() =>
    getHarnessClient().userModel.get(
      { $typeName: "mecatl.v1.GetUserModelRequest", key: "" },
      { signal },
    ),
  );
  return {
    entries: response.entries.map((entry) => ({
      key: entry.key,
      description: entry.description,
    })),
    sizeBytes: Number(response.sizeBytes),
    sha256: "",
  };
}

// ── Slash commands ───────────────────────────────────────────────────────────

/**
 * Reads the slash commands available to one session (GET /v1/commands
 * REQUIRES `session_id`: the daemon resolves the session's own placement, so
 * the browser never names a workspace path).
 */
export async function listHarnessCommands(
  sessionId: string,
  signal?: AbortSignal,
): Promise<{ name: string; description: string }[]> {
  const response = await harness(() =>
    getHarnessClient().commands.list(
      { $typeName: "mecatl.v1.ListCommandsRequest", sessionId },
      { signal },
    ),
  );
  return response.commands.map((command) => ({
    name: command.name,
    description: command.description,
  }));
}

// ── Skills ──────────────────────────────────────────────────────────────────

/**
 * Reads the skill inventory the daemon resolved at startup from its --skills-dir.
 * The model sees only each skill's name and one-line summary until it chooses to
 * load one, which is exactly what this returns.
 */
export interface HarnessSkillInfo {
  name: string;
  description: string;
  /** Learned-lifecycle provenance; absent for immutable external skills. */
  agentOwned: boolean;
  ownerAgent: string;
  activeVersion: string;
}

export async function listHarnessSkills(
  signal?: AbortSignal,
): Promise<HarnessSkillInfo[]> {
  const response = await harness(() =>
    getHarnessClient().skills.list(
      { $typeName: "mecatl.v1.ListSkillsRequest" },
      { signal },
    ),
  );
  return response.skills.map((skill) => ({
    name: skill.name,
    description: skill.description,
    agentOwned: skill.agentOwned,
    ownerAgent: skill.ownerAgent,
    activeVersion: skill.activeVersion,
  }));
}

/** Reads the selectable provider/model inventory. Carries no secret material. */
export async function listHarnessModels(signal?: AbortSignal): Promise<
  {
    id: string;
    providerId: string;
    displayName: string;
    contextLimit: number;
    image: boolean;
    reasoning: boolean;
  }[]
> {
  const response = await harness(() =>
    getHarnessClient().models.list(
      { $typeName: "mecatl.v1.ListModelsRequest" },
      { signal },
    ),
  );
  return response.models.map((model) => ({
    id: model.id,
    providerId: model.providerId,
    displayName: model.displayName || model.id,
    contextLimit: Number(model.contextLimit),
    image: model.image,
    reasoning: model.reasoning,
  }));
}

/** One row of the daemon's resolved agent inventory (proto AgentInfo). */
export interface HarnessAgentInfo {
  name: string;
  description: string;
  /** Resolved pinned model id; empty = inherit ("auto": session model or routed). */
  model: string;
  /** The def's effective read-only tool scope at the delegation call site. */
  tools: string[];
  /** Raw frontmatter permission mode; empty means "default". */
  permissionMode: string;
  /** Optional UX color hint from the def; never affects execution. */
  color: string;
}

/** Reads the daemon's RESOLVED agent inventory (what it can delegate to now). */
export async function listHarnessAgents(
  signal?: AbortSignal,
): Promise<HarnessAgentInfo[]> {
  const response = await harness(() =>
    getHarnessClient().agents.list(
      { $typeName: "mecatl.v1.ListAgentsRequest" },
      { signal },
    ),
  );
  return response.agents.map((agent) => ({
    name: agent.name,
    description: agent.description,
    model: agent.model,
    tools: [...agent.tools],
    permissionMode: agent.permissionMode,
    color: agent.color,
  }));
}
