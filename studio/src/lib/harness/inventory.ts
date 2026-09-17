/**
 * Daemon inventory and identity over the mecatl SDK: the liveness probe, the
 * compatibility document, and the read-only lists (models, agents, slash
 * commands, skills, the user-model memory index).
 */

import type { ServerCapabilities } from "@stacklok-oss/mecatl-sdk";

import { HarnessApiError } from "./errors";
import {
  getHarnessClient,
  harness,
  isUnsupportedByDaemon,
  toHarnessError,
} from "./sdk";
import { timestampUnix } from "./time";

export interface HarnessStatus {
  live: boolean;
  detail: string;
  /** The HTTP status behind a failed probe (0 = no response at all, e.g. a
   *  refused connection); 200 while live. */
  status: number;
  /** The stable machine code behind a failed probe: the proxy's
   *  `oidc_login_required` / `oidc_session_expired` / `oidc_idp_unavailable`,
   *  the daemon's `unauthenticated`, "" when the body carried none. */
  code: string;
}

/**
 * Cheap liveness probe. A deployed instance has no daemon on loopback, so this
 * failing is the expected path there — callers fall back to mock behaviour.
 * Never throws: every failure is folded into `{live:false, detail, status,
 * code}` — the typed half lets the offline banner tell "the daemon is down"
 * from "Studio's credential was refused" (`offline-cause.ts`).
 */
export async function probeHarness(
  signal?: AbortSignal,
): Promise<HarnessStatus> {
  try {
    await getHarnessClient().models.list(
      { $typeName: "mecatl.v1.ListModelsRequest" },
      { signal },
    );
    return { live: true, detail: "connected", status: 200, code: "" };
  } catch (error) {
    const translated = toHarnessError(error);
    const typed =
      translated instanceof HarnessApiError
        ? { status: translated.status, code: translated.code }
        : { status: 0, code: "" };
    return {
      live: false,
      detail:
        translated instanceof Error ? translated.message : String(translated),
      ...typed,
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
 * operator, cross-project. The key-less read returns the INDEX — key plus
 * one-line description — never entry values; one fact's value and revision
 * history come from `fetchHarnessUserModelEntry` (the TUI's lazy `enter`).
 *
 * There is no write endpoint: the agent curates memory through injection-scanned
 * tool calls, so this is read-only by construction, not by choice.
 */
export interface HarnessUserModel {
  entries: { key: string; description: string }[];
  /** Aggregate byte length of the rendered entries. */
  sizeBytes: number;
  /** Lowercase-hex SHA-256 over the rendered entries — tells at a glance
   *  whether the store changed between reads; "" when the daemon sent none. */
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
    sha256: response.sha256 ?? "",
  };
}

/** One read-only lifecycle revision of a fact (proto `UserModelRevision`). */
export interface HarnessUserModelRevision {
  key: string;
  value: string;
  description: string;
  version: string;
  status: string;
  writer: string;
  origin: string;
  sourceSessionId: string;
  sourceProposalId: string;
  /** Unix seconds; 0 when the store recorded no timestamp. */
  updatedAtUnix: number;
}

/**
 * One fact's exact detail (proto `UserModelDetail`): the current revision
 * plus the bounded prior revisions, in the order the daemon delivers them
 * (newest first). `historyAvailable` false means the store/driver supplied
 * only the current legacy value — say so, never render it as "no history".
 */
export interface HarnessUserModelDetail {
  current: HarnessUserModelRevision;
  history: HarnessUserModelRevision[];
  historyAvailable: boolean;
}

/** The wire revision, typed structurally (the SDK does not export the type). */
interface WireUserModelRevision {
  key: string;
  value: string;
  description: string;
  version: string;
  status: string;
  writer: string;
  origin: string;
  sourceSessionId: string;
  sourceProposalId: string;
  updatedAt?: { seconds: bigint | number } | undefined;
}

function toUserModelRevision(
  revision: WireUserModelRevision,
): HarnessUserModelRevision {
  return {
    key: revision.key,
    value: revision.value,
    description: revision.description,
    version: revision.version,
    status: revision.status,
    writer: revision.writer,
    origin: revision.origin,
    sourceSessionId: revision.sourceSessionId,
    sourceProposalId: revision.sourceProposalId,
    updatedAtUnix: timestampUnix(revision.updatedAt),
  };
}

/**
 * Reads ONE fact's value and revision history (`GET /v1/usermodel?key=`), the
 * web analogue of the TUI viewer's `enter`. Resolves null when the daemon
 * answers without `detail`: the key no longer exactly matches an entry, so an
 * index row the caller still holds is stale. A `--no-user-model` daemon
 * throws the same typed error as the index read.
 */
export async function fetchHarnessUserModelEntry(
  key: string,
  signal?: AbortSignal,
): Promise<HarnessUserModelDetail | null> {
  const response = await harness(() =>
    getHarnessClient().userModel.get(
      { $typeName: "mecatl.v1.GetUserModelRequest", key },
      { signal },
    ),
  );
  const detail = response.detail;
  if (!detail?.current) return null;
  return {
    current: toUserModelRevision(detail.current),
    history: (detail.history ?? []).map(toUserModelRevision),
    historyAvailable: detail.historyAvailable === true,
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

/** One selectable model from the daemon's inventory. */
export interface HarnessModelInfo {
  id: string;
  providerId: string;
  displayName: string;
  contextLimit: number;
  image: boolean;
  reasoning: boolean;
}

/**
 * One provider's last live-listing outcome (proto `ProviderStatus`, issue
 * #262): an operator-actionable hint for intent-driven gateways and
 * openai-codex, present even when the provider contributes zero models.
 * `state` is the daemon's string passthrough — "ok" | "unreachable" |
 * "unauthorized" | "empty" today, rendered verbatim if it grows.
 */
export interface HarnessProviderStatus {
  providerId: string;
  state: string;
  /** Short human remediation, e.g. "start it with `thv llm proxy start`". */
  hint: string;
  /** The server AUTO-selected this default provider's default model. */
  defaultModelAutoSelected: boolean;
  /** Models the last successful live listing returned (0 = none/unrecorded). */
  modelCount: number;
  /** Registered and reachable, but out-ranked by a key-driven default. */
  availableNotDefault: boolean;
}

/**
 * Reads the selectable provider/model inventory PLUS the daemon's
 * per-provider status hints (`ListModelsResponse.provider_status`). Carries
 * no secret material: an unavailable provider is omitted from `models`
 * entirely, and a status row names only the provider and its outcome.
 */
export async function listHarnessModelInventory(signal?: AbortSignal): Promise<{
  models: HarnessModelInfo[];
  providerStatus: HarnessProviderStatus[];
}> {
  const response = await harness(() =>
    getHarnessClient().models.list(
      { $typeName: "mecatl.v1.ListModelsRequest" },
      { signal },
    ),
  );
  return {
    models: response.models.map((model) => ({
      id: model.id,
      providerId: model.providerId,
      displayName: model.displayName || model.id,
      contextLimit: Number(model.contextLimit),
      image: model.image,
      reasoning: model.reasoning,
    })),
    providerStatus: (response.providerStatus ?? []).map((row) => ({
      providerId: row.providerId,
      state: row.state,
      hint: row.hint,
      defaultModelAutoSelected: row.defaultModelAutoSelected === true,
      modelCount: Number(row.modelCount ?? 0),
      availableNotDefault: row.availableNotDefault === true,
    })),
  };
}

/** Reads the selectable provider/model inventory. Carries no secret material. */
export async function listHarnessModels(
  signal?: AbortSignal,
): Promise<HarnessModelInfo[]> {
  return (await listHarnessModelInventory(signal)).models;
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
