/**
 * Browser-side client facade for Studio's two backends.
 *
 * DAEMON capabilities (sessions, runs, inventory, schedules, …) live in the
 * sibling modules and go through the mecatl TypeScript SDK (see ./sdk.ts);
 * this file re-exports them so importers keep one entry point.
 *
 * CONTROLLER capabilities — Studio's own local supervisor process behind the
 * /api/mecatl-control proxy (provider keys, model router, MCP gateway, skills
 * on disk, daemon restart) — are NOT engine capabilities and stay here as
 * plain fetch calls; the SDK has no notion of the controller.
 */

import { validSkillName } from "@/lib/controller-security.mjs";
import { apiError } from "./errors";

export { HarnessApiError } from "./errors";
export * from "./inventory";
export * from "./schedules";
export * from "./sessions";

// ── Controller: provider, model router, MCP gateway ─────────────────────────

const CONTROL_API = "/api/mecatl-control";

export interface HarnessControlStatus {
  /** "external" when Studio proxies to MECATL_BASE_URL; "managed" otherwise. */
  mode: "managed" | "external";
  provider: string;
  /** True when `provider` is the offline mock — the daemon serves canned
   *  turns rather than calling a real model. */
  isMock: boolean;
  running: boolean;
  gateway: { name: string; url: string } | null;
  toolhiveGateway: { available: boolean; active: boolean } | null;
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
      gateway?: { name?: string; url?: string } | null;
      toolhiveGateway?: { available?: boolean; active?: boolean } | null;
      modelRouter?: { enabled?: boolean; categories?: number } | null;
      operatorSettings?: boolean;
      skills?: { dir?: string };
      memory?: { dir?: string };
      configuredProviders?: unknown;
      selectedProvider?: string | null;
      authFile?: string;
    };
    return {
      mode: body.mode === "external" ? "external" : "managed",
      provider: body.provider ?? "unknown",
      isMock: Boolean(body.isMock),
      running: Boolean(body.running),
      gateway: body.gateway?.url
        ? { name: body.gateway.name ?? "gateway", url: body.gateway.url }
        : null,
      toolhiveGateway: body.toolhiveGateway
        ? {
            available: Boolean(body.toolhiveGateway.available),
            active: Boolean(body.toolhiveGateway.active),
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
    };
  } catch {
    return null;
  }
}

export interface HarnessRouterCategory {
  name: string;
  description: string;
  model: string;
}

export interface HarnessRouterConfig {
  enabled: boolean;
  classifierModel: string;
  defaultCategory: string;
  categories: HarnessRouterCategory[];
  /** True when routing comes from an imported operator settings file, which this UI must not overwrite. */
  managedByOperator: boolean;
}

export async function fetchHarnessRouter(
  signal?: AbortSignal,
): Promise<HarnessRouterConfig | null> {
  const response = await fetch(`${CONTROL_API}/model-router`, {
    signal,
    cache: "no-store",
  });
  if (!response.ok) return null;
  const body = (await response.json()) as {
    config?: {
      enabled?: boolean;
      classifierModel?: string;
      defaultCategory?: string;
      categories?: { name?: string; description?: string; model?: string }[];
    } | null;
    managedBy?: string;
  };
  return {
    enabled: Boolean(body.config?.enabled),
    classifierModel: body.config?.classifierModel ?? "",
    defaultCategory: body.config?.defaultCategory ?? "",
    categories: (body.config?.categories ?? []).map((category) => ({
      name: category.name ?? "",
      description: category.description ?? "",
      model: category.model ?? "",
    })),
    managedByOperator: body.managedBy === "operator-settings",
  };
}

/** Saves routing config. RESTARTS the daemon. */
export async function saveHarnessRouter(
  config: Omit<HarnessRouterConfig, "managedByOperator">,
): Promise<void> {
  const response = await fetch(`${CONTROL_API}/model-router`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(config),
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
  configured: boolean;
  /** A non-empty api_key / oauth access_token exists in the block. */
  keyPresent: boolean;
  source: string;
  /** The controller can key-test this kind with one cheap keyed call. */
  testable: boolean;
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
    }[];
  };
  return (body.providers ?? [])
    .filter((row) => typeof row.name === "string" && row.name !== "")
    .map((row) => ({
      name: row.name ?? "",
      configured: row.configured !== false,
      keyPresent: row.keyPresent === true,
      source: row.source ?? "auth.yaml",
      testable: row.testable === true,
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

/** Removes a provider's block from auth.yaml. RESTARTS the daemon. */
export async function removeHarnessProvider(name: string): Promise<void> {
  const response = await fetch(
    `${CONTROL_API}/providers/${encodeURIComponent(name)}`,
    { method: "DELETE" },
  );
  if (!response.ok) throw await apiError(response);
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
