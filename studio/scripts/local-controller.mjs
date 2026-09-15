import { execFile, spawn } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import {
  lstat,
  mkdir,
  open,
  readdir,
  readFile,
  rename,
  rm,
  stat,
  writeFile,
} from "node:fs/promises";
import http from "node:http";
import { homedir } from "node:os";
import { dirname, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import {
  requestIsAllowed,
  validateGatewayURL,
  validSkillName,
} from "../src/lib/controller-security.mjs";
import {
  customProviderProbeURL,
  KNOWN_AUTH_PROVIDERS,
  listAuthFileProviders,
  listSettingsProviders,
  removeAuthFileProvider,
  validProviderName,
} from "../src/lib/provider-auth.mjs";

const here = dirname(fileURLToPath(import.meta.url));
// Studio is a module INSIDE the mecatl monorepo, so the harness it drives is
// the repo root one directory up — the workspace it edits, the source of
// `bin/mecated` (built by `task build`), and the cwd every run inherits.
const mecatlDir = resolve(here, "../..");
const binary = resolve(mecatlDir, "bin/mecated");
const workspace = mecatlDir;
const studioStateDir = resolve(here, "../.scratch");
const operatorSettingsFile = resolve(studioStateDir, "operator-settings.yaml");
const routerSettingsFile = resolve(
  studioStateDir,
  "model-router-settings.yaml",
);
const routerStateFile = resolve(studioStateDir, "model-router.json");
// Project-scoped skills only. A SKILL.md steers the model the same way AGENTS.md
// does, so discovery is deliberately pinned to the workspace and we never pass
// --skills-conventional (which would also pull in ~/.claude/skills and the
// user-global mecatl dir — a much wider trust surface than this app should open).
const skillsDir = resolve(workspace, ".mecatl/skills");
// Disabled skills are MOVED into a holding area inside the pinned skills dir,
// not deleted and not flagged: mecated's discovery walks only the direct
// children of --skills-dir looking for <name>/SKILL.md, so a nested dir is
// invisible to it, and the skill-name grammar forbids a leading dot, so
// `.disabled` can never collide with a real skill.
const disabledSkillsDir = resolve(skillsDir, ".disabled");
// Per-project memory (the Remember/Recall/SearchMemory tools) is OFF in mecated
// until --memory-dir is passed, unlike the user model which is on by default. The
// store is per-project by design, so it lives beside the session store rather than
// in a shared location. Consolidation stays off: it spends tokens in the background.
const memoryDir = resolve(workspace, ".scratch/studio-memory");
const authFile = process.env.XDG_CONFIG_HOME
  ? resolve(process.env.XDG_CONFIG_HOME, "mecatl/auth.yaml")
  : resolve(homedir(), ".config/mecatl/auth.yaml");
// The user-global operator settings file, same XDG convention as auth.yaml.
// mecated always reads it at the operator tier; it is where a hand-added
// custom `providers:` block (ADR 0238) lives unless an imported
// operator-settings.yaml (a CLI-tier file, which wins whole-block) carries
// its own.
const userSettingsFile = process.env.XDG_CONFIG_HOME
  ? resolve(process.env.XDG_CONFIG_HOME, "mecatl/settings.yaml")
  : resolve(homedir(), ".config/mecatl/settings.yaml");
// mecated's --ready-file target: the atomically-published mecated-ready/1
// document carrying the RESOLVED listener addresses (the daemon binds
// 127.0.0.1:0 and reports what the kernel picked), pid, api_major, features,
// and deployment. Written only after every listener is up, never removed by
// the daemon — the controller unlinks the stale one before each spawn.
const readyFilePath = resolve(studioStateDir, "mecated-ready.json");
// The named FIFO backing --lifetime-pipe-fd. mecated fstat's the descriptor
// and rejects anything that is not a real pipe (S_IFIFO) — and Node's stdio
// "pipe" entries are AF_UNIX socketpairs — so the pipe is made with
// mkfifo(1) and its NAME is unlinked as soon as both ends are open.
const lifetimeFifoPath = resolve(studioStateDir, "mecated-lifetime.fifo");
// How long to wait for the ready file. Remote MCP gateways may cold-start and
// mecatl gives their initialize handshake up to 30 seconds; MCP construction
// happens during composition, which completes BEFORE the ready file is
// written, so the wait stays comfortably longer than that.
const readyWaitMs = 60_000;

/** auth.yaml's text, or "" when it does not exist / cannot be read. */
async function readAuthFileText() {
  try {
    return await readFile(authFile, "utf8");
  } catch {
    return "";
  }
}

/**
 * Names only of the providers configured in auth.yaml — never their values.
 * The line scan lives in src/lib/provider-auth.mjs (shared with its vitest
 * suite): it deliberately cannot read a credential, only detect that a
 * `providers:` block names a key at one level of indent.
 */
async function listConfiguredProviderNames() {
  return listAuthFileProviders(await readAuthFileText()).map(
    (provider) => provider.name,
  );
}

/**
 * The operator-defined custom providers (ADR 0238) mecated will actually
 * see, mirroring its whole-block first-non-nil `providers:` capture across
 * the operator tier: the imported operator-settings.yaml (the CLI-tier file
 * this controller passes) is consulted first, then the user-global
 * settings.yaml. The section is non-secret by design — ids, flavors, base
 * URLs, auth methods; any key stays in auth.yaml.
 */
async function listCustomSettingsProviders() {
  const sources = operatorSettingsActive
    ? [operatorSettingsFile, userSettingsFile]
    : [userSettingsFile];
  for (const file of sources) {
    let text;
    try {
      text = await readFile(file, "utf8");
    } catch {
      continue;
    }
    const providers = listSettingsProviders(text);
    if (providers !== null) return providers;
  }
  return [];
}

/**
 * Every provider name the daemon can be started on: the auth.yaml blocks
 * plus the settings-defined custom providers — a keyless
 * (`auth.method: none`) custom provider never appears in auth.yaml, so the
 * auth scan alone would refuse to select it.
 */
async function listSelectableProviderNames() {
  const names = await listConfiguredProviderNames();
  for (const provider of await listCustomSettingsProviders()) {
    if (!names.includes(provider.name)) names.push(provider.name);
  }
  return names;
}

// ── Provider management ─────────────────────────────────────────────────────
// The provider inventory, guided add, key test, and removal are controller-
// owned for the same reason the skills routes are: mecated reads auth.yaml
// once at startup and has no HTTP write API for it. Every route keeps Studio
// rule 3 intact — a credential is read SERVER-SIDE here for exactly one
// outbound probe or removed from the file; no response body ever carries a
// key, not even a redacted preview, and there is no route that ACCEPTS one.

/**
 * One cheap authenticated read per testable provider, mirroring the base
 * URLs the daemon itself defaults to (internal/cliconfig: the controller
 * spawns mecated without --*-base-url overrides, so these defaults are what
 * the key will actually be used against). OpenRouter's /models is public
 * (the daemon's own lister is deliberately keyless), so its keyed metadata
 * endpoint /key is the probe there.
 */
const providerKeyProbes = {
  openrouter: (key) => ({
    url: "https://openrouter.ai/api/v1/key",
    headers: { Authorization: `Bearer ${key}` },
  }),
  openai: (key) => ({
    url: "https://api.openai.com/v1/models",
    headers: { Authorization: `Bearer ${key}` },
  }),
  anthropic: (key) => ({
    url: "https://api.anthropic.com/v1/models?limit=1",
    headers: { "x-api-key": key, "anthropic-version": "2023-06-01" },
  }),
  opencode: (key) => ({
    url: "https://opencode.ai/zen/go/v1/models",
    headers: { Authorization: `Bearer ${key}` },
  }),
};

/**
 * The named provider's api_key value, read server-side for the one outbound
 * key probe. Deliberately controller-local (NOT in provider-auth.mjs, which
 * the browser bundle imports) and deliberately api_key-only: openai-codex's
 * oauth block is not key-testable. The value is never logged or echoed.
 */
function readProviderCredential(text, name) {
  const lines = String(text ?? "").split("\n");
  const providersAt = lines.findIndex((line) => /^providers:\s*$/.test(line));
  if (providersAt === -1) return "";
  let inBlock = false;
  for (const line of lines.slice(providersAt + 1)) {
    if (/^\S/.test(line)) break; // dedented past the providers block
    const key = line.match(/^ {2}([A-Za-z0-9_-]+):/);
    if (key) {
      inBlock = key[1] === name;
      continue;
    }
    if (!inBlock) continue;
    const credential = line.match(/^\s+api_key:\s*(.+)$/);
    if (!credential) continue;
    let value = credential[1].trim();
    const quote = value[0];
    if ((quote === '"' || quote === "'") && value.endsWith(quote)) {
      value = value.slice(1, -1);
    }
    return value.startsWith("#") ? "" : value;
  }
  return "";
}

/** Provider error text, bounded and de-control-charred before it reaches a
 *  response body (it is provider-authored, not ours). */
function clampProviderError(text) {
  return (
    String(text ?? "")
      // biome-ignore lint/suspicious/noControlCharactersInRegex: stripping them is the point
      .replace(/[\u0000-\u001f\u007f]+/g, " ")
      .replace(/\s+/g, " ")
      .trim()
      .slice(0, 300)
  );
}

/**
 * The one keyed probe request for a CUSTOM (settings-defined, ADR 0238)
 * provider: a models listing against ITS configured base URL, with the
 * header shape its api_flavor dictates. Controller-local for the same reason
 * readProviderCredential is — it carries the key.
 */
function customProviderKeyProbe(definition, key) {
  const url = customProviderProbeURL(definition.apiFlavor, definition.baseURL);
  if (definition.apiFlavor === "anthropic-messages") {
    return {
      url,
      headers: { "x-api-key": key, "anthropic-version": "2023-06-01" },
    };
  }
  return { url, headers: { Authorization: `Bearer ${key}` } };
}

/**
 * Runs the one bounded probe ({url, headers}) for a provider's stored key.
 * Returns the JSON the route answers with:
 * {ok:true} | {ok:false, status, rejected?, error}.
 * A 401/403 is the provider saying the KEY is bad; anything else (5xx,
 * timeout, DNS) is an infrastructure answer, reported distinctly so a red
 * "key rejected" dot is never shown for a provider outage.
 */
async function probeProviderKey(probe) {
  let response;
  try {
    response = await fetch(probe.url, {
      headers: { Accept: "application/json", ...probe.headers },
      redirect: "manual", // never replay the credential to a redirect target
      signal: AbortSignal.timeout(10_000),
    });
  } catch (error) {
    return {
      ok: false,
      status: 0,
      error: clampProviderError(error?.message || "provider unreachable"),
    };
  }
  const body = await response.text().catch(() => "");
  if (response.ok) return { ok: true };
  if (response.status === 401 || response.status === 403) {
    return {
      ok: false,
      status: response.status,
      rejected: true,
      error: `key rejected (HTTP ${response.status})`,
    };
  }
  return {
    ok: false,
    status: response.status,
    error: clampProviderError(body) || `HTTP ${response.status}`,
  };
}
// ── Workspace skills management ────────────────────────────────────────────
// The daemon has no HTTP write API for skills (Studio shipped them read-only;
// authoring is the ADR-0233 backlog item), and it resolves the skills dir
// ONCE at startup: skillfs's FSSource is a construction-time snapshot
// ("bodies are retained; no re-read") and internal/app/build.go registers
// "the skills resolved once at build time". So skill CRUD lives here, on the
// controller that owns --skills-dir, and every mutation the daemon can see
// restarts mecated through the same queueRestart machinery as config writes —
// in-flight runs and session ids die with it, exactly like a gateway or
// model-router write.

/** Max SKILL.md body accepted on the edit path. */
const maxSkillBodyBytes = 262_144;

// Multi-file create caps (a zip/folder upload): a skill is a small folder of
// instructions plus a few assets, never a repository.
const maxSkillUploadFiles = 200;
const maxSkillUploadFileBytes = 2 * 1024 * 1024;
const maxSkillUploadTotalBytes = 8 * 1024 * 1024;
// The create body cap: the total decoded cap, base64-inflated, plus headroom.
const maxSkillCreateBodyBytes = 12 * 1024 * 1024;

/** The controller's own guard on an uploaded relative path — the browser
 *  plans uploads too, but the server side is the one that counts. */
function validSkillUploadPath(path) {
  if (typeof path !== "string" || path === "" || path.includes("\\"))
    return false;
  return path
    .split("/")
    .every(
      (segment) =>
        segment !== "" &&
        segment !== "." &&
        segment !== ".." &&
        !segment.startsWith("."),
    );
}

function skillClientError(message, statusCode = 400) {
  return Object.assign(new Error(message), { statusCode });
}

/**
 * The enabled/disabled directory pair for a validated skill name. The grammar
 * already forbids separators, dots, and whitespace; the prefix check is
 * defense-in-depth should the grammar ever loosen.
 */
function skillPaths(name) {
  if (!validSkillName(name))
    throw skillClientError(
      "Skill names use lowercase letters, digits, hyphens, and underscores (max 64 characters)",
    );
  const enabled = resolve(skillsDir, name);
  const disabled = resolve(disabledSkillsDir, name);
  if (
    !enabled.startsWith(skillsDir + sep) ||
    !disabled.startsWith(disabledSkillsDir + sep)
  )
    throw skillClientError("Skill name escapes the skills directory");
  return { enabled, disabled };
}

async function isDirectory(path) {
  try {
    return (await stat(path)).isDirectory();
  } catch {
    return false;
  }
}

/**
 * One-line `description:` scan of a SKILL.md frontmatter block — a line scan,
 * never a YAML parse, mirroring listConfiguredProviderNames. Best-effort: a
 * block-scalar or absent description simply lists as "".
 */
function skillDescription(markdown) {
  const lines = markdown.split("\n");
  if (lines[0]?.trim() !== "---") return "";
  for (const line of lines.slice(1)) {
    if (line.trim() === "---") break;
    const match = line.match(/^description:\s*(.+)$/);
    if (!match) continue;
    const value = match[1].trim();
    if (/^[>|]/.test(value)) return "";
    return value.replace(/^["']|["']$/g, "");
  }
  return "";
}

/** Cap on the bundled-file listing — a skill is a small folder, not a repo. */
const maxSkillFiles = 500;

/**
 * Bounded recursive listing of one skill's folder: relative POSIX paths +
 * sizes. Symlinks are never followed (a link could point outside the skills
 * dir), dot-entries are skipped (.DS_Store noise), and the walk stops at
 * maxSkillFiles entries / depth 8.
 */
async function listSkillFiles(root) {
  const files = [];
  async function walk(dir, prefix, depth) {
    if (depth > 8 || files.length >= maxSkillFiles) return;
    let entries;
    try {
      entries = await readdir(dir, { withFileTypes: true });
    } catch {
      return;
    }
    entries.sort((a, b) => a.name.localeCompare(b.name));
    for (const entry of entries) {
      if (files.length >= maxSkillFiles) return;
      if (entry.name.startsWith(".") || entry.isSymbolicLink()) continue;
      const rel = prefix ? `${prefix}/${entry.name}` : entry.name;
      if (entry.isDirectory()) {
        await walk(resolve(dir, entry.name), rel, depth + 1);
      } else if (entry.isFile()) {
        try {
          files.push({
            path: rel,
            size: (await stat(resolve(dir, entry.name))).size,
          });
        } catch {
          // Raced away between readdir and stat — skip it.
        }
      }
    }
  }
  await walk(root, "", 0);
  return files;
}

/** Names (+ best-effort descriptions) in the `.disabled/` holding area. */
async function listDisabledSkills() {
  let entries;
  try {
    entries = await readdir(disabledSkillsDir, { withFileTypes: true });
  } catch {
    return [];
  }
  const skills = [];
  for (const entry of entries) {
    if (!entry.isDirectory() || !validSkillName(entry.name)) continue;
    let description = "";
    try {
      description = skillDescription(
        await readFile(
          resolve(disabledSkillsDir, entry.name, "SKILL.md"),
          "utf8",
        ),
      );
    } catch {
      // A SKILL.md-less folder still lists — enabling it back is how the
      // operator recovers it.
    }
    skills.push({ name: entry.name, description });
  }
  return skills.sort((a, b) => a.name.localeCompare(b.name));
}

// The kinds startMecatl/preferredKind understand: the two synthetic ones plus
// every provider auth.yaml can name a block for (KNOWN_AUTH_PROVIDERS is the
// same registry the guided-add UI offers).
const KNOWN_PROVIDER_KINDS = new Set([
  "mock",
  "toolhive",
  ...KNOWN_AUTH_PROVIDERS.map((entry) => entry.name),
]);
const configuredProvider =
  process.env.MECATL_STUDIO_PROVIDER?.trim().toLowerCase() || "";
if (configuredProvider && !KNOWN_PROVIDER_KINDS.has(configuredProvider)) {
  throw new Error(
    `MECATL_STUDIO_PROVIDER must be one of: ${[...KNOWN_PROVIDER_KINDS].join(", ")}`,
  );
}
// The LIVE active-provider selection. Seeded from MECATL_STUDIO_PROVIDER at
// startup, but — unlike that env var, which is frozen for the process's
// lifetime — reassignable at runtime through POST /providers/active (the
// Studio settings UI's provider switch), so an operator can move between the
// offline mock and a real provider without restarting `npm run dev`.
let activeProviderOverride = configuredProvider || null;
const managedAuthToken = (
  process.env.MECATL_AUTH_TOKEN || randomBytes(32).toString("base64url")
).replace(/^Bearer\s+/i, "");
const mcpProxySecret = randomBytes(24).toString("base64url");
const studioPublicOrigin =
  process.env.MECATL_STUDIO_PUBLIC_ORIGIN?.trim() || "http://localhost:3000";
const allowedOrigins = new Set(
  (
    process.env.MECATL_STUDIO_ORIGINS ||
    "http://localhost:3000,http://127.0.0.1:3000"
  )
    .split(",")
    .map((origin) => origin.trim())
    .filter(Boolean),
);
allowedOrigins.add(studioPublicOrigin);
let child = null;
let provider = "offline mock";
let mecatlBaseURL = "";
// What the child's ready file reported at the last successful start: the
// non-secret compatibility descriptor a parent may surface (H1.3). Null
// until a child has published one.
let readyInfo = null;
let gateway = null;
let modelRouterConfig = null;
let operatorSettingsActive = false;
let startupLog = "";
let restartQueue = Promise.resolve();
let gatewayRefresh = null;
let shuttingDown = false;
let restartTimer = null;
let restartFailures = 0;
let startupError = "";
const expectedExits = new WeakSet();
const oauthAttempts = new Map();
const oauthRedirectUri = "http://127.0.0.1:8788/oauth/callback";
// The ToolHive LLM gateway reaches mecated through "thv llm proxy", a LOOPBACK
// reverse proxy that injects a fresh OIDC token per request. The controller
// never holds a gateway credential itself — that is the whole point of routing
// through the proxy rather than pasting a key. mecated auto-detects the same
// proxy from ToolHive's own config, so the port here is only used for the
// readiness probe that decides whether "toolhive" is an offerable provider.
const toolhiveGatewayURL = "http://127.0.0.1:14000/v1";
let toolhiveReady = false;

const delay = (milliseconds) =>
  new Promise((done) => setTimeout(done, milliseconds));

function jsonError(response, status, message) {
  response.statusCode = status;
  response.end(JSON.stringify({ error: message }));
}

async function readBody(request, limit = 1_048_576) {
  const declared = Number(request.headers["content-length"] || 0);
  if (declared > limit)
    throw Object.assign(new Error("request too large"), { statusCode: 413 });
  const chunks = [];
  let size = 0;
  for await (const chunk of request) {
    size += chunk.length;
    if (size > limit)
      throw Object.assign(new Error("request too large"), { statusCode: 413 });
    chunks.push(chunk);
  }
  return Buffer.concat(chunks);
}

/**
 * A REAL pipe for mecated's --lifetime-pipe-fd. The daemon fstat's the
 * inherited descriptor and rejects anything that is not S_IFIFO — and Node's
 * stdio "pipe" entries are AF_UNIX socketpairs — so the pipe is a named FIFO
 * made with mkfifo(1), both ends opened, and the name unlinked (the
 * descriptors outlive it). The controller holds the WRITE end and never
 * writes: if this process dies — SIGKILL included — the kernel closes it,
 * the child reads EOF, and mecated stops through its ordinary graceful
 * shutdown. That is what keeps a controller crash from orphaning a daemon.
 */
async function createLifetimePipe() {
  await rm(lifetimeFifoPath, { force: true });
  await new Promise((done, fail) => {
    execFile("mkfifo", ["-m", "600", lifetimeFifoPath], (error) =>
      error ? fail(error) : done(),
    );
  });
  try {
    // Opening either end of a FIFO blocks until the other side opens, so the
    // two opens must run concurrently; together they complete immediately.
    const [readEnd, writeEnd] = await Promise.all([
      open(lifetimeFifoPath, "r"),
      open(lifetimeFifoPath, "w"),
    ]);
    return { readEnd, writeEnd };
  } finally {
    await rm(lifetimeFifoPath, { force: true });
  }
}

/**
 * Waits for THIS child's mecated-ready/1 document. The daemon publishes it
 * atomically (temp + rename) and only after composition and every listener
 * are up, so a successful read IS readiness — no connect polling, no
 * stability window. A parse failure is a foreign file, never a torn write;
 * a pid mismatch is the previous child's stale document (unlinked before the
 * spawn, so only a pathological race shows one) and polling continues.
 */
async function waitForReadyDoc(proc) {
  const deadline = Date.now() + readyWaitMs;
  while (Date.now() < deadline) {
    if (proc.exitCode !== null)
      throw new Error(`mecatl exited during startup (code ${proc.exitCode})`);
    if (child !== proc)
      throw new Error("mecatl was replaced before it became ready");
    let text = "";
    try {
      text = await readFile(readyFilePath, "utf8");
    } catch {
      /* not published yet */
    }
    if (text) {
      let doc = null;
      try {
        doc = JSON.parse(text);
      } catch {
        /* not a ready document */
      }
      if (doc && doc.schema !== "mecated-ready/1")
        throw new Error(
          `mecated wrote an unsupported ready-file schema "${doc.schema}"`,
        );
      if (doc?.pid === proc.pid) {
        if (typeof doc.http_address !== "string" || doc.http_address === "")
          throw new Error("mecatl's ready file reports no HTTP listener");
        return doc;
      }
    }
    await delay(100);
  }
  throw new Error("mecatl did not publish its ready file in time");
}

function fetchMecatl(path, options = {}) {
  if (!mecatlBaseURL)
    throw new Error("mecated has not been assigned a listener yet");
  const headers = new Headers(options.headers);
  headers.set("authorization", `Bearer ${managedAuthToken}`);
  return fetch(new URL(path, mecatlBaseURL), { ...options, headers });
}

function providerLabel(kind) {
  if (kind === "mock") return "offline mock";
  if (kind === "toolhive") return "ToolHive LLM gateway";
  return (
    KNOWN_AUTH_PROVIDERS.find((entry) => entry.name === kind)?.label ?? kind
  );
}

function startupFailure(kind, message) {
  startupError =
    kind !== "mock" && kind !== "toolhive"
      ? `${providerLabel(kind)} could not start. Add providers.${kind}.api_key to ${authFile}, then switch to it again. mecated: ${message}`
      : message;
  return new Error(startupError);
}

// A short probe, deliberately: when no token is cached the proxy blocks on an
// interactive browser login, and a controller start must never hang on that.
// Timing out simply means "not offerable right now" and studio falls back.
async function detectToolhiveGateway() {
  try {
    const response = await fetch(`${toolhiveGatewayURL}/models`, {
      signal: AbortSignal.timeout(2500),
    });
    return response.ok;
  } catch {
    return false;
  }
}

// Provider credentials are owned by mecated's conventional auth file, never
// copied through a browser form or patched into the child's environment here.
// MECATL_STUDIO_PROVIDER / the /providers/active switch select a provider
// without carrying its credential.
const preferredKind = () =>
  activeProviderOverride || (toolhiveReady ? "toolhive" : "mock");

/** Whether `kind` is safe to hand to startMecatl right now: the two
 *  synthetic kinds (mock always, toolhive only while the gateway answers), a
 *  provider that actually has a block in auth.yaml, or a settings-defined
 *  custom provider (ADR 0238) — never an arbitrary string, so a typo can't
 *  reach mecated's fail-fast --default-provider check and crash the child. */
function isSelectableProviderKind(kind, selectableNames) {
  if (kind === "mock") return true;
  if (kind === "toolhive") return toolhiveReady;
  return selectableNames.includes(kind);
}

function normalizeModelRouter(input) {
  const classifierModel =
    typeof input?.classifierModel === "string"
      ? input.classifierModel.trim()
      : "";
  if (!classifierModel || classifierModel.length > 200)
    throw new Error("Choose a valid classifier model");
  if (
    !Array.isArray(input?.categories) ||
    input.categories.length < 2 ||
    input.categories.length > 8
  ) {
    throw new Error("Semantic routing needs between 2 and 8 categories");
  }
  const seen = new Set();
  const categories = input.categories.map((category) => {
    const name =
      typeof category?.name === "string"
        ? category.name.trim().toLowerCase()
        : "";
    const description =
      typeof category?.description === "string"
        ? category.description.trim()
        : "";
    const model =
      typeof category?.model === "string" ? category.model.trim() : "";
    if (!/^[a-z][a-z0-9_-]{0,39}$/.test(name))
      throw new Error(
        "Category names must start with a letter and use only letters, numbers, underscores, or dashes",
      );
    if (seen.has(name)) throw new Error(`Category name ${name} is duplicated`);
    if (!description || description.length > 300)
      throw new Error(
        `Category ${name} needs a distinct description of at most 300 characters`,
      );
    if (!model || model.length > 200)
      throw new Error(`Choose a model for category ${name}`);
    seen.add(name);
    return { name, description, model };
  });
  const defaultCategory =
    typeof input?.defaultCategory === "string"
      ? input.defaultCategory.trim().toLowerCase()
      : "";
  if (!seen.has(defaultCategory))
    throw new Error(
      "The default category must match one of the routing categories",
    );
  return {
    enabled: input?.enabled !== false,
    classifierModel,
    defaultCategory,
    categories,
  };
}

const yamlString = (value) => JSON.stringify(String(value));

function renderModelRouterYAML(config) {
  const categories = config.categories
    .map((category) =>
      [
        `      - name: ${yamlString(category.name)}`,
        `        description: ${yamlString(category.description)}`,
        `        model: ${yamlString(category.model)}`,
      ].join("\n"),
    )
    .join("\n");
  return [
    "# Managed by Mecatl Studio. This is loaded at the trusted CLI/operator tier.",
    "models:",
    "  slots:",
    `    router: ${yamlString(config.classifierModel)}`,
    "  router:",
    `    disabled: ${config.enabled ? "false" : "true"}`,
    "    classifier-slot: router",
    `    default-category: ${yamlString(config.defaultCategory)}`,
    "    categories:",
    categories,
    "",
  ].join("\n");
}

async function persistModelRouter(config) {
  await mkdir(studioStateDir, { recursive: true, mode: 0o700 });
  const yamlTemp = `${routerSettingsFile}.tmp`;
  const jsonTemp = `${routerStateFile}.tmp`;
  await writeFile(yamlTemp, renderModelRouterYAML(config), { mode: 0o600 });
  await writeFile(jsonTemp, `${JSON.stringify(config, null, 2)}\n`, {
    mode: 0o600,
  });
  await rename(yamlTemp, routerSettingsFile);
  await rename(jsonTemp, routerStateFile);
}

async function loadModelRouter() {
  try {
    return normalizeModelRouter(
      JSON.parse(await readFile(routerStateFile, "utf8")),
    );
  } catch (error) {
    if (error?.code !== "ENOENT")
      process.stderr.write(
        `[router] saved configuration ignored: ${error.message || error}\n`,
      );
    return null;
  }
}

async function hasOperatorSettings() {
  try {
    await readFile(operatorSettingsFile, "utf8");
    return true;
  } catch (error) {
    if (error?.code !== "ENOENT")
      process.stderr.write(
        `[settings] operator configuration ignored: ${error.message || error}\n`,
      );
    return false;
  }
}

function wellKnownURL(base, name) {
  const url = new URL(base);
  const issuerPath =
    url.pathname === "/" ? "" : url.pathname.replace(/\/$/, "");
  return new URL(`/.well-known/${name}${issuerPath}`, url.origin).toString();
}

async function fetchJSON(url) {
  try {
    const response = await fetch(url, {
      headers: { Accept: "application/json" },
      redirect: "follow",
      signal: AbortSignal.timeout(8_000),
    });
    if (!response.ok) return null;
    return await response.json();
  } catch {
    return null;
  }
}

function requireHttpsEndpoint(value, label) {
  if (!value) throw new Error(`OAuth metadata does not include ${label}`);
  const endpoint = new URL(value);
  if (endpoint.protocol !== "https:")
    throw new Error(`OAuth ${label} must use HTTPS`);
  return endpoint.toString();
}

async function discoverOAuth(gatewayURL) {
  const resourceCandidates = [];
  try {
    const probe = await fetch(gatewayURL, {
      method: "GET",
      headers: { Accept: "text/event-stream" },
      redirect: "manual",
      signal: AbortSignal.timeout(5_000),
    });
    const challenge = probe.headers.get("www-authenticate") || "";
    const match = challenge.match(
      /resource_metadata\s*=\s*(?:"([^"]+)"|([^,\s]+))/i,
    );
    if (match?.[1] || match?.[2]) resourceCandidates.push(match[1] || match[2]);
    await probe.body?.cancel().catch(() => undefined);
  } catch {
    /* fall through to RFC 9728 well-known locations */
  }

  resourceCandidates.push(
    wellKnownURL(gatewayURL, "oauth-protected-resource"),
    new URL(
      "/.well-known/oauth-protected-resource",
      gatewayURL.origin,
    ).toString(),
  );

  let resourceMetadata = null;
  for (const candidate of [...new Set(resourceCandidates)]) {
    let candidateURL;
    try {
      candidateURL = requireHttpsEndpoint(
        candidate,
        "protected resource metadata URL",
      );
    } catch {
      continue;
    }
    resourceMetadata = await fetchJSON(candidateURL);
    if (resourceMetadata) break;
  }

  const authorizationServer =
    resourceMetadata?.authorization_servers?.[0] || gatewayURL.origin;
  const metadataCandidates = [
    wellKnownURL(authorizationServer, "oauth-authorization-server"),
    wellKnownURL(authorizationServer, "openid-configuration"),
    new URL(
      "/.well-known/openid-configuration",
      new URL(authorizationServer).origin,
    ).toString(),
  ];
  let metadata = null;
  for (const candidate of [...new Set(metadataCandidates)]) {
    metadata = await fetchJSON(candidate);
    if (metadata) break;
  }
  if (!metadata)
    throw new Error(
      "The gateway did not advertise usable MCP OAuth authorization-server metadata",
    );

  const authorizationEndpoint = requireHttpsEndpoint(
    metadata.authorization_endpoint,
    "authorization endpoint",
  );
  const tokenEndpoint = requireHttpsEndpoint(
    metadata.token_endpoint,
    "token endpoint",
  );
  const registrationEndpoint = requireHttpsEndpoint(
    metadata.registration_endpoint,
    "dynamic client registration endpoint",
  );
  const resourceScopes = Array.isArray(resourceMetadata?.scopes_supported)
    ? resourceMetadata.scopes_supported
    : [];
  const authScopes = Array.isArray(metadata.scopes_supported)
    ? metadata.scopes_supported
    : [];
  const defaultScopes = ["openid", "profile", "email", "offline_access"];
  const preferredAuthScopes = defaultScopes.filter((scope) =>
    authScopes.includes(scope),
  );
  const scopes = [...new Set([...resourceScopes, ...preferredAuthScopes])];

  return {
    authorizationEndpoint,
    tokenEndpoint,
    registrationEndpoint,
    resource: resourceMetadata?.resource || gatewayURL.toString(),
    scope: scopes.join(" ") || "openid profile email offline_access",
  };
}

function queueRestart(operation) {
  const run = restartQueue.then(operation, operation);
  restartQueue = run.catch(() => undefined);
  return run;
}

function scheduleMecatlRestart() {
  if (shuttingDown || restartTimer || child) return;
  const wait = Math.min(10_000, 750 * 2 ** restartFailures);
  process.stderr.write(
    `[supervisor] mecated stopped unexpectedly; restarting in ${wait}ms\n`,
  );
  restartTimer = setTimeout(() => {
    restartTimer = null;
    queueRestart(async () => {
      if (shuttingDown || child) return;
      try {
        await startMecatl(preferredKind());
        restartFailures = 0;
        process.stderr.write("[supervisor] mecated restarted\n");
      } catch (error) {
        restartFailures += 1;
        process.stderr.write(
          `[supervisor] restart failed: ${error.message || error}\n`,
        );
        scheduleMecatlRestart();
      }
    });
  }, wait);
}

async function refreshGatewayAccessToken(force = false) {
  if (!gateway?.refreshToken || !gateway.clientId || !gateway.tokenEndpoint)
    return false;
  if (!force && gateway.expiresAt && gateway.expiresAt > Date.now() + 60_000)
    return true;
  if (gatewayRefresh) return gatewayRefresh;
  gatewayRefresh = (async () => {
    // No `scope` on refresh: RFC 6749 §6 makes it optional (the server reuses
    // the originally granted scopes), and this provider rejects the request as
    // malformed when it is present — which silently killed every auto-refresh
    // and made gateway auth die on the hour.
    const refreshBody = new URLSearchParams({
      grant_type: "refresh_token",
      refresh_token: gateway.refreshToken,
      client_id: gateway.clientId,
    });
    if (gateway.resource) refreshBody.set("resource", gateway.resource);
    const tokenResponse = await fetch(gateway.tokenEndpoint, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: refreshBody,
    });
    const tokenResult = await tokenResponse.json().catch(() => ({}));
    if (!tokenResponse.ok || !tokenResult.access_token) {
      process.stderr.write(
        `[oauth] refresh rejected (${tokenResponse.status}, ${String(tokenResult.error || "unknown_error").slice(0, 80)})\n`,
      );
      throw new Error(
        tokenResult.error_description ||
          tokenResult.error ||
          "Gateway token refresh failed",
      );
    }
    gateway.token = tokenResult.access_token;
    if (tokenResult.refresh_token)
      gateway.refreshToken = tokenResult.refresh_token;
    gateway.expiresAt =
      Date.now() + Math.max(60, Number(tokenResult.expires_in) || 300) * 1000;
    process.stderr.write("[oauth] gateway access token refreshed\n");
    return true;
  })().finally(() => {
    gatewayRefresh = null;
  });
  return gatewayRefresh;
}

async function stopChild() {
  if (!child) {
    await delay(200);
    return;
  }
  const current = child;
  expectedExits.add(current);
  await new Promise((done) => {
    const timer = setTimeout(() => {
      current.kill("SIGKILL");
      done();
    }, 2000);
    current.once("exit", () => {
      clearTimeout(timer);
      done();
    });
    current.kill("SIGTERM");
  });
  if (child === current) child = null;
  // Let loopback listeners finish closing before the next child binds them.
  await delay(250);
}

async function startMecatl(kind) {
  if (restartTimer) {
    clearTimeout(restartTimer);
    restartTimer = null;
  }
  await stopChild();
  startupLog = "";
  startupError = "";
  // No listener is assigned until THIS child's ready file names one: the old
  // base URL points at a dead port, and fetchMecatl's guard is the honest
  // answer while the restart is in flight.
  mecatlBaseURL = "";
  readyInfo = null;
  await mkdir(studioStateDir, { recursive: true, mode: 0o700 });
  // The daemon never removes its ready file (a SIGKILLed one could not), so
  // the previous child's document must go before the spawn — otherwise the
  // wait below could read yesterday's addresses. The pid check is the
  // backstop for the pathological race.
  await rm(readyFilePath, { force: true });
  const args = [
    "serve",
    "--workspace",
    workspace,
    "--store-dir",
    ".scratch/studio-sessions",
    "--grpc-addr",
    "127.0.0.1:0",
    // An ephemeral HTTP port: the kernel picks it at bind(2) time and the
    // ready file reports it RESOLVED — no pre-bind pick, no TOCTOU window.
    "--http-addr",
    "127.0.0.1:0",
    "--ready-file",
    readyFilePath,
  ];
  if (operatorSettingsActive)
    args.push("--permission-config", operatorSettingsFile);
  else if (modelRouterConfig)
    args.push("--permission-config", routerSettingsFile);
  // mecated refuses to start on a missing --skills-dir, and an empty directory is
  // the correct "no skills yet" state, so create it before every spawn.
  await mkdir(skillsDir, { recursive: true });
  args.push("--skills-dir", skillsDir);
  await mkdir(memoryDir, { recursive: true });
  args.push("--memory-dir", memoryDir);
  if (kind === "mock") {
    args.push("--mock");
  } else if (kind === "toolhive") {
    // No credential and no base-URL flag for the gateway: mecated finds the
    // loopback proxy through ToolHive's own config and registers it as the
    // "toolhive" provider on its own. Naming it as the default is all it takes.
    args.push("--default-provider", "toolhive");
  } else {
    // Any auth.yaml-configured provider (openrouter, anthropic, openai,
    // opencode, …) or a settings-defined custom provider (ADR 0238) —
    // mecated validates the id fail-fast at startup.
    args.push("--default-provider", kind);
  }
  const env = { ...process.env, MECATL_AUTH_TOKEN: managedAuthToken };
  if (gateway) {
    // Mecatl's SDK opens the optional standalone SSE notification stream after
    // initialization. Some authenticated gateways (including Connector Gateway)
    // close that GET stream and thereby cancel an otherwise valid MCP session.
    // Keep the optional stream on loopback and forward request/response traffic.
    args.push(
      "--mcp-server",
      `${gateway.name}=http://127.0.0.1:8788/mcp-proxy/${mcpProxySecret}/${encodeURIComponent(gateway.name)}`,
    );
  }
  // The lifetime pipe is best-effort: without mkfifo(1) the daemon still
  // starts, it just loses the parent-crash cleanup (SIGINT/SIGTERM reaping
  // below still covers the clean paths).
  let lifetime = null;
  try {
    lifetime = await createLifetimePipe();
    // The FIFO's read end lands at fd 3 in the child (stdio index 3 below).
    args.push("--lifetime-pipe-fd", "3");
  } catch (error) {
    process.stderr.write(
      `[supervisor] lifetime pipe unavailable (${error.message || error}); spawning without parent-crash protection\n`,
    );
  }
  const proc = spawn(binary, args, {
    cwd: mecatlDir,
    env,
    stdio: [
      "ignore",
      "ignore",
      "pipe",
      ...(lifetime ? [lifetime.readEnd.fd] : []),
    ],
  });
  child = proc;
  if (lifetime) {
    // The child owns its duplicate of the read end now; the controller keeps
    // ONLY the write end, open and never written to, until this child exits.
    void lifetime.readEnd.close().catch(() => undefined);
    const writeEnd = lifetime.writeEnd;
    const releaseWriteEnd = () => void writeEnd.close().catch(() => undefined);
    proc.once("exit", releaseWriteEnd);
    proc.once("error", releaseWriteEnd);
  }
  proc.stderr.on("data", (chunk) => {
    const text = chunk.toString();
    startupLog = (startupLog + text).slice(-24_000);
    process.stderr.write(`[mecatl] ${text}`);
  });
  proc.once("exit", () => {
    if (child === proc) child = null;
    if (!expectedExits.has(proc)) scheduleMecatlRestart();
  });
  provider = providerLabel(kind);
  // The ready file replaces the old connect-poll + stability window: it is
  // written atomically and only after composition and every listener are up,
  // so its appearance IS readiness and its http_address arrives resolved.
  let doc;
  try {
    doc = await waitForReadyDoc(proc);
  } catch (error) {
    throw startupFailure(kind, error.message || "mecatl did not become ready");
  }
  mecatlBaseURL = `http://${doc.http_address}`;
  readyInfo = {
    apiMajor: Number(doc.api_major ?? 0),
    features: Array.isArray(doc.features)
      ? doc.features.filter((feature) => typeof feature === "string")
      : [],
    deployment: typeof doc.deployment === "string" ? doc.deployment : "",
  };
  // MCP gateway construction happens during composition, BEFORE the ready
  // file is written — so when the daemon came up degraded rather than dead,
  // the handshake failure is already in the startup log.
  if (gateway && /Unauthorized/i.test(startupLog)) {
    throw new Error(
      "MCP Gateway rejected the bearer token (Unauthorized). Paste a current gateway access token and try again.",
    );
  }
  if (
    gateway &&
    /MCP manager construction failed|no servers could be connected/i.test(
      startupLog,
    )
  ) {
    throw new Error(
      "MCP Gateway could not be initialized. Check that the URL is a Streamable HTTP endpoint and that its credential is valid.",
    );
  }
  restartFailures = 0;
}

const server = http.createServer(async (request, response) => {
  const requestURL = new URL(request.url, "http://127.0.0.1:8788");
  const mcpProxyPrefix = `/mcp-proxy/${mcpProxySecret}/`;
  if (
    !requestIsAllowed(request, requestURL, { allowedOrigins, mcpProxyPrefix })
  ) {
    jsonError(response, 403, "request origin is not allowed");
    return;
  }
  if (requestURL.pathname.startsWith("/mecatl/")) {
    try {
      const body =
        request.method === "GET" || request.method === "HEAD"
          ? undefined
          : await readBody(request);
      const headers = {};
      for (const key of [
        "content-type",
        "accept",
        "mcp-session-id",
        "mcp-protocol-version",
        "last-event-id",
      ]) {
        if (request.headers[key]) headers[key] = request.headers[key];
      }
      const path =
        requestURL.pathname.slice("/mecatl".length) + requestURL.search;
      const upstream = await fetchMecatl(path, {
        method: request.method,
        headers,
        body,
        redirect: "manual",
      });
      response.statusCode = upstream.status;
      for (const key of [
        "content-type",
        "cache-control",
        "mcp-session-id",
        "www-authenticate",
      ]) {
        const value = upstream.headers.get(key);
        if (value) response.setHeader(key, value);
      }
      if (upstream.body)
        for await (const chunk of upstream.body) response.write(chunk);
      response.end();
    } catch (error) {
      jsonError(
        response,
        error.statusCode || 502,
        error.message || "mecated proxy failed",
      );
    }
    return;
  }
  if (requestURL.pathname.startsWith(mcpProxyPrefix)) {
    const proxyName = decodeURIComponent(
      requestURL.pathname.slice(mcpProxyPrefix.length),
    );
    if (!gateway || proxyName !== gateway.name) {
      response.statusCode = 404;
      response.end("gateway not configured");
      return;
    }
    if (request.method === "GET") {
      response.writeHead(200, {
        "Content-Type": "text/event-stream",
        "Cache-Control": "no-cache",
        Connection: "keep-alive",
      });
      response.write(": loopback notification channel\n\n");
      const heartbeat = setInterval(
        () => response.write(": keepalive\n\n"),
        15_000,
      );
      request.once("close", () => clearInterval(heartbeat));
      return;
    }
    if (!["POST", "DELETE"].includes(request.method || "")) {
      response.statusCode = 405;
      response.end("method not allowed");
      return;
    }
    try {
      const requestBody =
        request.method === "POST" ? await readBody(request) : undefined;
      await refreshGatewayAccessToken(false);
      const headers = { Authorization: `Bearer ${gateway.token}` };
      for (const key of [
        "content-type",
        "accept",
        "mcp-session-id",
        "mcp-protocol-version",
        "last-event-id",
      ]) {
        if (request.headers[key]) headers[key] = request.headers[key];
      }
      let upstream = await fetch(gateway.url, {
        method: request.method,
        headers,
        body: requestBody,
      });
      if (upstream.status === 401 && gateway.refreshToken) {
        await upstream.arrayBuffer();
        await refreshGatewayAccessToken(true);
        headers.Authorization = `Bearer ${gateway.token}`;
        upstream = await fetch(gateway.url, {
          method: request.method,
          headers,
          body: requestBody,
        });
      }
      process.stderr.write(
        `[mcp-proxy] ${request.method} ${upstream.status} ${upstream.headers.get("content-type") || ""}\n`,
      );
      response.statusCode = upstream.status;
      for (const key of [
        "content-type",
        "cache-control",
        "mcp-session-id",
        "www-authenticate",
      ]) {
        const value = upstream.headers.get(key);
        if (value) response.setHeader(key, value);
      }
      if (upstream.body) {
        for await (const chunk of upstream.body) response.write(chunk);
      }
      response.end();
    } catch (error) {
      process.stderr.write(
        `[mcp-proxy] ${request.method} failed: ${error.message || error}\n`,
      );
      jsonError(
        response,
        error.statusCode || 502,
        error.message || "gateway proxy failed",
      );
    }
    return;
  }
  response.setHeader("Content-Type", "application/json");
  response.setHeader("Cache-Control", "no-store");
  if (request.method === "GET" && requestURL.pathname === "/mcp/oauth/start") {
    try {
      const name = requestURL.searchParams.get("name") || "";
      const gatewayURL = new URL(requestURL.searchParams.get("url") || "");
      if (!/^[A-Za-z0-9_]+$/.test(name))
        throw new Error(
          "Gateway name may contain only letters, numbers, and underscores",
        );
      if (gatewayURL.protocol !== "https:")
        throw new Error("OAuth gateways must use HTTPS");
      const discovery = await discoverOAuth(gatewayURL);
      const registrationResponse = await fetch(discovery.registrationEndpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          client_name: "Mecatl Studio",
          application_type: "native",
          redirect_uris: [oauthRedirectUri],
          grant_types: ["authorization_code", "refresh_token"],
          response_types: ["code"],
          token_endpoint_auth_method: "none",
        }),
        signal: AbortSignal.timeout(10_000),
      });
      const registration = await registrationResponse.json().catch(() => ({}));
      if (!registrationResponse.ok)
        throw new Error(
          registration.error_description ||
            registration.error ||
            "Gateway refused OAuth client registration",
        );
      if (!registration.client_id)
        throw new Error("Gateway registration did not return a client ID");
      const state = randomBytes(24).toString("base64url");
      const verifier = randomBytes(48).toString("base64url");
      const challenge = createHash("sha256")
        .update(verifier)
        .digest("base64url");
      const scope = registration.scope || discovery.scope;
      oauthAttempts.set(state, {
        name,
        url: gatewayURL.toString(),
        clientId: registration.client_id,
        tokenEndpoint: discovery.tokenEndpoint,
        scope,
        resource: discovery.resource,
        verifier,
        expires: Date.now() + 10 * 60_000,
      });
      const authorizationURL = new URL(discovery.authorizationEndpoint);
      authorizationURL.searchParams.set("response_type", "code");
      authorizationURL.searchParams.set("client_id", registration.client_id);
      authorizationURL.searchParams.set("redirect_uri", oauthRedirectUri);
      authorizationURL.searchParams.set("scope", scope);
      if (discovery.resource)
        authorizationURL.searchParams.set("resource", discovery.resource);
      authorizationURL.searchParams.set("state", state);
      authorizationURL.searchParams.set("code_challenge", challenge);
      authorizationURL.searchParams.set("code_challenge_method", "S256");
      if (requestURL.searchParams.get("redirect") === "1") {
        response.writeHead(302, { Location: authorizationURL.toString() });
        response.end();
      } else {
        response.end(
          JSON.stringify({ authorizationUrl: authorizationURL.toString() }),
        );
      }
    } catch (error) {
      response.statusCode = 400;
      response.end(
        JSON.stringify({
          error: error.message || "Could not start gateway sign-in",
        }),
      );
    }
    return;
  }
  if (request.method === "GET" && requestURL.pathname === "/oauth/callback") {
    response.setHeader("Content-Type", "text/html; charset=utf-8");
    const state = requestURL.searchParams.get("state") || "";
    const attempt = oauthAttempts.get(state);
    oauthAttempts.delete(state);
    try {
      if (requestURL.searchParams.get("error"))
        throw new Error(
          requestURL.searchParams.get("error_description") ||
            requestURL.searchParams.get("error"),
        );
      if (!attempt || attempt.expires < Date.now())
        throw new Error(
          "The gateway sign-in attempt expired. Start it again from Mecatl Studio.",
        );
      const code = requestURL.searchParams.get("code");
      if (!code)
        throw new Error("Gateway sign-in did not return an authorization code");
      const tokenBody = new URLSearchParams({
        grant_type: "authorization_code",
        code,
        client_id: attempt.clientId,
        redirect_uri: oauthRedirectUri,
        code_verifier: attempt.verifier,
      });
      if (attempt.resource) tokenBody.set("resource", attempt.resource);
      const tokenResponse = await fetch(attempt.tokenEndpoint, {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
        body: tokenBody,
        signal: AbortSignal.timeout(15_000),
      });
      const tokenResult = await tokenResponse.json().catch(() => ({}));
      if (!tokenResponse.ok || !tokenResult.access_token)
        throw new Error(
          tokenResult.error_description ||
            tokenResult.error ||
            "Gateway token exchange failed",
        );
      await queueRestart(async () => {
        const previousGateway = gateway;
        gateway = {
          name: attempt.name,
          url: attempt.url,
          token: tokenResult.access_token,
          refreshToken: tokenResult.refresh_token || "",
          clientId: attempt.clientId,
          tokenEndpoint: attempt.tokenEndpoint,
          scope: attempt.scope,
          resource: attempt.resource,
          expiresAt:
            Date.now() +
            Math.max(60, Number(tokenResult.expires_in) || 300) * 1000,
        };
        try {
          await startMecatl(preferredKind());
        } catch (error) {
          gateway = previousGateway;
          await startMecatl(preferredKind());
          throw error;
        }
      });
      response.end(
        `<!doctype html><title>Mecatl Gateway Connected</title><style>body{font:16px system-ui;padding:40px;color:#25231f}</style><h1>Gateway connected</h1><p>You can close this window.</p><script>window.opener?.postMessage({type:"mecatl-mcp-oauth",ok:true},${JSON.stringify(studioPublicOrigin)});setTimeout(()=>window.close(),700)</script>`,
      );
    } catch (error) {
      const message = String(error.message || "Gateway sign-in failed").replace(
        /[<>&"']/g,
        "",
      );
      process.stderr.write(`[oauth] callback failed: ${message}\n`);
      response.statusCode = 400;
      response.end(
        `<!doctype html><title>Mecatl Gateway Error</title><style>body{font:16px system-ui;padding:40px;color:#25231f}</style><h1>Could not connect</h1><p>${message}</p><script>window.opener?.postMessage({type:"mecatl-mcp-oauth",ok:false,error:${JSON.stringify(message)}},${JSON.stringify(studioPublicOrigin)})</script>`,
      );
    }
    return;
  }
  if (
    request.method === "POST" &&
    requestURL.pathname === "/mcp/oauth/refresh"
  ) {
    try {
      if (!gateway?.refreshToken)
        throw new Error("Gateway does not have a refresh token; sign in again");
      await refreshGatewayAccessToken(true);
      response.end(JSON.stringify({ ok: true }));
    } catch (error) {
      response.statusCode = 400;
      response.end(
        JSON.stringify({
          error: error.message || "Gateway token refresh failed",
        }),
      );
    }
    return;
  }
  // Restart mecated with the CURRENT provider, gateway and router config. The
  // daemon resolves skills and agent definitions once at startup (ListSkills is
  // a pure snapshot read), so a newly authored SKILL.md only reaches the model
  // after a restart. Provider credentials remain owned by mecated's auth file.
  if (request.method === "POST" && requestURL.pathname === "/restart") {
    try {
      await queueRestart(async () => {
        await startMecatl(preferredKind());
      });
      response.end(JSON.stringify({ ok: true, provider }));
    } catch (error) {
      response.statusCode = 400;
      response.end(
        JSON.stringify({ error: error.message || "Could not restart mecated" }),
      );
    }
    return;
  }
  if (request.method === "GET" && requestURL.pathname === "/status") {
    const configuredProviders = await listSelectableProviderNames();
    response.end(
      JSON.stringify({
        mode: "managed",
        provider,
        isMock: provider === "offline mock",
        running: Boolean(child),
        startupError,
        authFile,
        // Where a custom `providers:` block lands (ADR 0238): the imported
        // operator-settings.yaml when one is active (a CLI-tier file wins
        // that section whole-block), otherwise the user-global settings.
        settingsFile: operatorSettingsActive
          ? operatorSettingsFile
          : userSettingsFile,
        // The child's ready-file compatibility descriptor (never a
        // credential): api_major, feature identifiers, and the operator's
        // deployment label, verbatim from mecated-ready/1.
        apiMajor: readyInfo?.apiMajor ?? null,
        features: readyInfo?.features ?? [],
        deployment: readyInfo?.deployment ?? "",
        // Names only — never values. What MECATL_STUDIO_PROVIDER / the
        // /providers/active switch may select among (auth.yaml blocks plus
        // settings-defined custom providers), and which one is active right
        // now, if any.
        configuredProviders,
        selectedProvider: activeProviderOverride,
        // The client has no other way to learn this: it is resolved from THIS
        // file's location, so a clone anywhere works with no source edit.
        workspace,
        gateway: gateway ? { name: gateway.name, url: gateway.url } : null,
        toolhiveGateway: {
          available: toolhiveReady,
          baseURL: toolhiveGatewayURL,
          active: provider === "ToolHive LLM gateway",
        },
        modelRouter: modelRouterConfig
          ? {
              enabled: modelRouterConfig.enabled,
              categories: modelRouterConfig.categories.length,
            }
          : null,
        operatorSettings: operatorSettingsActive,
        skills: { dir: skillsDir, scope: "project" },
        memory: { dir: memoryDir, scope: "project" },
      }),
    );
    return;
  }
  // Provider inventory: names + key-present booleans from auth.yaml, plus the
  // settings-defined custom providers (ADR 0238) — a keyless custom provider
  // has no auth.yaml block, so the auth scan alone would hide it. Never
  // values. Like the skill routes (and unlike /status) this is NOT in the
  // header-free read-only allowlist — it needs the server-set studio header.
  if (request.method === "GET" && requestURL.pathname === "/providers") {
    const authRows = listAuthFileProviders(await readAuthFileText());
    const custom = await listCustomSettingsProviders();
    const customNames = new Set(custom.map((definition) => definition.name));
    const providers = authRows
      .filter((row) => !customNames.has(row.name))
      .map((row) => ({
        name: row.name,
        configured: true,
        keyPresent: row.keyPresent,
        source: "auth.yaml",
        testable: Object.hasOwn(providerKeyProbes, row.name),
      }));
    for (const definition of custom) {
      const keyed = definition.authMethod === "api_key";
      const authRow = authRows.find((row) => row.name === definition.name);
      providers.push({
        name: definition.name,
        configured: true,
        // A keyless (auth.method: none) provider needs no credential — its
        // requirement is satisfied by configuration alone.
        keyPresent: keyed ? Boolean(authRow?.keyPresent) : true,
        source:
          keyed && authRow ? "settings.yaml + auth.yaml" : "settings.yaml",
        testable:
          keyed &&
          customProviderProbeURL(definition.apiFlavor, definition.baseURL) !==
            "",
      });
    }
    response.end(JSON.stringify({ providers }));
    return;
  }
  // The provider kinds the daemon understands, with the guided-add snippet
  // (a <YOUR_KEY> placeholder — this route never sees a real credential).
  if (request.method === "GET" && requestURL.pathname === "/providers/known") {
    response.end(JSON.stringify({ known: KNOWN_AUTH_PROVIDERS }));
    return;
  }
  // Live provider switch: POST /providers/active { kind }. Unlike
  // MECATL_STUDIO_PROVIDER (fixed for the process's lifetime), this
  // reassigns activeProviderOverride and restarts mecated on the spot — the
  // Studio settings UI's "switch to mock" / "switch to <provider>" control.
  // Checked BEFORE the /providers/{name} regex below, which would otherwise
  // match "active" as a provider name and 404 it first.
  if (
    request.method === "POST" &&
    requestURL.pathname === "/providers/active"
  ) {
    try {
      if (
        !String(request.headers["content-type"] || "")
          .toLowerCase()
          .startsWith("application/json")
      )
        throw Object.assign(
          new Error("Content-Type must be application/json"),
          { statusCode: 415 },
        );
      const input = JSON.parse(
        (await readBody(request, 4096)).toString("utf8"),
      );
      const kind =
        typeof input?.kind === "string" ? input.kind.trim().toLowerCase() : "";
      const selectableNames = await listSelectableProviderNames();
      if (!kind || !isSelectableProviderKind(kind, selectableNames)) {
        throw Object.assign(
          new Error(
            kind === "toolhive"
              ? "The ToolHive LLM gateway is not reachable right now"
              : `"${kind || "(empty)"}" is not mock, toolhive, a provider configured in ${authFile}, or a custom provider defined in the operator settings`,
          ),
          { statusCode: 400 },
        );
      }
      await queueRestart(async () => {
        const previous = activeProviderOverride;
        activeProviderOverride = kind;
        try {
          await startMecatl(preferredKind());
        } catch (error) {
          activeProviderOverride = previous;
          await startMecatl(preferredKind());
          throw error;
        }
      });
      response.end(
        JSON.stringify({
          ok: true,
          provider,
          selectedProvider: activeProviderOverride,
        }),
      );
    } catch (error) {
      jsonError(
        response,
        error.statusCode || 400,
        error.message || "Could not switch provider",
      );
    }
    return;
  }
  // Key test + removal: POST /providers/{name}/test, DELETE /providers/{name}.
  const providerRoute = requestURL.pathname.match(
    /^\/providers\/([^/]+?)(?:\/(test))?$/,
  );
  if (providerRoute) {
    const [, rawName, action] = providerRoute;
    let name = rawName;
    try {
      name = decodeURIComponent(rawName);
    } catch {
      // Malformed escape: the grammar below rejects percent-shaped names.
    }
    if (!validProviderName(name)) {
      jsonError(response, 400, "not a valid provider name");
      return;
    }
    if (request.method === "POST" && action === "test") {
      // ONE cheap authenticated call with the STORED key, made entirely
      // server-side. The key never appears in the response, the logs, or an
      // error message; the probe is bounded (10s) and never follows a
      // redirect with the credential attached. A settings-defined custom
      // provider (ADR 0238) is probed against ITS OWN base URL with the
      // header shape its api_flavor dictates.
      const definition = (await listCustomSettingsProviders()).find(
        (candidate) => candidate.name === name,
      );
      let buildProbe = null;
      if (definition) {
        if (definition.authMethod !== "api_key") {
          jsonError(
            response,
            400,
            `"${name}" uses auth.method none — there is no key to test.`,
          );
          return;
        }
        if (
          customProviderProbeURL(definition.apiFlavor, definition.baseURL) ===
          ""
        ) {
          jsonError(
            response,
            400,
            `Key testing is not supported for "${name}" — its api_flavor or base_url cannot be probed; mecated will report an auth problem on first use instead.`,
          );
          return;
        }
        buildProbe = (key) => customProviderKeyProbe(definition, key);
      } else if (Object.hasOwn(providerKeyProbes, name)) {
        buildProbe = providerKeyProbes[name];
      } else {
        jsonError(
          response,
          400,
          `Key testing is not supported for "${name}" — mecated will report an auth problem on first use instead.`,
        );
        return;
      }
      const key = readProviderCredential(await readAuthFileText(), name);
      if (!key) {
        jsonError(
          response,
          400,
          `No api_key found for providers.${name} in ${authFile}`,
        );
        return;
      }
      response.end(JSON.stringify(await probeProviderKey(buildProbe(key))));
      return;
    }
    if (request.method === "DELETE" && !action) {
      // Removing a provider block is a conservative line-range cut of the
      // named top-level key (provider-auth.mjs), written temp-file+rename
      // with owner-only permissions, then a daemon restart so the change is
      // real. Removing the LAST provider is allowed: mecated runs on the
      // offline mock without providers (the state the user already sees on
      // first run) — the UI's confirm warns, the controller doesn't refuse.
      // If the restart then fails (e.g. MECATL_STUDIO_PROVIDER still names
      // the removed provider), the removal STANDS — the operator asked for
      // the credential to be gone — and the startup error surfaces both in
      // this response and on /status.startupError.
      try {
        await queueRestart(async () => {
          const current = await readAuthFileText();
          const { text, removed } = removeAuthFileProvider(current, name);
          if (!removed) {
            // A settings-defined provider has no auth.yaml block to cut —
            // its definition lives in an operator-owned settings file the
            // controller never edits.
            const settingsDefined = (await listCustomSettingsProviders()).some(
              (definition) => definition.name === name,
            );
            throw Object.assign(
              new Error(
                settingsDefined
                  ? `"${name}" is defined in the operator settings (providers: section), not ${authFile} — remove it from the settings file, then restart the daemon.`
                  : `No provider named "${name}" in ${authFile}`,
              ),
              { statusCode: settingsDefined ? 409 : 404 },
            );
          }
          const temp = `${authFile}.tmp`;
          await writeFile(temp, text, { mode: 0o600 });
          await rename(temp, authFile);
          await startMecatl(preferredKind());
        });
        response.end(JSON.stringify({ ok: true, restarted: true }));
      } catch (error) {
        jsonError(
          response,
          error.statusCode || 400,
          error.message || "Provider removal failed",
        );
      }
      return;
    }
    response.statusCode = 404;
    response.end(JSON.stringify({ error: "not found" }));
    return;
  }
  // The disabled-skill inventory. Like every controller route this sits
  // behind requestIsAllowed (loopback Host + allowlisted Origin + the
  // server-set studio header) — the Next server proxy is the only caller.
  if (request.method === "GET" && requestURL.pathname === "/skills/disabled") {
    response.end(JSON.stringify({ disabled: await listDisabledSkills() }));
    return;
  }
  // Skill creation: POST /skills with { name, body } → <skillsDir>/<name>/
  // SKILL.md. A brand-new skill is invisible until the daemon rebuilds its
  // startup snapshot, so creation always restarts mecated — serialized through
  // queueRestart like every sibling mutation (existence probes included, so a
  // queued restart cannot race them).
  if (request.method === "POST" && requestURL.pathname === "/skills") {
    try {
      if (
        !String(request.headers["content-type"] || "")
          .toLowerCase()
          .startsWith("application/json")
      )
        throw skillClientError("Content-Type must be application/json", 415);
      const input = JSON.parse(
        (await readBody(request, maxSkillCreateBodyBytes)).toString("utf8"),
      );
      if (typeof input?.name !== "string")
        throw skillClientError("Provide the skill name as { name }");
      const name = input.name;
      const paths = skillPaths(name);
      // Two accepted shapes: { name, body } writes a lone SKILL.md; { name,
      // files: [{ path, contentBase64 }] } writes a whole folder skill (a
      // zip/folder upload). Either way a SKILL.md must land at the root.
      let files;
      if (Array.isArray(input?.files)) {
        if (
          input.files.length === 0 ||
          input.files.length > maxSkillUploadFiles
        )
          throw skillClientError(
            `Provide between 1 and ${maxSkillUploadFiles} files`,
          );
        let total = 0;
        const seen = new Set();
        files = input.files.map((entry) => {
          if (
            !validSkillUploadPath(entry?.path) ||
            typeof entry?.contentBase64 !== "string"
          )
            throw skillClientError(
              "Each file needs a safe relative { path } and { contentBase64 }",
            );
          // The write below is on the default (case-insensitive) macOS
          // filesystem — case-colliding paths would silently overwrite.
          const key = entry.path.toLowerCase();
          if (seen.has(key))
            throw skillClientError(
              `The upload holds duplicate paths: ${entry.path}`,
            );
          seen.add(key);
          const content = Buffer.from(entry.contentBase64, "base64");
          if (content.length > maxSkillUploadFileBytes)
            throw skillClientError(
              `"${entry.path}" exceeds the ${maxSkillUploadFileBytes}-byte per-file limit`,
              413,
            );
          total += content.length;
          return { path: entry.path, content };
        });
        if (total > maxSkillUploadTotalBytes)
          throw skillClientError(
            `The upload exceeds the ${maxSkillUploadTotalBytes}-byte total limit`,
            413,
          );
        const skillMd = files.find((file) => file.path === "SKILL.md");
        if (!skillMd)
          throw skillClientError(
            "The upload needs a SKILL.md at the folder root",
          );
        if (skillMd.content.length > maxSkillBodyBytes)
          throw skillClientError(
            `SKILL.md is limited to ${maxSkillBodyBytes} bytes`,
            413,
          );
      } else {
        if (typeof input?.body !== "string" || input.body.length === 0)
          throw skillClientError("Provide the SKILL.md content as { body }");
        if (Buffer.byteLength(input.body, "utf8") > maxSkillBodyBytes)
          throw skillClientError(
            `SKILL.md is limited to ${maxSkillBodyBytes} bytes`,
            413,
          );
        files = [{ path: "SKILL.md", content: Buffer.from(input.body) }];
      }
      let restarted = false;
      await queueRestart(async () => {
        // A name taken on EITHER side is a collision: a same-named disabled
        // skill would silently resurrect over this content when enabled.
        if (
          (await isDirectory(paths.enabled)) ||
          (await isDirectory(paths.disabled))
        )
          throw skillClientError(
            `A skill named "${name}" already exists under ${skillsDir}`,
            409,
          );
        try {
          for (const file of files) {
            const target = resolve(paths.enabled, file.path);
            // Defense-in-depth behind validSkillUploadPath.
            if (!target.startsWith(paths.enabled + sep))
              throw skillClientError(
                `Path escapes the skill folder: ${file.path}`,
              );
            await mkdir(dirname(target), { recursive: true });
            await writeFile(target, file.content);
          }
        } catch (error) {
          // The collision check above proved the dir was ours to create, so
          // a half-written skill is safe to sweep away whole.
          await rm(paths.enabled, { recursive: true, force: true });
          throw error;
        }
        restarted = true;
        await startMecatl(preferredKind());
      });
      response.end(JSON.stringify({ ok: true, restarted }));
    } catch (error) {
      jsonError(
        response,
        error.statusCode || 400,
        error.message || "Skill create failed",
      );
    }
    return;
  }
  // Skill CRUD: GET/PUT /skills/{name}/body, POST /skills/{name}/{enable|
  // disable}, DELETE /skills/{name}. Mutations serialize through queueRestart
  // (checks included, so a queued restart cannot race the side probes) and
  // restart mecated whenever the change touches what its startup snapshot saw.
  const skillRoute = requestURL.pathname.match(
    /^\/skills\/([^/]+?)(?:\/(body|enable|disable|files|file))?$/,
  );
  if (skillRoute) {
    const [, rawName, action] = skillRoute;
    let name = rawName;
    try {
      try {
        name = decodeURIComponent(rawName);
      } catch {
        // Malformed escape: the raw segment is the only candidate, and the
        // name grammar below rejects anything percent-shaped anyway.
      }
      const paths = skillPaths(name);
      if (request.method === "GET" && action === "body") {
        for (const side of [paths.enabled, paths.disabled]) {
          let body;
          try {
            body = await readFile(resolve(side, "SKILL.md"), "utf8");
          } catch {
            continue; // try the other side
          }
          response.end(JSON.stringify({ body }));
          return;
        }
        throw skillClientError(`No skill named "${name}" has a SKILL.md`, 404);
      }
      // Read-only folder views: a skill can be a whole folder of assets
      // (scripts/, references/, …), not just a SKILL.md. Listing and preview
      // work on whichever side (enabled/disabled) holds the skill.
      if (request.method === "GET" && action === "files") {
        for (const side of [paths.enabled, paths.disabled]) {
          if (!(await isDirectory(side))) continue;
          response.end(JSON.stringify({ files: await listSkillFiles(side) }));
          return;
        }
        throw skillClientError(`No skill named "${name}"`, 404);
      }
      if (request.method === "GET" && action === "file") {
        const relPath = requestURL.searchParams.get("path") || "";
        const segments = relPath.split("/");
        if (
          !relPath ||
          relPath.includes("\\") ||
          segments.some(
            (segment) => segment === "" || segment === "." || segment === "..",
          )
        )
          throw skillClientError(
            "Provide a relative file path inside the skill as ?path=",
          );
        for (const side of [paths.enabled, paths.disabled]) {
          if (!(await isDirectory(side))) continue;
          const target = resolve(side, relPath);
          if (!target.startsWith(side + sep))
            throw skillClientError("Path escapes the skill folder");
          let meta;
          try {
            meta = await lstat(target);
          } catch {
            throw skillClientError(`No file "${relPath}" in "${name}"`, 404);
          }
          if (!meta.isFile())
            throw skillClientError(`"${relPath}" is not a regular file`);
          if (meta.size > maxSkillBodyBytes)
            throw skillClientError(
              `"${relPath}" is too large to preview (limit ${maxSkillBodyBytes} bytes)`,
              413,
            );
          const bytes = await readFile(target);
          if (bytes.includes(0))
            throw skillClientError(
              `"${relPath}" is a binary file — no text preview`,
              415,
            );
          response.end(JSON.stringify({ content: bytes.toString("utf8") }));
          return;
        }
        throw skillClientError(`No skill named "${name}"`, 404);
      }
      if (request.method === "PUT" && action === "body") {
        if (
          !String(request.headers["content-type"] || "")
            .toLowerCase()
            .startsWith("application/json")
        )
          throw skillClientError("Content-Type must be application/json", 415);
        const input = JSON.parse(
          (await readBody(request, maxSkillBodyBytes + 16_384)).toString(
            "utf8",
          ),
        );
        if (typeof input?.body !== "string" || input.body.length === 0)
          throw skillClientError("Provide the SKILL.md content as { body }");
        if (Buffer.byteLength(input.body, "utf8") > maxSkillBodyBytes)
          throw skillClientError(
            `SKILL.md is limited to ${maxSkillBodyBytes} bytes`,
            413,
          );
        let restarted = false;
        await queueRestart(async () => {
          const onEnabled = await isDirectory(paths.enabled);
          const onDisabled = await isDirectory(paths.disabled);
          if (!onEnabled && !onDisabled)
            throw skillClientError(`No skill named "${name}"`, 404);
          const target = resolve(
            onEnabled ? paths.enabled : paths.disabled,
            "SKILL.md",
          );
          const temp = `${target}.tmp`;
          await writeFile(temp, input.body);
          await rename(temp, target);
          // A disabled skill is invisible to the daemon's snapshot, so
          // editing it owes no restart.
          if (onEnabled) {
            restarted = true;
            await startMecatl(preferredKind());
          }
        });
        response.end(JSON.stringify({ ok: true, restarted }));
        return;
      }
      if (
        request.method === "POST" &&
        (action === "enable" || action === "disable")
      ) {
        let restarted = false;
        await queueRestart(async () => {
          const onEnabled = await isDirectory(paths.enabled);
          const onDisabled = await isDirectory(paths.disabled);
          if (!onEnabled && !onDisabled)
            throw skillClientError(`No skill named "${name}"`, 404);
          if (onEnabled && onDisabled)
            throw skillClientError(
              `Both an enabled and a disabled "${name}" exist under ${skillsDir}; resolve the collision on disk first`,
              409,
            );
          // Idempotent-ish: already on the requested side moves nothing and
          // restarts nothing.
          if (action === "disable" ? onDisabled : onEnabled) return;
          if (action === "disable") {
            await mkdir(disabledSkillsDir, { recursive: true });
            await rename(paths.enabled, paths.disabled);
          } else {
            await rename(paths.disabled, paths.enabled);
          }
          restarted = true;
          await startMecatl(preferredKind());
        });
        response.end(JSON.stringify({ ok: true, restarted }));
        return;
      }
      if (request.method === "DELETE" && !action) {
        let restarted = false;
        await queueRestart(async () => {
          const onEnabled = await isDirectory(paths.enabled);
          const onDisabled = await isDirectory(paths.disabled);
          if (!onEnabled && !onDisabled)
            throw skillClientError(`No skill named "${name}"`, 404);
          // A delete means gone from BOTH sides — never a hidden disabled
          // copy waiting to resurrect under the same name.
          if (onDisabled)
            await rm(paths.disabled, { recursive: true, force: true });
          if (onEnabled) {
            await rm(paths.enabled, { recursive: true, force: true });
            restarted = true;
            await startMecatl(preferredKind());
          }
        });
        response.end(JSON.stringify({ ok: true, restarted }));
        return;
      }
      response.statusCode = 404;
      response.end(JSON.stringify({ error: "not found" }));
    } catch (error) {
      jsonError(
        response,
        error.statusCode || 400,
        error.message || "Skill update failed",
      );
    }
    return;
  }
  if (request.method === "GET" && requestURL.pathname === "/model-router") {
    response.end(
      JSON.stringify({
        config: modelRouterConfig,
        managedBy: operatorSettingsActive ? "operator-settings" : "studio",
      }),
    );
    return;
  }
  if (
    request.method !== "POST" ||
    !["/mcp", "/model-router"].includes(requestURL.pathname)
  ) {
    response.statusCode = 404;
    response.end(JSON.stringify({ error: "not found" }));
    return;
  }
  try {
    if (
      !String(request.headers["content-type"] || "")
        .toLowerCase()
        .startsWith("application/json")
    ) {
      throw Object.assign(new Error("Content-Type must be application/json"), {
        statusCode: 415,
      });
    }
    const input = JSON.parse(
      (await readBody(request, 16_384)).toString("utf8"),
    );
    if (requestURL.pathname === "/model-router") {
      if (operatorSettingsActive)
        throw new Error(
          "Routing is managed by the imported operator settings. Update the complete settings file to preserve its aliases, slots, and guardrails.",
        );
      const nextConfig = normalizeModelRouter(input);
      await queueRestart(async () => {
        const previousConfig = modelRouterConfig;
        modelRouterConfig = nextConfig;
        await persistModelRouter(nextConfig);
        try {
          await startMecatl(preferredKind());
        } catch (error) {
          modelRouterConfig = previousConfig;
          if (previousConfig) await persistModelRouter(previousConfig);
          else
            await Promise.all([
              rm(routerSettingsFile, { force: true }),
              rm(routerStateFile, { force: true }),
            ]);
          await startMecatl(preferredKind());
          throw error;
        }
      });
      response.end(JSON.stringify({ ok: true, config: modelRouterConfig }));
      return;
    }
    if (!/^[A-Za-z0-9_]+$/.test(input.name || ""))
      throw new Error(
        "Gateway name may contain only letters, numbers, and underscores",
      );
    const parsed = validateGatewayURL(input.url, {
      allowLoopbackHTTP: process.env.MECATL_ALLOW_INSECURE_LOOPBACK_MCP === "1",
    });
    const token =
      typeof input.token === "string"
        ? input.token.trim().replace(/^Bearer\s+/i, "")
        : "";
    const candidate = { name: input.name, url: parsed.toString(), token };
    await queueRestart(async () => {
      const previousGateway = gateway;
      gateway = candidate;
      try {
        await startMecatl(preferredKind());
      } catch (error) {
        // Keep the provider usable and keep rejected gateway credentials out of
        // controller state. The caller still receives the original handshake error.
        gateway = previousGateway;
        await startMecatl(preferredKind());
        throw error;
      }
    });
    response.end(
      JSON.stringify({
        ok: true,
        gateway: { name: gateway.name, url: gateway.url },
      }),
    );
  } catch (error) {
    jsonError(
      response,
      error.statusCode || 400,
      error.message || "Could not update the Mecatl controller",
    );
  }
});

server.listen(8788, "127.0.0.1", async () => {
  process.stdout.write("Mecatl local controller: http://127.0.0.1:8788\n");
  operatorSettingsActive = await hasOperatorSettings();
  modelRouterConfig = await loadModelRouter();
  toolhiveReady = await detectToolhiveGateway();
  process.stdout.write(
    toolhiveReady
      ? `ToolHive LLM gateway detected at ${toolhiveGatewayURL}\n`
      : `ToolHive LLM gateway not reachable at ${toolhiveGatewayURL} (start it with "thv llm proxy start"); falling back to the offline mock\n`,
  );
  try {
    await startMecatl(preferredKind());
  } catch (error) {
    startupError ||= error.message || "mecated could not start";
    process.stderr.write(`${startupError}\n`);
  }
});

// Clean-exit reaping. The CRASH path needs none of this: the lifetime pipe's
// write end dies with this process — SIGKILL included — and mecated reads EOF
// and shuts itself down, so a controller crash can no longer orphan a daemon.
for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, async () => {
    shuttingDown = true;
    if (restartTimer) clearTimeout(restartTimer);
    await stopChild();
    server.close(() => process.exit(0));
  });
}
