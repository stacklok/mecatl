/**
 * Browser-side client facade for Studio's two backends.
 *
 * DAEMON capabilities (sessions, runs, inventory, schedules, …) live in the
 * sibling modules and go through the mecatl TypeScript SDK (see ./sdk.ts);
 * this file re-exports them so importers keep one entry point.
 *
 * CONTROLLER capabilities — Studio's own local supervisor process behind the
 * /api/mecatl-control proxy (provider keys, MCP gateway, skills
 * on disk, daemon restart) — are NOT engine capabilities and stay here as
 * plain fetch calls; the SDK has no notion of the controller.
 */

import { validSkillName } from "@/lib/controller-security.mjs";
import {
  type HarnessDaemonDefaults,
  readDaemonDefaults,
} from "./daemon-defaults";
import { apiError } from "./errors";
import { type HarnessRetentionState, readRetentionState } from "./retention";
import { type HarnessStorageState, readStorageState } from "./store-location";
import { type HarnessTrustState, readTrustState } from "./trust";

export * from "./daemon-defaults";
export * from "./diagnostics";
export { HarnessApiError } from "./errors";
export * from "./inventory";
export * from "./mcp-authorization";
export * from "./retention";
export * from "./schedules";
export * from "./sessions";
export * from "./store-location";
export * from "./trust";
export * from "./worktrees";

// ── Controller: provider, MCP gateway ───────────────────────────────────────

const CONTROL_API = "/api/mecatl-control";

export interface HarnessControlStatus {
  /** "external" when Studio proxies to MECATL_BASE_URL; "managed" otherwise. */
  mode: "managed" | "external";
  provider: string;
  /** True when `provider` is the offline mock — the daemon serves canned
   *  turns rather than calling a real model. */
  isMock: boolean;
  running: boolean;
  /** Why the managed daemon's last start failed ("" when it started, or in
   *  external mode). The Diagnostics page's daemon-log card shows it above
   *  the log tail. Absent on an older controller. */
  startupError?: string;
  gateway: { name: string; url: string } | null;
  toolhiveGateway: {
    available: boolean;
    active: boolean;
    /** The loopback proxy URL the controller probes (display only). */
    baseURL?: string;
    /** ToolHive's `thv` CLI was found on the controller's PATH at boot, so
     *  Studio can offer to start the proxy. Absent on an older controller. */
    thvOnPath?: boolean;
  } | null;
  modelRouter: { enabled: boolean; categories: number } | null;
  operatorSettings: boolean;
  skillsDir: string;
  memoryDir: string;
  /**
   * Provider NAMES found in the operator's auth.yaml — never credentials.
   * Managed mode can now add/remove blocks THROUGH the controller (which
   * owns the file server-side); no key value ever crosses this boundary.
   */
  configuredProviders: string[];
  /** Which provider is active right now — "mock", "toolhive", or one of
   *  `configuredProviders` — seeded from MECATL_STUDIO_PROVIDER at startup
   *  but changeable at runtime via setActiveHarnessProvider. */
  selectedProvider: string | null;
  /** The auth.yaml path on the controller's machine (guided-add copy). */
  authFile: string;
  /** The controller's workspace label — display only (Studio rule 2). */
  workspace: string;
  /** The root the managed controller falls back to (the monorepo), so the
   *  Workspace card can say when the live root differs and offer "Reset to
   *  default". Absent in external mode and against an older controller.
   *  Display only, like `workspace`. */
  defaultWorkspace?: string;
  /**
   * The SAVED permissions the managed daemon was spawned with (operator
   * posture, project trust, shell-less mode — CLI flags, never a
   * settings.yaml key). Null in external mode, where the deployment owns
   * them. The EFFECTIVE posture is `serverCapabilities.posture`.
   */
  permissions: HarnessPermissionsState | null;
  /**
   * The controller's OWN project-trust decision for the current spawn
   * (`./trust`): whether the workspace carries admittable authority, what
   * the spawn got (trusted/once/drifted/untrusted), who granted it and the
   * live identity anchor. Null in external mode and against an older
   * controller (optional so status literals elsewhere keep compiling —
   * readers treat absence as null). It cannot see the daemon's own
   * trust.yaml grants.
   */
  trust?: HarnessTrustState | null;
  /**
   * The SESSION STORE the managed daemon was spawned with (durable
   * directory or in-memory — a spawn flag the controller owns). Null in
   * external mode and against an older controller that does not report it.
   */
  storage: HarnessStorageState | null;
  /**
   * The RETENTION document the managed daemon was spawned with (family
   * age/count limits, sweep cadence, main-deletion acknowledgement — spawn
   * flags the controller owns) plus who manages it. Null in external mode
   * and against an older controller that does not report it.
   */
  retention: HarnessRetentionState | null;
  /** Where a custom `providers:` block / a `provider_overrides:` snippet
   *  lands on the controller's machine (the imported operator settings when
   *  active, else the user-global settings.yaml). Absent on an older
   *  controller. Display only. */
  settingsFile?: string;
  /**
   * The saved DAEMON DEFAULTS the managed daemon was spawned with (spawn
   * flags — default/subagent model per provider, effort, context window,
   * prompt caching, base URLs, ToolHive, aliases/slots, the credentials
   * PATH). Null in external mode and against an older controller.
   */
  daemonDefaults?: HarnessDaemonDefaults | null;
}

/** The controller's saved permissions document (POST /permissions body). */
export interface HarnessPermissionsConfig {
  /** One of the daemon's posture ladder tiers: strict/trusted/auto/yolo. */
  posture: string;
  /** `--trust-project`: honour the project's ALLOW rules, soul, agents,
   *  commands and skills. */
  trustProject: boolean;
  /** `--no-shell`: drop the Shell tool from the daemon's catalog. */
  noShell: boolean;
}

export interface HarnessPermissionsState extends HarnessPermissionsConfig {
  /** A this-process-only trust grant the controller passes on top of the
   *  saved switch (never persisted). */
  trustOnce: boolean;
}

function readPermissions(raw: unknown): HarnessPermissionsState | null {
  if (!raw || typeof raw !== "object") return null;
  const body = raw as {
    posture?: unknown;
    trustProject?: unknown;
    noShell?: unknown;
    trustOnce?: unknown;
  };
  return {
    posture: typeof body.posture === "string" ? body.posture : "strict",
    trustProject: body.trustProject === true,
    noShell: body.noShell === true,
    trustOnce: body.trustOnce === true,
  };
}

export async function fetchHarnessControlStatus(
  signal?: AbortSignal,
): Promise<HarnessControlStatus | null> {
  try {
    const response = await fetch(`${CONTROL_API}/status`, {
      signal,
      cache: "no-store",
    });
    if (!response.ok) return null;
    const body = (await response.json()) as {
      mode?: string;
      provider?: string;
      isMock?: boolean;
      running?: boolean;
      startupError?: unknown;
      gateway?: { name?: string; url?: string } | null;
      toolhiveGateway?: {
        available?: boolean;
        active?: boolean;
        baseURL?: string;
        thvOnPath?: boolean;
      } | null;
      modelRouter?: { enabled?: boolean; categories?: number } | null;
      operatorSettings?: boolean;
      skills?: { dir?: string };
      memory?: { dir?: string };
      configuredProviders?: unknown;
      selectedProvider?: string | null;
      authFile?: string;
      workspace?: string;
      defaultWorkspace?: string;
      permissions?: unknown;
      trust?: unknown;
      storage?: unknown;
      retention?: unknown;
      settingsFile?: string;
      daemonDefaults?: unknown;
    };
    return {
      mode: body.mode === "external" ? "external" : "managed",
      provider: body.provider ?? "unknown",
      isMock: Boolean(body.isMock),
      running: Boolean(body.running),
      startupError:
        typeof body.startupError === "string" ? body.startupError : undefined,
      gateway: body.gateway?.url
        ? { name: body.gateway.name ?? "gateway", url: body.gateway.url }
        : null,
      toolhiveGateway: body.toolhiveGateway
        ? {
            available: Boolean(body.toolhiveGateway.available),
            active: Boolean(body.toolhiveGateway.active),
            baseURL:
              typeof body.toolhiveGateway.baseURL === "string"
                ? body.toolhiveGateway.baseURL
                : undefined,
            thvOnPath:
              typeof body.toolhiveGateway.thvOnPath === "boolean"
                ? body.toolhiveGateway.thvOnPath
                : undefined,
          }
        : null,
      modelRouter: body.modelRouter
        ? {
            enabled: Boolean(body.modelRouter.enabled),
            categories: Number(body.modelRouter.categories ?? 0),
          }
        : null,
      operatorSettings: Boolean(body.operatorSettings),
      configuredProviders: Array.isArray(body.configuredProviders)
        ? body.configuredProviders.filter(
            (name): name is string => typeof name === "string",
          )
        : [],
      selectedProvider: body.selectedProvider ?? null,
      authFile: body.authFile ?? "",
      skillsDir: body.skills?.dir ?? "",
      memoryDir: body.memory?.dir ?? "",
      workspace: typeof body.workspace === "string" ? body.workspace : "",
      defaultWorkspace:
        typeof body.defaultWorkspace === "string"
          ? body.defaultWorkspace
          : undefined,
      permissions: readPermissions(body.permissions),
      trust: readTrustState(body.trust),
      storage: readStorageState(body.storage),
      retention: readRetentionState(body.retention),
      settingsFile:
        typeof body.settingsFile === "string" ? body.settingsFile : undefined,
      daemonDefaults: readDaemonDefaults(body.daemonDefaults),
    };
  } catch {
    return null;
  }
}

/**
 * The controller's saved permissions plus whether an imported operator
 * settings file is active (its `posture:` key is OUT-RANKED by Studio's
 * explicit flag, but the user should know it exists). Null when the
 * controller cannot answer (external mode's 409, offline).
 */
export async function fetchHarnessPermissions(signal?: AbortSignal): Promise<{
  config: HarnessPermissionsState;
  operatorSettings: boolean;
} | null> {
  const response = await fetch(`${CONTROL_API}/permissions`, {
    signal,
    cache: "no-store",
  });
  if (!response.ok) return null;
  const body = (await response.json()) as {
    config?: unknown;
    trustOnce?: unknown;
    operatorSettings?: unknown;
  };
  const config = readPermissions(body.config);
  if (!config) return null;
  return {
    config: { ...config, trustOnce: body.trustOnce === true },
    operatorSettings: body.operatorSettings === true,
  };
}

/** Saves the permissions document. RESTARTS the daemon; a start mecated
 *  refuses (auto/yolo as root outside a sandbox) is rolled back by the
 *  controller and surfaces here as the thrown error. */
export async function saveHarnessPermissions(
  config: HarnessPermissionsConfig,
): Promise<void> {
  const response = await fetch(`${CONTROL_API}/permissions`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      posture: config.posture,
      trustProject: config.trustProject,
      noShell: config.noShell,
    }),
  });
  if (!response.ok) throw await apiError(response);
}

/**
 * Connects an MCP gateway. RESTARTS the daemon, and a failed handshake rolls the
 * previous gateway back on the controller side.
 *
 * The token is passed straight through to the loopback controller and is never
 * stored, logged, or echoed by this UI.
 */
export async function connectHarnessGateway(
  name: string,
  url: string,
  token?: string,
): Promise<void> {
  const response = await fetch(`${CONTROL_API}/mcp`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      name,
      url,
      token: token?.trim() || undefined,
    }),
  });
  if (!response.ok) throw await apiError(response);
}

// ── Controller: provider management ─────────────────────────────────────────
// auth.yaml stays server-side property of the controller: these calls move
// NAMES and booleans, never key material. There is deliberately no
// "add provider with key" call — adding one is a guided copy-into-auth.yaml
// (see the Add-provider dialog), so a credential never transits the browser.

/** One provider block found in the controller's auth.yaml — names and
 *  booleans only, never values (Studio rule 3). */
export interface HarnessProviderInfo {
  name: string;
  /** False for a built-in kind the inventory lists only so it can be
   *  described and added (no auth.yaml block, no env var). */
  configured: boolean;
  /** A non-empty api_key / oauth access_token exists in the block. */
  keyPresent: boolean;
  source: string;
  /** The controller can key-test this kind with one cheap keyed call. */
  testable: boolean;
  // ── `providers status` parity (absent on an older controller) ──────────
  /** "built-in" | "custom" (settings-defined, ADR 0238) | "external" (the
   *  ToolHive gateway) | "unknown" (an auth.yaml block naming neither). */
  class?: string;
  /** "api_key" | "oauth" (openai-codex's manual token) | "none" | "external". */
  authMethod?: string;
  /** The kind's credential env var is set in the controller's environment,
   *  which the spawned mecated inherits — the variable wins over auth.yaml. */
  envShadowed?: boolean;
  /** Plain authentication state: "configured", "not configured",
   *  "configured (environment shadows auth.yaml)", "not required",
   *  "manual token", "gateway reachable", … */
  authState?: string;
  /** The saved daemon default model for this provider (or a custom
   *  definition's default_model); "" when none. */
  defaultModel?: string;
  /** The next recovery step, e.g. "add an API key to auth.yaml". */
  nextStep?: string;
  /** This is the kind the daemon was spawned on. */
  active?: boolean;
  /** ToolHive row only: the loopback proxy URL (display only). */
  baseURL?: string;
  /** ToolHive row only: the readiness probe answered. */
  reachable?: boolean;
  /** ToolHive row only: `thv` is on the controller's PATH, so Studio can
   *  start the proxy. */
  thvOnPath?: boolean;
}

export async function listHarnessProviders(
  signal?: AbortSignal,
): Promise<HarnessProviderInfo[]> {
  const response = await fetch(`${CONTROL_API}/providers`, {
    signal,
    cache: "no-store",
  });
  if (!response.ok) throw await apiError(response);
  const body = (await response.json()) as {
    providers?: {
      name?: string;
      configured?: boolean;
      keyPresent?: boolean;
      source?: string;
      testable?: boolean;
      class?: string;
      authMethod?: string;
      envShadowed?: boolean;
      authState?: string;
      defaultModel?: string;
      nextStep?: string;
      active?: boolean;
      baseURL?: string;
      reachable?: boolean;
      thvOnPath?: boolean;
    }[];
  };
  const text = (value: unknown) => (typeof value === "string" ? value : "");
  return (body.providers ?? [])
    .filter((row) => typeof row.name === "string" && row.name !== "")
    .map((row) => ({
      name: row.name ?? "",
      configured: row.configured !== false,
      keyPresent: row.keyPresent === true,
      source: row.source ?? "auth.yaml",
      testable: row.testable === true,
      class: text(row.class),
      authMethod: text(row.authMethod),
      envShadowed: row.envShadowed === true,
      authState: text(row.authState),
      defaultModel: text(row.defaultModel),
      nextStep: text(row.nextStep),
      active: row.active === true,
      baseURL: text(row.baseURL),
      reachable: row.reachable === true,
      thvOnPath: row.thvOnPath === true,
    }));
}

/** One provider kind the daemon understands, with the guided-add snippet
 *  (a `<YOUR_KEY>` placeholder — never a real value). */
export interface KnownHarnessProvider {
  name: string;
  label: string;
  testable: boolean;
  snippet: string;
  note: string;
}

export async function listKnownHarnessProviders(
  signal?: AbortSignal,
): Promise<KnownHarnessProvider[]> {
  const response = await fetch(`${CONTROL_API}/providers/known`, {
    signal,
    cache: "no-store",
  });
  if (!response.ok) throw await apiError(response);
  const body = (await response.json()) as {
    known?: {
      name?: string;
      label?: string;
      testable?: boolean;
      snippet?: string;
      note?: string;
    }[];
  };
  return (body.known ?? [])
    .filter((row) => typeof row.name === "string" && row.name !== "")
    .map((row) => ({
      name: row.name ?? "",
      label: row.label ?? row.name ?? "",
      testable: row.testable === true,
      snippet: row.snippet ?? "",
      note: row.note ?? "",
    }));
}

/** The controller's verdict on a stored key after ONE bounded probe. */
export interface HarnessProviderKeyTest {
  ok: boolean;
  /** HTTP status from the provider (0 = unreachable/timeout). */
  status: number;
  /** True when the provider answered 401/403 — the KEY is bad, not the wire. */
  rejected: boolean;
  error: string;
}

/**
 * Asks the controller to test a provider's STORED key with one cheap
 * authenticated call. The key itself never reaches the browser — only the
 * verdict does. Throws when the test could not run at all (unknown provider,
 * no key in auth.yaml, external mode's 409).
 */
export async function testHarnessProviderKey(
  name: string,
): Promise<HarnessProviderKeyTest> {
  const response = await fetch(
    `${CONTROL_API}/providers/${encodeURIComponent(name)}/test`,
    { method: "POST" },
  );
  if (!response.ok) throw await apiError(response);
  const body = (await response.json()) as {
    ok?: boolean;
    status?: number;
    rejected?: boolean;
    error?: string;
  };
  return {
    ok: body.ok === true,
    status: Number(body.status ?? 0),
    rejected: body.rejected === true,
    error: body.error ?? "",
  };
}

/** What a provider removal cuts: `"all"` (the TUI's `providers remove`) is
 *  the auth.yaml block AND a custom provider's settings.yaml definition;
 *  `"credential"` (`providers logout`) is only the api_key line, leaving the
 *  block and the definition so the provider stays configured without a key. */
export type HarnessProviderRemovalScope = "credential" | "all";

/** Removes a provider (or only its key) from the controller's files.
 *  RESTARTS the daemon. Names and a scope travel; never a value. */
export async function removeHarnessProvider(
  name: string,
  scope: HarnessProviderRemovalScope = "all",
): Promise<void> {
  const response = await fetch(
    `${CONTROL_API}/providers/${encodeURIComponent(name)}?scope=${scope}`,
    { method: "DELETE" },
  );
  if (!response.ok) throw await apiError(response);
}

/** The NON-secret custom provider definition (ADR 0238) the controller
 *  writes into the daemon's user-global settings.yaml: an id, the wire
 *  flavor, an HTTPS base URL, a default model id and the auth METHOD. There
 *  is no key field — the api_key still goes into auth.yaml by hand (Studio
 *  rule 3), so a credential has no channel here. */
export interface HarnessCustomProviderDefinition {
  id: string;
  apiFlavor: string;
  baseURL: string;
  defaultModel: string;
  authMethod: "api_key" | "none";
}

/** The controller's answer to a definition write: whether it restarted the
 *  daemon (it does so at once for a keyless provider; an api_key one waits
 *  for the key), and the cause when that restart failed — the definition
 *  itself STANDS either way. */
export interface HarnessCustomProviderSaved {
  restarted: boolean;
  restartError: string;
}

/**
 * Writes a custom provider definition through the controller (`providers
 * add` without the login step). Throws on a refused write: an invalid
 * definition (400), an id already configured or an active imported
 * operator settings file (409), external mode (409).
 */
export async function createHarnessCustomProvider(
  definition: HarnessCustomProviderDefinition,
): Promise<HarnessCustomProviderSaved> {
  const response = await fetch(`${CONTROL_API}/providers/custom`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      id: definition.id,
      apiFlavor: definition.apiFlavor,
      baseURL: definition.baseURL,
      defaultModel: definition.defaultModel,
      authMethod: definition.authMethod,
    }),
  });
  if (!response.ok) throw await apiError(response);
  const body = (await response.json()) as {
    restarted?: boolean;
    restartError?: string;
  };
  return {
    restarted: body.restarted === true,
    restartError:
      typeof body.restartError === "string" ? body.restartError : "",
  };
}

/** Restarts the daemon with its current config — how a provider block just
 *  added to auth.yaml (guided add) becomes visible to mecated. */
export async function restartHarnessDaemon(): Promise<void> {
  const response = await fetch(`${CONTROL_API}/restart`, { method: "POST" });
  if (!response.ok) throw await apiError(response);
}

/**
 * Switches the daemon's active provider — "mock", "toolhive", or any name
 * already configured in auth.yaml — and restarts it on the spot. This is the
 * live equivalent of setting MECATL_STUDIO_PROVIDER and restarting `npm run
 * dev`: no credential travels with the request, only the chosen name.
 */
export async function setActiveHarnessProvider(
  kind: string,
): Promise<{ provider: string; selectedProvider: string | null }> {
  const response = await fetch(`${CONTROL_API}/providers/active`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ kind }),
  });
  if (!response.ok) throw await apiError(response);
  const body = (await response.json()) as {
    provider?: string;
    selectedProvider?: string | null;
  };
  return {
    provider: body.provider ?? "",
    selectedProvider: body.selectedProvider ?? null,
  };
}

/** The controller's answer to a ToolHive proxy start: whether the gateway
 *  answered within the bounded poll, and a next-step hint when it did not
 *  (typically: run `thv llm login` in a terminal first). */
export interface HarnessToolhiveStart {
  available: boolean;
  hint: string;
}

/**
 * Asks the controller to start the ToolHive LLM gateway proxy (`thv llm
 * proxy start`, detached) and to re-probe it — the `providers setup`
 * delegation to `thv llm`. Does NOT restart the daemon; "Set as active" on
 * the ToolHive row does. Throws when thv is not installed (409), in external
 * mode (409), or when the spawn failed.
 */
export async function startHarnessToolhiveGateway(): Promise<HarnessToolhiveStart> {
  const response = await fetch(`${CONTROL_API}/toolhive/start`, {
    method: "POST",
  });
  if (!response.ok) throw await apiError(response);
  const body = (await response.json()) as {
    available?: boolean;
    hint?: string;
  };
  return {
    available: body.available === true,
    hint: typeof body.hint === "string" ? body.hint : "",
  };
}

// ── Controller: workspace skills management ─────────────────────────────────
// The daemon has no skill write API (its skills snapshot is resolved once at
// startup), so skill CRUD goes to the managed-mode controller, which owns the
// pinned --skills-dir and restarts mecated when a change touches what the
// daemon can see. External mode has no controller: every one of these answers
// 409 there, and the UI renders the controls disabled instead of calling them.

/** A skill parked in the controller's `.disabled/` holding area. */
export interface DisabledSkillInfo {
  name: string;
  description: string;
}

/** Client-side mirror of the controller's name gate, so a bad name fails with
 *  this message rather than as a mystery 400. */
function requireSkillName(name: string): string {
  if (!validSkillName(name)) {
    throw new Error(
      "Skill names use lowercase letters, digits, hyphens, and underscores (max 64 characters)",
    );
  }
  return encodeURIComponent(name);
}

/** Skills the controller has disabled (moved out of the daemon's sight). */
export async function listDisabledHarnessSkills(
  signal?: AbortSignal,
): Promise<DisabledSkillInfo[]> {
  const response = await fetch(`${CONTROL_API}/skills/disabled`, {
    signal,
    cache: "no-store",
  });
  if (!response.ok) throw await apiError(response);
  const body = (await response.json()) as {
    disabled?: { name?: string; description?: string }[];
  };
  return (body.disabled ?? [])
    .filter((skill) => typeof skill.name === "string" && skill.name !== "")
    .map((skill) => ({
      name: skill.name ?? "",
      description: skill.description ?? "",
    }));
}

/** Creates a new skill folder with its SKILL.md, enabled. RESTARTS the daemon
 *  — a new skill is invisible until the startup snapshot is rebuilt. */
export async function createHarnessSkill(
  name: string,
  body: string,
): Promise<void> {
  requireSkillName(name);
  const response = await fetch(`${CONTROL_API}/skills`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name, body }),
  });
  if (!response.ok) throw await apiError(response);
}

/** Reads a skill's SKILL.md from the controller (either side of disabled). */
export async function fetchHarnessSkillBody(
  name: string,
  signal?: AbortSignal,
): Promise<string> {
  const response = await fetch(
    `${CONTROL_API}/skills/${requireSkillName(name)}/body`,
    { signal, cache: "no-store" },
  );
  if (!response.ok) throw await apiError(response);
  const body = (await response.json()) as { body?: string };
  return body.body ?? "";
}

/** One file of a multi-file skill create (a zip/folder upload). */
export interface HarnessSkillUploadFile {
  /** Relative POSIX path inside the skill folder, e.g. "scripts/run.sh". */
  path: string;
  contentBase64: string;
}

/** Creates a whole folder skill from an upload's files (must include a
 *  root SKILL.md). RESTARTS the daemon, like every skill mutation. */
export async function createHarnessSkillFiles(
  name: string,
  files: HarnessSkillUploadFile[],
): Promise<void> {
  const response = await fetch(`${CONTROL_API}/skills`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name: requireSkillName(name), files }),
  });
  if (!response.ok) throw await apiError(response);
}

/** One bundled file in a skill's folder. */
export interface HarnessSkillFile {
  /** Relative POSIX path inside the skill folder, e.g. "scripts/run.sh". */
  path: string;
  size: number;
}

/** Lists the files bundled in a skill's folder (works for disabled skills). */
export async function listHarnessSkillFiles(
  name: string,
  signal?: AbortSignal,
): Promise<HarnessSkillFile[]> {
  const response = await fetch(
    `${CONTROL_API}/skills/${requireSkillName(name)}/files`,
    { signal, cache: "no-store" },
  );
  if (!response.ok) throw await apiError(response);
  const body = (await response.json()) as { files?: HarnessSkillFile[] };
  return (body.files ?? []).filter(
    (file) => typeof file?.path === "string" && typeof file?.size === "number",
  );
}

/** Reads one bundled text file from a skill's folder (bounded server-side;
 *  binary or oversized files answer with a refusal that renders verbatim). */
export async function fetchHarnessSkillFile(
  name: string,
  path: string,
  signal?: AbortSignal,
): Promise<string> {
  const response = await fetch(
    `${CONTROL_API}/skills/${requireSkillName(name)}/file?path=${encodeURIComponent(path)}`,
    { signal, cache: "no-store" },
  );
  if (!response.ok) throw await apiError(response);
  const body = (await response.json()) as { content?: string };
  return body.content ?? "";
}

/** Writes a skill's SKILL.md. RESTARTS the daemon when the skill is enabled. */
export async function saveHarnessSkillBody(
  name: string,
  body: string,
): Promise<void> {
  const response = await fetch(
    `${CONTROL_API}/skills/${requireSkillName(name)}/body`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ body }),
    },
  );
  if (!response.ok) throw await apiError(response);
}

/** Moves a skill in or out of the disabled holding area. RESTARTS the daemon. */
export async function setHarnessSkillEnabled(
  name: string,
  enabled: boolean,
): Promise<void> {
  const response = await fetch(
    `${CONTROL_API}/skills/${requireSkillName(name)}/${enabled ? "enable" : "disable"}`,
    { method: "POST" },
  );
  if (!response.ok) throw await apiError(response);
}

/** Deletes a skill's directory from the workspace. RESTARTS the daemon. */
export async function deleteHarnessSkill(name: string): Promise<void> {
  const response = await fetch(
    `${CONTROL_API}/skills/${requireSkillName(name)}`,
    { method: "DELETE" },
  );
  if (!response.ok) throw await apiError(response);
}

/**
 * Begins the gateway's OAuth flow and returns the URL to send the user to.
 *
 * The controller performs discovery and dynamic client registration, then waits
 * for the provider to redirect back to its own loopback callback, where it
 * exchanges the code, stores the token and reconnects the daemon.
 */
export async function startHarnessGatewayOAuth(
  name: string,
  url: string,
): Promise<string> {
  const query = new URLSearchParams({ name, url });
  const response = await fetch(`${CONTROL_API}/mcp/oauth/start?${query}`);
  if (!response.ok) throw await apiError(response);
  const body = (await response.json()) as { authorizationUrl?: string };
  if (!body.authorizationUrl) {
    throw new Error("gateway returned no authorization URL");
  }
  return body.authorizationUrl;
}

/**
 * Waits for the controller to report a connected gateway.
 *
 * Polling rather than listening for the callback page's postMessage: that
 * message is addressed to a hardcoded origin (Studio's own port), so it never
 * arrives here. The controller's status is the shared source of truth either way.
 */
export async function waitForHarnessGateway(
  isCancelled: () => boolean,
  timeoutMs = 180_000,
): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (isCancelled()) return false;
    const status = await fetchHarnessControlStatus();
    if (status?.gateway) return true;
    await new Promise((resolve) => setTimeout(resolve, 1_500));
  }
  return false;
}
