import { execFile, spawn } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { createReadStream } from "node:fs";
import {
  appendFile,
  lstat,
  mkdir,
  open,
  readdir,
  readFile,
  realpath,
  rename,
  rm,
  stat,
  writeFile,
} from "node:fs/promises";
import http from "node:http";
import { homedir } from "node:os";
import { basename, dirname, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import {
  diagnosticsOptionArgs,
  diagnosticsRestartRequired,
  mergeDiagnosticsOptions,
  normalizeDiagnosticsOptions,
  productMetricsEnvOptOut,
} from "../src/lib/controller-diagnostics-options.mjs";
import {
  createLogRing,
  LOG_FILE_NAME,
  MAX_LOG_FILE_BYTES,
  parseTailLines,
  ROTATED_LOG_FILE_NAME,
  shouldRotate,
  tailIsTruncated,
} from "../src/lib/controller-log.mjs";
import {
  freeLoopbackPort,
  isAddressInUse,
  PERF_PROXY_TIMEOUT_MS,
  perfProxyRoute,
  perfStatus,
} from "../src/lib/controller-perf.mjs";
import {
  normalizePermissions,
  permissionArgs,
} from "../src/lib/controller-permissions.mjs";
import {
  requestIsAllowed,
  validateGatewayURL,
  validSkillName,
} from "../src/lib/controller-security.mjs";
import {
  hasProjectAuthority,
  trustAnchor,
  trustDecision,
} from "../src/lib/controller-trust.mjs";
import {
  DAEMON_DEFAULTS_EMPTY,
  daemonDefaultArgs,
  normalizeDaemonDefaults,
} from "../src/lib/daemon-defaults.mjs";
import {
  DEFAULT_COMMAND_DIRS,
  DEFAULT_DAEMON_OPTIONS,
  daemonOptionArgs,
  daemonOptionDirFields,
  effectiveDaemonDirs,
  normalizeDaemonOptions,
  optionDirScope,
  optionDirWithinRoots,
  resolveOptionDir,
} from "../src/lib/daemon-options.mjs";
import {
  CUSTOM_PROVIDER_API_FLAVORS,
  customProviderProbeURL,
  describeProviderRow,
  describeToolhiveRow,
  KNOWN_AUTH_PROVIDERS,
  listAuthFileProviders,
  listSettingsProviders,
  PROVIDER_ENV_KEYS,
  removeAuthFileProvider,
  removeAuthFileProviderKey,
  removeSettingsProvider,
  upsertSettingsProvider,
  validCustomProviderBaseURL,
  validCustomProviderId,
  validProviderName,
} from "../src/lib/provider-auth.mjs";
import {
  normalizeRetentionSettings,
  retentionArgs,
} from "../src/lib/retention-settings.mjs";
import {
  DEFAULT_RUNTIME_SETTINGS,
  effectiveRuntimeSettings,
  NO_INHERITED_SETTINGS,
  normalizeRuntimeSettings,
  readLearningBlock,
  readSteerScalar,
  renderRuntimeSettingsYAML,
  runtimeSettingsArgs,
  runtimeSettingsHasYAML,
  SOUL_MAX_BYTES,
  soulFileWithinRoots,
} from "../src/lib/runtime-settings.mjs";
import {
  normalizeStorageSettings,
  storageDefaultsFromEnv,
  storageStatus,
} from "../src/lib/storage-settings.mjs";
import {
  memoryDirFor,
  normalizeWorkspaceConfig,
  skillsDirFor,
  storageArgsFor,
  storeDirFor,
  validateWorkspacePath,
  WORKSPACE_STATE_FILE,
} from "../src/lib/workspace-config.mjs";

const here = dirname(fileURLToPath(import.meta.url));
// Studio is a module INSIDE the mecatl monorepo, so the harness it drives is
// the repo root one directory up — the workspace it edits, the source of
// `bin/mecated` (built by `task build`), and the cwd every run inherits.
const mecatlDir = resolve(here, "../..");
const binary = resolve(mecatlDir, "bin/mecated");
// The DEFAULT workspace root. The LIVE root below is reassignable at runtime
// through POST /workspace (Settings → Workspace) — the web analogue of the
// TUI's `--workspace` deployment choice — and applyWorkspace re-derives
// every root-dependent directory, so the existing readers of these
// bindings need no per-site change. A saved root lives in
// studio-workspace.json; the default writes no file.
const defaultWorkspace = mecatlDir;
let workspace = defaultWorkspace;
const studioStateDir = resolve(here, "../.scratch");
const workspaceStateFile = resolve(studioStateDir, WORKSPACE_STATE_FILE);
const operatorSettingsFile = resolve(studioStateDir, "operator-settings.yaml");
const routerSettingsFile = resolve(
  studioStateDir,
  "model-router-settings.yaml",
);
const routerStateFile = resolve(studioStateDir, "model-router.json");
// The Studio-owned RUNTIME SETTINGS (learning mode/sensitivity, the steer
// opt-out, the soul flags; src/lib/runtime-settings.mjs). Steer and soul are
// spawn flags; learning has no flag, so it is a second CLI-tier
// --permission-config file carrying the FULL merged `learning:` block
// (mecated captures that section whole-block, first file wins — a partial
// block would silently drop the operator's skills/automatic settings). The
// user-global settings.yaml is never edited.
const runtimeSettingsFile = resolve(studioStateDir, "runtime-settings.yaml");
const runtimeStateFile = resolve(studioStateDir, "runtime-settings.json");
// The Studio-owned permissions state (operator posture, project trust,
// shell-less mode). It becomes mecated CLI flags on every spawn — never a
// settings.yaml key — so an imported operator-settings.yaml is never
// rewritten by this controller.
const permissionsStateFile = resolve(studioStateDir, "permissions.json");
// The Studio-owned session-store setting (durable dir or in-memory). Like
// permissions it becomes a spawn flag — `--store-dir <abs>` or no flag at all
// — never a settings.yaml key; see src/lib/storage-settings.mjs.
const storageStateFile = resolve(studioStateDir, "storage-settings.json");
// The Studio-owned retention document (per-family age/count limits, sweep
// cadence, main-deletion acknowledgement). Spawn flags that out-rank a
// settings file per field — never a settings.yaml `retention:` key; see
// src/lib/retention-settings.mjs.
const retentionStateFile = resolve(studioStateDir, "retention.json");
// The Studio-owned DAEMON DEFAULTS (default/subagent model per provider,
// reasoning effort, context window, prompt caching, base-URL overrides, the
// ToolHive LLM gateway, aliases/slots, the credentials-file path) plus the
// durable active-provider choice. Every value becomes a mecated spawn flag
// (src/lib/daemon-defaults.mjs) — never a settings.yaml key — so the CLI
// tier wins even under an imported operator-settings.yaml.
const daemonDefaultsFile = resolve(studioStateDir, "daemon-defaults.json");
// The Studio-owned DIAGNOSTICS OPTIONS (log level, the loopback
// admin/metrics listener + perf MCP + goroutine alarm, product-metrics
// opt-out) plus the controller-side `quiet` switch. Spawn flags again
// (src/lib/controller-diagnostics-options.mjs) — never a settings.yaml key.
// The operator POSTURE is NOT here: it is the permissions document's.
const diagnosticsOptionsFile = resolve(
  studioStateDir,
  "diagnostics-options.json",
);
// The Studio-owned DAEMON OPTIONS (src/lib/daemon-options.mjs): the
// per-project memory store on/off + location, the user model on/off +
// location + review interval, skill discovery on/off + location, slash
// commands on/off + location, and the four MCP discovery flags. Spawn flags
// again — never a settings.yaml key; the default document is the
// pre-feature command line byte for byte. Distinct from the diagnostics
// options above and the runtime settings (learning/steer/soul); `--no-shell`
// stays the permissions document's — one writer per flag.
const daemonOptionsFile = resolve(studioStateDir, "daemon-options.json");
let daemonOptions = DEFAULT_DAEMON_OPTIONS;
// Skills are project-scoped by DEFAULT: a SKILL.md steers the model the same
// way AGENTS.md does, so discovery is pinned to <workspace>/.mecatl/skills
// unless the daemon options relocate it (a trust decision the Tools page
// labels as such, confined to the workspace / the mecatl config dir), and we
// never pass --skills-conventional (which would also pull in ~/.claude/skills
// and the user-global mecatl dir — a much wider trust surface than this app
// should open). With `skills.enabled` off the directory is still owned and
// edited here; the spawn simply omits --skills-dir, which drops the Skill tool.
let skillsDir = skillsDirFor(workspace);
// Disabled skills are MOVED into a holding area inside the pinned skills dir,
// not deleted and not flagged: mecated's discovery walks only the direct
// children of --skills-dir looking for <name>/SKILL.md, so a nested dir is
// invisible to it, and the skill-name grammar forbids a leading dot, so
// `.disabled` can never collide with a real skill.
let disabledSkillsDir = resolve(skillsDir, ".disabled");
// Per-project memory (the Remember/Recall/SearchMemory tools) is OFF in mecated
// until --memory-dir is passed, unlike the user model which is on by default. The
// store is per-project by design, so it lives beside the session store rather than
// in a shared location. Consolidation stays off: it spends tokens in the background.
// A NON-default root keys its memory (and its session store) by a hash of
// its path UNDER THE DEFAULT ROOT's .scratch (src/lib/workspace-config.mjs):
// per-root, but never sprouting state inside a foreign repo.
let memoryDir = memoryDirFor(workspace, defaultWorkspace);

/**
 * Points the LIVE root at `root` and re-derives every root-dependent
 * directory: project skills follow the root (the one directory the
 * controller creates inside it), memory and the session store are keyed
 * per root under the default root's .scratch. The trust registry and the
 * spawn flags read `workspace` at spawn time, so the next startMecatl
 * picks the new root up with no further plumbing.
 */
function applyWorkspace(root) {
  workspace = root;
  applyDaemonOptions(daemonOptions);
}

/** The pinned/default locations the daemon options fall back to for the
 *  LIVE root — what `daemonOptionArgs` renders when no override is saved
 *  and what GET /daemon-options reports as the placeholders. */
function daemonOptionContext() {
  return {
    workspace,
    pinnedSkillsDir: skillsDirFor(workspace),
    defaultMemoryDir: memoryDirFor(workspace, defaultWorkspace),
  };
}

/**
 * Adopts a daemon-options document and re-derives the two directories the
 * controller itself reads (the skills CRUD routes, /status): the override
 * when saved — a relative one resolves against the live root, so a root
 * change (POST /workspace) moves it along — else the pinned/default
 * location. Called by applyWorkspace too, so the two stay one derivation.
 */
function applyDaemonOptions(next) {
  daemonOptions = next;
  const dirs = effectiveDaemonDirs(daemonOptions, daemonOptionContext());
  skillsDir = dirs.skillsDir;
  disabledSkillsDir = resolve(skillsDir, ".disabled");
  memoryDir = dirs.memoryDir;
}
// mecated's user-global config directory (XDG). The daemon defaults'
// `apiKeyFile` is confined to it: the controller reads AND rewrites the
// credentials file for the provider inventory/removal routes, so a
// browser-settable path must never reach outside this directory.
const mecatlConfigDir = process.env.XDG_CONFIG_HOME
  ? resolve(process.env.XDG_CONFIG_HOME, "mecatl")
  : resolve(homedir(), ".config/mecatl");
const defaultAuthFile = resolve(mecatlConfigDir, "auth.yaml");
// The EFFECTIVE credentials file: the default above, or the saved
// `--api-key-file` path (reassigned by applyDaemonDefaults) so the
// inventory, key-test and removal routes read the same file the daemon does.
let authFile = defaultAuthFile;
// The user-global operator settings file, same XDG convention as auth.yaml.
// mecated always reads it at the operator tier; it is where a hand-added
// custom `providers:` block (ADR 0238) lives unless an imported
// operator-settings.yaml (a CLI-tier file, which wins whole-block) carries
// its own.
const userSettingsFile = process.env.XDG_CONFIG_HOME
  ? resolve(process.env.XDG_CONFIG_HOME, "mecatl/settings.yaml")
  : resolve(homedir(), ".config/mecatl/settings.yaml");
// The conventional soul file mecated loads when no --soul-file is passed
// (internal/adapter/soul/store.go soulSubpath) — the placeholder the
// Persona card shows, and the first of the two roots a browser-chosen
// --soul-file must sit under (the other is the workspace). A soul is
// injected into every turn's prompt and GET /v1/soul echoes its content, so
// an unconstrained path would turn the browser into a file-read oracle over
// anything the daemon's user can read (auth.yaml, SSH keys).
const userSoulFile = resolve(mecatlConfigDir, "soul.md");
// A function, not a frozen array: the live root can change (POST /workspace).
const soulFileRoots = () => [mecatlConfigDir, workspace];
// mecated's conventional user-model store when no --user-model-dir is passed
// (cmd/mecated flag help) — the Memory page's placeholder, display only.
const defaultUserModelDir = resolve(mecatlConfigDir, "usermodel");
// Where a browser-chosen daemon-options directory (skills, project memory,
// user model, commands) may sit: the live root, the default root (a
// non-default root's memory store lives under its .scratch) or the mecatl
// config dir. A SKILL.md or a command template steers the model like
// AGENTS.md, and the controller mkdir's the skills/memory dirs before every
// spawn, so an unconstrained path would let the browser plant model-steering
// content — or create directories — anywhere the daemon's user can write.
const optionDirRoots = () => [workspace, defaultWorkspace, mecatlConfigDir];
// mecated's --ready-file target: the atomically-published mecated-ready/1
// document carrying the RESOLVED listener addresses (the daemon binds
// 127.0.0.1:0 and reports what the kernel picked), pid, api_major, features,
// and deployment. Written only after every listener is up, never removed by
// the daemon — the controller unlinks the stale one before each spawn.
const readyFilePath = resolve(studioStateDir, "mecated-ready.json");
// The managed daemon's diagnostics log — Studio's analogue of mecatui's
// embedded-server log file ($XDG_STATE_HOME/mecatl/mecatui.log). mecated has
// no log-file flag (its diagnostics go to stderr), so the controller holding
// that stream appends it here: owner-only inside the 0700 state dir, ONE
// rotated generation at mecatui's 10 MiB bound, plus a bounded in-memory
// ring the Diagnostics page reads through GET /logs. Model-influenced
// content (prompt fragments, provider error bodies) — never rendered as
// markup, served only behind the studio header.
const mecatedLogFile = resolve(studioStateDir, LOG_FILE_NAME);
const rotatedLogFile = resolve(studioStateDir, ROTATED_LOG_FILE_NAME);
const logRing = createLogRing();
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
  // A built-in whose credential env var is set in this environment is
  // registered by mecated from the variable alone (it inherits the env), so
  // it is startable with no auth.yaml block at all. Presence only.
  for (const kind of Object.keys(PROVIDER_ENV_KEYS)) {
    if (providerEnvShadowed(kind) && !names.includes(kind)) names.push(kind);
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
// Session-store defaults: MECATL_STUDIO_STORE_DIR (absolute or
// workspace-relative; default .scratch/studio-sessions under the workspace)
// and MECATL_STUDIO_NO_STORE=1 (in-memory: no --store-dir at all). A saved
// storage-settings.json (POST /storage) overrides both; a bad value throws
// here like a bad MECATL_STUDIO_PROVIDER does.
const storageDefaults = storageDefaultsFromEnv(process.env);
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
// The saved runtime-settings document (PUT /runtime-settings), loaded from
// runtimeStateFile before the first spawn; the default document adds no
// flag and no file, so the command line is byte-identical to before.
let runtimeSettings = DEFAULT_RUNTIME_SETTINGS;
// `--approve-soul` rides EXACTLY ONE spawn (POST /soul/approve): it rewrites
// the drift baseline to the current soul, so persisting it would silently
// re-accept every later edit. Never written to disk.
let approveSoulPending = false;
let operatorSettingsActive = false;
// The saved permissions document (posture / trustProject / noShell), loaded
// from permissionsStateFile before the first spawn; defaults are mecated's
// own (strict, untrusted, shell on).
let permissionsConfig = normalizePermissions({});
// The saved diagnostics-options document (POST /diagnostics-options), loaded
// from diagnosticsOptionsFile before the first spawn; defaults are mecated's
// own (info logging, product metrics on) with the admin listener explicitly
// OFF and the controller mirroring mecated's stderr.
let diagnosticsOptions = normalizeDiagnosticsOptions({});
// The loopback admin/metrics address (`host:port`) the CURRENT child was
// spawned with as `--metrics-addr`, or "" while the surface is off. The
// controller CHOOSES it per spawn (mecated's ready file names only
// http_address), so a restart may move it; GET /perf reports the live one.
let adminAddr = "";
// The SAVED storage document (POST /storage), or null while the env/built-in
// defaults still apply — the same null-means-unsaved shape as
// modelRouterConfig, so a failed restart can roll back to "no file".
let storageSettings = null;
const effectiveStorage = () => storageSettings ?? storageDefaults;
// The `/status.storage` projection against the LIVE root: the saved
// document resolves as storageStatus always did, but `dir`/`defaultDir`
// are the PER-ROOT directories the spawn actually uses (byte-identical for
// the default root; `-<rootKey>`-suffixed under the default root's .scratch
// for any other) — so the Storage card shows the path the daemon has.
const currentStorageStatus = () => {
  const settings = effectiveStorage();
  return {
    ...storageStatus(settings, defaultWorkspace, storageDefaults),
    dir:
      settings.persistence === "durable"
        ? storeDirFor(settings, workspace, defaultWorkspace)
        : "",
    defaultDir: storeDirFor(storageDefaults, workspace, defaultWorkspace),
  };
};
// The SAVED retention document (POST /retention), or null while mecated's
// own defaults apply (no flags passed) — the same null-means-unsaved shape
// as storageSettings, so a failed restart can roll back to "no file".
let retentionSettings = null;
const effectiveRetention = () =>
  retentionSettings ?? normalizeRetentionSettings({});
// The `/status.retention` projection. An imported operator settings file
// suspends the flags entirely (its own retention: block, if any, then
// stands), which the UI reports as "operator-settings".
const retentionStatus = () => ({
  settings: effectiveRetention(),
  managedBy: operatorSettingsActive ? "operator-settings" : "studio",
});
// The saved daemon-defaults document (PUT /daemon-defaults, plus the
// active-provider choice POST /providers/active persists), loaded from
// daemonDefaultsFile before the first spawn; the empty document means every
// flag is omitted and the command line is byte-identical to the pre-feature
// one. Always the NORMALISED shape — never a raw body.
let daemonDefaults = DAEMON_DEFAULTS_EMPTY;
// A this-process-only trust grant: `--trust-project` on the next spawns
// without persisting it, for a "trust once" answer to a trust prompt. Never
// written to disk, so it dies with the controller — and ONLY with the
// controller: every daemon restart in between (a router save, a skill edit,
// a provider switch) keeps it, which the banner's copy says.
let trustOnce = false;
// The controller's OWN trust registry state for the CURRENT spawn (see
// src/lib/controller-trust.mjs), computed in startMecatl — never per /status
// poll, which would walk the workspace tree every 5 s. `trustDrifted` is
// the saved grant's anchor no longer matching the live one: the spawn then
// gets NO --trust-project (fail safe, the daemon's own Drifted arm) and the
// workspace banner re-prompts. Drift is detected at spawn time only.
let trustDrifted = false;
let trustAuthority = false;
let liveTrustAnchor = "";
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
// Whether ToolHive's `thv` CLI is on the controller's PATH — detected ONCE at
// boot with a FIXED command (no user input reaches the shell), so the
// provider inventory can offer to start the proxy (POST /toolhive/start)
// instead of only telling the operator to. Presence only; the resolved path
// is never reported.
let thvOnPath = false;
function detectThvOnPath() {
  return new Promise((done) => {
    execFile(
      "/bin/sh",
      ["-c", "command -v thv"],
      { timeout: 2000 },
      (error, stdout) => done(!error && String(stdout).trim() !== ""),
    );
  });
}

/**
 * Whether `kind`'s documented credential env var is SET in this controller's
 * environment — which the spawned mecated inherits, so the variable SHADOWS
 * the auth.yaml key (the TUI's "configured (environment shadows …)" state).
 * Presence only: the value is never read past the Boolean coercion, never
 * logged, never sent.
 */
function providerEnvShadowed(kind) {
  const name = PROVIDER_ENV_KEYS[kind];
  return name ? Boolean(process.env[name]) : false;
}

/**
 * The provider ids the RUNNING daemon lists models for (its ListModels view,
 * where a provider with no resolved credentials is omitted — so "keyed in
 * auth.yaml but absent here" means mecated started before the key landed and
 * needs a restart). Null when the controller cannot tell: daemon down, on the
 * offline mock (whose registry is the mock alone), or the read failed. A
 * read-only inventory call; nothing about the key crosses it.
 */
async function daemonProviderIds() {
  if (!child || !mecatlBaseURL || provider === "offline mock") return null;
  try {
    const response = await fetchMecatl("/v1/models", {
      signal: AbortSignal.timeout(1500),
    });
    if (!response.ok) return null;
    const body = await response.json();
    const ids = new Set();
    for (const model of Array.isArray(body?.models) ? body.models : []) {
      const id = model?.provider_id ?? model?.providerId;
      if (typeof id === "string" && id !== "") ids.add(id);
    }
    return ids;
  } catch {
    return null;
  }
}

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
  // Into the log file too (not echoed — every caller surfaces it), so a
  // crash-at-start is diagnosable from the file alone.
  recordDaemonLog(`[supervisor] ${startupError}\n`);
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
// The ToolHive gateway is offerable only while it answers AND the saved
// daemon defaults have not passed `--toolhive-llm=false` (a disabled
// detection means mecated never registers the provider, so naming it as the
// default would fail startup).
const toolhiveOfferable = () =>
  toolhiveReady && daemonDefaults.toolhive.enabled;
const preferredKind = () =>
  activeProviderOverride || (toolhiveOfferable() ? "toolhive" : "mock");

/** Whether `kind` is safe to hand to startMecatl right now: the two
 *  synthetic kinds (mock always, toolhive only while the gateway answers and
 *  detection is not disabled), a provider that actually has a block in
 *  auth.yaml, or a settings-defined custom provider (ADR 0238) — never an
 *  arbitrary string, so a typo can't reach mecated's fail-fast
 *  --default-provider check and crash the child. */
function isSelectableProviderKind(kind, selectableNames) {
  if (kind === "mock") return true;
  if (kind === "toolhive") return toolhiveOfferable();
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

/**
 * The operator-tier `learning:` block and `steer:` scalar mecated will
 * itself capture, mirroring its first-file-wins order: the imported
 * operator-settings.yaml (a CLI-tier file) when active, then the user-global
 * settings.yaml. What the rendered runtime-settings file merges over, and
 * what GET /runtime-settings reports as inherited.
 */
async function readInheritedRuntimeSettings() {
  const sources = operatorSettingsActive
    ? [operatorSettingsFile, userSettingsFile]
    : [userSettingsFile];
  let learning = null;
  let steer = null;
  for (const file of sources) {
    let text;
    try {
      text = await readFile(file, "utf8");
    } catch {
      continue;
    }
    if (learning === null) learning = readLearningBlock(text);
    if (steer === null) steer = readSteerScalar(text);
  }
  return {
    learning: learning ?? NO_INHERITED_SETTINGS.learning,
    steer,
  };
}

/** Atomic (tmp + rename), owner-only: the JSON document only. The YAML
 *  mecated reads is rendered fresh by every spawn (writeRuntimeSettingsYAML)
 *  so a later edit of the user-global learning block is still merged in. */
async function persistRuntimeState(config) {
  await mkdir(studioStateDir, { recursive: true, mode: 0o700 });
  const temp = `${runtimeStateFile}.tmp`;
  await writeFile(temp, `${JSON.stringify(config, null, 2)}\n`, {
    mode: 0o600,
  });
  await rename(temp, runtimeStateFile);
}

/** The CLI-tier learning file for THIS spawn: Studio's mode/sensitivity
 *  merged over the inherited block. Atomic, owner-only. */
async function writeRuntimeSettingsYAML(config) {
  await mkdir(studioStateDir, { recursive: true, mode: 0o700 });
  const inherited = await readInheritedRuntimeSettings();
  const temp = `${runtimeSettingsFile}.tmp`;
  await writeFile(temp, renderRuntimeSettingsYAML(config, inherited), {
    mode: 0o600,
  });
  await rename(temp, runtimeSettingsFile);
}

/** The saved runtime settings, or the defaults when none were saved yet. A
 *  corrupt file is logged and ignored, never honoured. */
async function loadRuntimeSettings() {
  try {
    return normalizeRuntimeSettings(
      JSON.parse(await readFile(runtimeStateFile, "utf8")),
    );
  } catch (error) {
    if (error?.code !== "ENOENT")
      process.stderr.write(
        `[runtime-settings] saved configuration ignored: ${error.message || error}\n`,
      );
    return DEFAULT_RUNTIME_SETTINGS;
  }
}

/** Atomic (tmp + rename), owner-only. Flags only — no settings.yaml key. */
async function persistDaemonOptions(config) {
  await mkdir(studioStateDir, { recursive: true, mode: 0o700 });
  const temp = `${daemonOptionsFile}.tmp`;
  await writeFile(temp, `${JSON.stringify(config, null, 2)}\n`, {
    mode: 0o600,
  });
  await rename(temp, daemonOptionsFile);
}

/** The saved daemon options, or the defaults when none were saved yet. A
 *  corrupt file is logged and ignored, never honoured. */
async function loadDaemonOptions() {
  try {
    return normalizeDaemonOptions(
      JSON.parse(await readFile(daemonOptionsFile, "utf8")),
    );
  } catch (error) {
    if (error?.code !== "ENOENT")
      process.stderr.write(
        `[daemon-options] saved configuration ignored: ${error.message || error}\n`,
      );
    return DEFAULT_DAEMON_OPTIONS;
  }
}

/**
 * The filesystem half of the daemon-options directory check (the shape half
 * is normalizeOptionDir): every SET directory, resolved against the live
 * root, must sit under one of optionDirRoots() lexically AND after
 * realpath() of its deepest existing ancestor (a symlinked parent cannot
 * escape; the directory itself may not exist yet — the spawn creates the
 * skills/memory ones), and an existing path must be a real directory, not a
 * symlink or a file. Throws a 400-shaped error naming the field.
 */
async function validateOptionDirs(config) {
  const bad = (message) =>
    Object.assign(new Error(message), { statusCode: 400 });
  const roots = optionDirRoots();
  const realRoots = await Promise.all(
    roots.map((root) => realpath(root).catch(() => root)),
  );
  for (const [field, dir] of daemonOptionDirFields(config)) {
    const path = resolveOptionDir(dir, workspace);
    if (!optionDirWithinRoots(path, roots))
      throw bad(
        `${field} must be inside the workspace ${workspace}${
          defaultWorkspace !== workspace ? `, ${defaultWorkspace}` : ""
        } or ${mecatlConfigDir}`,
      );
    let probe = path;
    let info = null;
    while (probe !== "/") {
      try {
        info = await lstat(probe);
        break;
      } catch {
        probe = dirname(probe);
      }
    }
    if (info && probe === path) {
      if (info.isSymbolicLink()) throw bad(`${field} must not be a symlink`);
      if (!info.isDirectory()) throw bad(`${field} must be a directory`);
    }
    const real = await realpath(probe).catch(() => "");
    const realPath = real ? `${real}${path.slice(probe.length)}` : "";
    if (!realPath || !optionDirWithinRoots(realPath, realRoots))
      throw bad(`${field} resolves outside the allowed directories`);
  }
}

/** The GET /daemon-options body: the saved document, the locations it
 *  falls back to (the form's placeholders), the directories THIS spawn
 *  resolved to, and where they may sit. Paths on this machine, never
 *  contents. */
function daemonOptionsDocument() {
  const context = daemonOptionContext();
  return {
    options: daemonOptions,
    defaults: {
      skillsDir: context.pinnedSkillsDir,
      memoryDir: context.defaultMemoryDir,
      userModelDir: defaultUserModelDir,
      commandDirs: DEFAULT_COMMAND_DIRS,
    },
    effective: effectiveDaemonDirs(daemonOptions, context),
    allowedRoots: optionDirRoots(),
  };
}

/** The `/status` mirror: the switches the CURRENT child was spawned with,
 *  read-only, one poll for the UI. */
function daemonOptionsStatus() {
  return {
    projectMemory: daemonOptions.projectMemory.enabled,
    userModel: daemonOptions.userModel.enabled,
    userModelReviewInterval: daemonOptions.userModel.reviewInterval,
    skills: daemonOptions.skills.enabled,
    commands: daemonOptions.commands.enabled,
    toolhive: daemonOptions.mcp.toolhive,
    toolhiveGroup: daemonOptions.mcp.toolhiveGroup,
    mcpResourceTools: daemonOptions.mcp.resourceTools,
    mcpPrompts: daemonOptions.mcp.prompts,
  };
}

/**
 * The filesystem half of the soul-path check (the shape half is
 * normalizeSoulFile): the path must sit under the mecatl config dir or the
 * workspace BOTH lexically and after realpath() (a symlinked parent cannot
 * escape), must itself be a regular file rather than a symlink (mecatui's
 * rejectSymlinkPath discipline), must fit the daemon's 20 KiB ceiling
 * (over it mecated rejects the soul outright, so the save would "succeed"
 * into a persona-less daemon), and must be readable. Throws a 400-shaped
 * error naming the reason.
 */
async function validateSoulFile(path) {
  const bad = (message) =>
    Object.assign(new Error(message), { statusCode: 400 });
  if (!soulFileWithinRoots(path, soulFileRoots()))
    throw bad(
      `soul.file must be inside ${mecatlConfigDir} or the workspace ${workspace}`,
    );
  let info;
  try {
    info = await lstat(path);
  } catch {
    throw bad(`soul.file does not exist: ${path}`);
  }
  if (info.isSymbolicLink()) throw bad("soul.file must not be a symlink");
  if (!info.isFile()) throw bad("soul.file must be a regular file");
  if (info.size > SOUL_MAX_BYTES)
    throw bad(
      `soul.file is ${info.size} bytes; mecated rejects a soul over ${SOUL_MAX_BYTES} bytes`,
    );
  const real = await realpath(path).catch(() => "");
  const realRoots = await Promise.all(
    soulFileRoots().map((root) => realpath(root).catch(() => root)),
  );
  if (!real || !soulFileWithinRoots(real, realRoots))
    throw bad("soul.file resolves outside the allowed directories");
  try {
    await readFile(path, "utf8");
  } catch (error) {
    throw bad(`soul.file is not readable: ${error.message || error}`);
  }
}

/** The `*.md` regular files directly inside the mecatl config dir (dotfiles
 *  and symlinks skipped, at most 50, sorted) — the picker's candidates, so
 *  a user need not type a path. Absolute paths on the operator's OWN
 *  machine (managed mode only), never file contents. */
async function listSoulCandidates() {
  let entries;
  try {
    entries = await readdir(mecatlConfigDir, { withFileTypes: true });
  } catch {
    return [];
  }
  return entries
    .filter(
      (entry) =>
        entry.isFile() &&
        !entry.name.startsWith(".") &&
        entry.name.endsWith(".md") &&
        entry.name !== ".md",
    )
    .map((entry) => ({
      path: resolve(mecatlConfigDir, entry.name),
      name: basename(entry.name, ".md"),
    }))
    .sort((a, b) => a.name.localeCompare(b.name))
    .slice(0, 50);
}

/** The GET /runtime-settings body: the saved document, who manages each
 *  knob, the inherited settings.yaml values, the fold mecated will actually
 *  run with, and the soul picker's inputs. */
async function runtimeSettingsDocument() {
  const inherited = await readInheritedRuntimeSettings();
  return {
    config: runtimeSettings,
    managedBy: {
      learning: operatorSettingsActive ? "operator-settings" : "studio",
      steer: "studio",
      soul: "studio",
    },
    inherited: {
      learning: {
        mode: inherited.learning.mode,
        sensitivity: inherited.learning.sensitivity,
      },
      steer: inherited.steer,
    },
    effective: effectiveRuntimeSettings(runtimeSettings, inherited, {
      operatorSettingsActive,
    }),
    soulFileDefault: userSoulFile,
    soulCandidates: await listSoulCandidates(),
  };
}

/** Atomic (tmp + rename), owner-only. Flags only — no settings.yaml key. */
async function persistPermissions(config) {
  await mkdir(studioStateDir, { recursive: true, mode: 0o700 });
  const temp = `${permissionsStateFile}.tmp`;
  await writeFile(temp, `${JSON.stringify(config, null, 2)}\n`, {
    mode: 0o600,
  });
  await rename(temp, permissionsStateFile);
}

/** The saved permissions, or mecated's defaults when none were saved yet. A
 *  corrupt or unknown-posture file is logged and ignored, never honoured. */
async function loadPermissions() {
  try {
    return normalizePermissions(
      JSON.parse(await readFile(permissionsStateFile, "utf8")),
    );
  } catch (error) {
    if (error?.code !== "ENOENT")
      process.stderr.write(
        `[permissions] saved configuration ignored: ${error.message || error}\n`,
      );
    return normalizePermissions({});
  }
}

/**
 * Persists a permissions document and restarts the daemon on its flags,
 * serialised on the restart queue. A start mecated refuses (auto/yolo as
 * root outside MECATL_SANDBOX) rolls the previous document back, restarts
 * on it, and rethrows with mecated's own refusal so the caller can answer
 * it as the 400 body. Shared by POST /permissions and the two trust grants.
 * @param {ReturnType<typeof normalizePermissions>} next
 */
function restartOnPermissions(next) {
  return queueRestart(async () => {
    const previous = permissionsConfig;
    permissionsConfig = next;
    await persistPermissions(next);
    try {
      await startMecatl(preferredKind());
    } catch (error) {
      // mecated names the refused tier on stderr; the generic
      // "exited during startup" alone would hide the reason.
      const refusal = startupLog.match(/posture "[a-z]+" refused[^\n]*/);
      permissionsConfig = previous;
      await persistPermissions(previous);
      await startMecatl(preferredKind());
      throw refusal
        ? new Error(`${refusal[0]} (previous permissions restored)`)
        : error;
    }
  });
}

/** Atomic (tmp + rename), owner-only. Flags only — no settings.yaml key. */
async function persistDiagnosticsOptions(options) {
  await mkdir(studioStateDir, { recursive: true, mode: 0o700 });
  const temp = `${diagnosticsOptionsFile}.tmp`;
  await writeFile(temp, `${JSON.stringify(options, null, 2)}\n`, {
    mode: 0o600,
  });
  await rename(temp, diagnosticsOptionsFile);
}

/** The saved diagnostics options, or the defaults when none were saved yet.
 *  A corrupt file is logged and ignored, never honoured. */
async function loadDiagnosticsOptions() {
  try {
    return normalizeDiagnosticsOptions(
      JSON.parse(await readFile(diagnosticsOptionsFile, "utf8")),
    );
  } catch (error) {
    if (error?.code !== "ENOENT")
      process.stderr.write(
        `[diagnostics] saved options ignored: ${error.message || error}\n`,
      );
    return normalizeDiagnosticsOptions({});
  }
}

// Bytes in the current log generation (null until the first write stat's
// the file), and the ONE promise chain every append rides so chunk order
// and the rotate-then-append step hold under a chatty daemon.
let daemonLogSize = null;
let daemonLogQueue = Promise.resolve();
let daemonLogWriteWarned = false;

/**
 * Records one chunk of daemon diagnostics: into the in-memory ring at once,
 * and onto the owner-only file through the serialised queue — rotating the
 * current generation to `mecated.log.1` (overwriting the previous one) when
 * the append would push it past the 10 MiB bound. A file that cannot be
 * written is reported ONCE and never takes the relay down: the ring and
 * the terminal mirror keep working without it.
 */
function recordDaemonLog(text) {
  if (typeof text !== "string" || text === "") return;
  logRing.append(text);
  const bytes = Buffer.byteLength(text);
  daemonLogQueue = daemonLogQueue
    .then(async () => {
      if (daemonLogSize === null) {
        await mkdir(studioStateDir, { recursive: true, mode: 0o700 });
        daemonLogSize = await stat(mecatedLogFile).then(
          (info) => info.size,
          () => 0,
        );
      }
      if (shouldRotate(daemonLogSize, bytes)) {
        await rename(mecatedLogFile, rotatedLogFile);
        daemonLogSize = 0;
      }
      await appendFile(mecatedLogFile, text, { mode: 0o600 });
      daemonLogSize += bytes;
    })
    .catch((error) => {
      daemonLogSize = null;
      if (daemonLogWriteWarned) return;
      daemonLogWriteWarned = true;
      process.stderr.write(
        `[supervisor] daemon log file unavailable (${error.message || error}); continuing without it\n`,
      );
    });
}

/** The controller's own supervisor lines: always echoed (they are not the
 *  mecated relay `quiet` mutes) AND recorded in the same file, so a restart
 *  loop reads in order next to the daemon's last words. */
function supervisorNote(line) {
  process.stderr.write(line);
  recordDaemonLog(line);
}

/** The `GET /logs` payload: file facts, the bounded tail, the two
 *  controller-side knobs the card edits, liveness and the last startup
 *  error. Paths and text only — nothing here is a credential. */
async function daemonLogStatus(lines) {
  const sizeBytes = await stat(mecatedLogFile).then(
    (info) => info.size,
    () => 0,
  );
  return {
    path: mecatedLogFile,
    rotatedPath: rotatedLogFile,
    sizeBytes,
    maxBytes: MAX_LOG_FILE_BYTES,
    lines: logRing.lines(lines),
    truncated: tailIsTruncated(logRing, lines),
    quiet: diagnosticsOptions.quiet,
    level: diagnosticsOptions.logLevel,
    running: Boolean(child),
    startupError,
  };
}

/** The effective product-metrics verdict: the saved switch AND no opt-out
 *  in the controller's own environment, which the spawned mecated inherits.
 *  mecated's precedence is flag > MECATL_PRODUCT_METRICS > DO_NOT_TRACK >
 *  settings.yaml (internal/cliconfig ResolveProductMetricsEnabled), so an
 *  explicit --product-metrics=true WOULD out-rank the environment — which
 *  is exactly why the controller never passes it (only =false when the
 *  switch is off): the operator's env opt-out always wins. The settings.yaml
 *  tier is not readable here, so `enabled: true` means "not disabled by
 *  Studio or this environment", not a definitive on. */
function diagnosticsStatus() {
  const envOptOut = productMetricsEnvOptOut(process.env);
  return {
    options: diagnosticsOptions,
    productMetrics: {
      enabled: !envOptOut && diagnosticsOptions.productMetrics.enabled,
      dryRun: diagnosticsOptions.productMetrics.dryRun,
      source: envOptOut ? "environment" : "studio",
    },
  };
}

/** Atomic (tmp + rename), owner-only. Flags only — no settings.yaml key. */
async function persistDaemonDefaults(config) {
  await mkdir(studioStateDir, { recursive: true, mode: 0o700 });
  const temp = `${daemonDefaultsFile}.tmp`;
  await writeFile(temp, `${JSON.stringify(config, null, 2)}\n`, {
    mode: 0o600,
  });
  await rename(temp, daemonDefaultsFile);
}

/** The saved daemon defaults, or the empty document when none were saved.
 *  A corrupt or out-of-grammar file is logged and ignored, never honoured
 *  (a stale --default-model would otherwise wedge every restart). */
async function loadDaemonDefaults() {
  try {
    return normalizeDaemonDefaults(
      JSON.parse(await readFile(daemonDefaultsFile, "utf8")),
      { configDir: mecatlConfigDir },
    );
  } catch (error) {
    if (error?.code !== "ENOENT")
      process.stderr.write(
        `[daemon-defaults] saved configuration ignored: ${error.message || error}\n`,
      );
    return DAEMON_DEFAULTS_EMPTY;
  }
}

/** Makes `next` the live document: the effective credentials file follows
 *  its `apiKeyFile` so every auth.yaml route reads what the daemon reads. */
function applyDaemonDefaults(next) {
  daemonDefaults = next;
  authFile = next.apiKeyFile || defaultAuthFile;
}

/** Atomic (tmp + rename), owner-only. A spawn flag — no settings.yaml key. */
async function persistStorageSettings(config) {
  await mkdir(studioStateDir, { recursive: true, mode: 0o700 });
  const temp = `${storageStateFile}.tmp`;
  await writeFile(temp, `${JSON.stringify(config, null, 2)}\n`, {
    mode: 0o600,
  });
  await rename(temp, storageStateFile);
}

/** The saved storage document, or null when none was saved yet (the env /
 *  built-in defaults then apply). A corrupt file is logged and ignored. */
async function loadStorageSettings() {
  try {
    return normalizeStorageSettings(
      JSON.parse(await readFile(storageStateFile, "utf8")),
      storageDefaults,
    );
  } catch (error) {
    if (error?.code !== "ENOENT")
      process.stderr.write(
        `[storage] saved configuration ignored: ${error.message || error}\n`,
      );
    return null;
  }
}

/** The saved workspace root: atomic (tmp + rename), owner-only. The DEFAULT
 *  root writes no file — the state dir stays clean until a root is chosen,
 *  and a rollback to the default removes the document again. */
async function persistWorkspace(root) {
  if (root === defaultWorkspace) {
    await rm(workspaceStateFile, { force: true });
    return;
  }
  await mkdir(studioStateDir, { recursive: true, mode: 0o700 });
  const temp = `${workspaceStateFile}.tmp`;
  await writeFile(temp, `${JSON.stringify({ workspace: root }, null, 2)}\n`, {
    mode: 0o600,
  });
  await rename(temp, workspaceStateFile);
}

/** The saved workspace root, or the default when none was saved. A corrupt
 *  document falls back to the default (normalizeWorkspaceConfig); a saved
 *  root that no longer exists / is no longer a directory is logged and
 *  ALSO falls back — mecated would otherwise fail every start against it. */
async function loadWorkspace() {
  let saved;
  try {
    saved = normalizeWorkspaceConfig(
      JSON.parse(await readFile(workspaceStateFile, "utf8")),
      defaultWorkspace,
    ).workspace;
  } catch (error) {
    if (error?.code !== "ENOENT")
      process.stderr.write(
        `[workspace] saved root ignored: ${error.message || error}\n`,
      );
    return defaultWorkspace;
  }
  if (saved === defaultWorkspace) return defaultWorkspace;
  try {
    return await validateWorkspacePath(saved, {
      realpath,
      stat,
      defaultWorkspace,
      forbidden: [studioStateDir],
    });
  } catch (error) {
    process.stderr.write(
      `[workspace] saved root ${saved} ignored, using the default: ${error.message || error}\n`,
    );
    return defaultWorkspace;
  }
}

/** Atomic (tmp + rename), owner-only. Spawn flags — no settings.yaml key. */
async function persistRetentionSettings(config) {
  await mkdir(studioStateDir, { recursive: true, mode: 0o700 });
  const temp = `${retentionStateFile}.tmp`;
  await writeFile(temp, `${JSON.stringify(config, null, 2)}\n`, {
    mode: 0o600,
  });
  await rename(temp, retentionStateFile);
}

/** The saved retention document, or null when none was saved yet (mecated's
 *  own defaults then apply). A corrupt file — including one that enables
 *  main deletion without the acknowledgement — is logged and ignored. */
async function loadRetentionSettings() {
  try {
    return normalizeRetentionSettings(
      JSON.parse(await readFile(retentionStateFile, "utf8")),
    );
  } catch (error) {
    if (error?.code !== "ENOENT")
      process.stderr.write(
        `[retention] saved configuration ignored: ${error.message || error}\n`,
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
  supervisorNote(
    `[supervisor] mecated stopped unexpectedly; restarting in ${wait}ms\n`,
  );
  restartTimer = setTimeout(() => {
    restartTimer = null;
    queueRestart(async () => {
      if (shuttingDown || child) return;
      try {
        await startMecatl(preferredKind());
        restartFailures = 0;
        supervisorNote("[supervisor] mecated restarted\n");
      } catch (error) {
        restartFailures += 1;
        supervisorNote(
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

// `adminRetry` is internal: set on the ONE re-spawn startMecatl allows itself
// when the probed admin port was taken before mecated could bind it.
async function startMecatl(kind, { adminRetry = false } = {}) {
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
  // Session store: `--store-dir <abs>` for a durable store (the directory
  // created first — mecated does not create it), NO flag for in-memory
  // (mecated has no --no-store; an empty --store-dir IS the in-memory store).
  // A path mkdir refuses fails this start, which the POST /storage rollback
  // turns into the 400 body and a restart on the previous document.
  //
  // Both are PER-ROOT (src/lib/workspace-config.mjs): a non-default root's
  // store is `<default>/.scratch/studio-sessions-<rootKey>` — a session is
  // bound to the placement it was created in (ADR 0291), so a shared store
  // would list chats that cannot run — and the default root keeps the
  // historical path byte for byte.
  const storage = effectiveStorage();
  if (storage.persistence === "durable")
    await mkdir(storeDirFor(storage, workspace, defaultWorkspace), {
      recursive: true,
    });
  const args = [
    "serve",
    "--workspace",
    workspace,
    ...storageArgsFor(storage, workspace, defaultWorkspace),
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
  // The learning mode/sensitivity file (--permission-config is repeatable;
  // its `learning:` block does not overlap the router file's `models:`),
  // rendered fresh for THIS spawn over the user-global learning block, and
  // only when the document overrides something. Not alongside an imported
  // operator settings file: that file's own learning: block then stands.
  if (!operatorSettingsActive && runtimeSettingsHasYAML(runtimeSettings)) {
    await writeRuntimeSettingsYAML(runtimeSettings);
    args.push("--permission-config", runtimeSettingsFile);
  }
  // The daemon options as spawn flags (src/lib/daemon-options.mjs): the
  // skills/memory directories (mecated refuses to start on a missing
  // --skills-dir, and an empty directory is the correct "no skills yet"
  // state, so each ENABLED store's directory is created before every spawn
  // — a disabled one gets neither the mkdir nor the flag, which is how
  // mecated turns Remember/Recall and the Skill tool off), the user-model
  // flags, slash commands and MCP discovery. The default document renders
  // the pre-feature command line byte for byte. Every directory passed the
  // containment check at save time (validateOptionDirs).
  const optionDirs = effectiveDaemonDirs(daemonOptions, daemonOptionContext());
  if (daemonOptions.skills.enabled)
    await mkdir(optionDirs.skillsDir, { recursive: true });
  if (daemonOptions.projectMemory.enabled)
    await mkdir(optionDirs.memoryDir, { recursive: true });
  if (daemonOptions.userModel.enabled && optionDirs.userModelDir)
    await mkdir(optionDirs.userModelDir, { recursive: true });
  if (daemonOptions.commands.enabled && optionDirs.commandsDir)
    await mkdir(optionDirs.commandsDir, { recursive: true });
  args.push(...daemonOptionArgs(daemonOptions, daemonOptionContext()));
  // The saved daemon defaults as spawn flags: the model pair is emitted for
  // THIS kind only (mecated validates --default-model against the current
  // default provider fail-fast), the rest is global. An empty document adds
  // nothing, so the pre-feature command line is byte-identical.
  args.push(...daemonDefaultArgs(daemonDefaults, kind));
  // Operator posture / project trust / shell-less mode as CLI flags. This
  // spawn is deliberately NOT --headless: on an interactive root mecated
  // raises the project-trust floor for trusted/auto/yolo, which is what the
  // Permissions page tells the user (controller-permissions.test.ts pins it).
  //
  // The controller's own trust registry is re-read here, once per spawn:
  // the live identity anchor decides whether a SAVED grant still stands
  // (its stamped anchor must equal the live one — a soul/agent/command/
  // skill edit since the grant drifts it, and a drifted grant passes NO
  // trust flag), and the authority probe tells /status whether there is
  // anything a grant would admit at all.
  liveTrustAnchor = trustAnchor(workspace);
  trustAuthority = hasProjectAuthority(workspace);
  trustDrifted =
    permissionsConfig.trustProject &&
    permissionsConfig.trustAnchor !== liveTrustAnchor;
  args.push(...permissionArgs(permissionsConfig, { trustOnce, trustDrifted }));
  // Observability flags: log level, the admin/metrics listener, the perf
  // MCP mount, the goroutine alarm, product-metrics opt-out. `quiet` is
  // controller-side (the stderr mirror below) and adds no flag.
  //
  // The admin listener is EXPLICIT in both directions. Off (the default)
  // passes `--metrics-addr=`: mecated's built-in default is a FIXED
  // loopback port (127.0.0.1:9090) that two managed daemons on one machine
  // would fight over and that an "off" switch would otherwise silently
  // leave open — so a managed daemon opens NO admin listener until the
  // Performance card turns it on (anyone who scraped :9090 from a managed
  // Studio daemon before this flag was passed enables the surface there).
  // On, the controller CHOOSES the port: the ready file names only
  // http_address, so a free loopback port is probed and released just
  // before the spawn. Probe-to-bind is a real, rare race; the ready-wait
  // below re-probes and re-spawns ONCE when the failed start says the
  // address was taken.
  adminAddr = diagnosticsOptions.admin.enabled
    ? `127.0.0.1:${await freeLoopbackPort()}`
    : "";
  args.push(...diagnosticsOptionArgs(diagnosticsOptions, { adminAddr }));
  // Retention limits + sweep cadence as CLI flags (they out-rank a settings
  // file per field), but ONLY when no imported operator settings file is
  // active: that file's own retention: block, if any, then stands untouched
  // and POST /retention is refused.
  if (!operatorSettingsActive)
    args.push(...retentionArgs(effectiveRetention()));
  // The steer opt-out and the soul flags (--no-steer, --no-soul,
  // --soul-strict, --soul-file; --approve-soul for the one spawn POST
  // /soul/approve asks for). CLI flags out-rank any settings file, so they
  // hold under an imported operator file too — and --no-steer can only
  // tighten (steer is on by default; the file's own steer: false stands).
  args.push(
    ...runtimeSettingsArgs(runtimeSettings, {
      approveSoul: approveSoulPending,
    }),
  );
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
    // The file + the GET /logs ring always receive the text (that is what
    // makes the diagnostics operator-recoverable); the controller-side
    // `quiet` switch (POST /diagnostics-options) mutes ONLY the mirror onto
    // the controller's own terminal — mecatui's --quiet, minus the file.
    recordDaemonLog(text);
    if (!diagnosticsOptions.quiet) process.stderr.write(`[mecatl] ${text}`);
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
    // The admin port was probed, not kernel-assigned, so another process
    // can take it between the probe and mecated's bind. One re-probe +
    // re-spawn covers that race (the dead child's stderr is in startupLog);
    // a second failure is a real startup error and surfaces as one.
    if (adminAddr !== "" && !adminRetry && isAddressInUse(startupLog)) {
      supervisorNote(
        `[supervisor] admin listener ${adminAddr} was taken before mecated bound it; retrying on another port\n`,
      );
      return startMecatl(kind, { adminRetry: true });
    }
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
        // The LIVE workspace root the daemon was spawned against (a display
        // label in the browser, never a placement input — Studio rule 2)
        // and the default it falls back to; POST /workspace changes it.
        workspace,
        defaultWorkspace,
        gateway: gateway ? { name: gateway.name, url: gateway.url } : null,
        toolhiveGateway: {
          available: toolhiveReady,
          baseURL: toolhiveGatewayURL,
          active: provider === "ToolHive LLM gateway",
          // Whether the thv CLI was found on PATH at boot (presence only),
          // so the UI can offer POST /toolhive/start instead of a hint.
          thvOnPath,
        },
        modelRouter: modelRouterConfig
          ? {
              enabled: modelRouterConfig.enabled,
              categories: modelRouterConfig.categories.length,
            }
          : null,
        operatorSettings: operatorSettingsActive,
        // The saved permissions the daemon was spawned with (flags, never a
        // settings.yaml key) plus the volatile trust-once grant. The
        // EFFECTIVE posture is the daemon's own capabilities.posture.
        permissions: {
          posture: permissionsConfig.posture,
          trustProject: permissionsConfig.trustProject,
          noShell: permissionsConfig.noShell,
          trustOnce,
        },
        // The controller's OWN project-trust registry as resolved for the
        // CURRENT spawn (src/lib/controller-trust.mjs): whether the
        // workspace carries anything a grant would admit, the decision the
        // spawn actually got, who granted it, and the live identity anchor
        // (an opaque hash the banner keys its "Not now" on). Computed at
        // spawn time — never here — and it cannot see the daemon's own
        // registry (its remembered / declared grants), which the UI states.
        trust: {
          hasAuthority: trustAuthority,
          ...trustDecision(permissionsConfig, { trustOnce, trustDrifted }),
          anchor: liveTrustAnchor,
        },
        // The skills/memory directories the CURRENT child was spawned with
        // (or would be — `enabled: false` means the spawn omitted the
        // flag), scoped by which allowed root holds them.
        skills: {
          dir: skillsDir,
          scope: optionDirScope(skillsDir, {
            workspace,
            defaultWorkspace,
            configDir: mecatlConfigDir,
          }),
          enabled: daemonOptions.skills.enabled,
        },
        memory: {
          dir: memoryDir,
          scope: optionDirScope(memoryDir, {
            workspace,
            defaultWorkspace,
            configDir: mecatlConfigDir,
          }),
          enabled: daemonOptions.projectMemory.enabled,
        },
        // The saved daemon options the child was spawned with (spawn flags:
        // the two memory stores, the Skill tool, slash commands, MCP
        // discovery) — switches only, no paths beyond the two above.
        daemonOptions: daemonOptionsStatus(),
        // The session store the daemon was spawned with: durable dir
        // (resolved) or in-memory, plus the default the form falls back to.
        // Paths only — the store's CONTENTS never cross here.
        storage: currentStorageStatus(),
        // The retention flags the daemon was spawned with (or that mecated's
        // defaults apply) and who manages them. The EFFECTIVE policy is the
        // daemon's own GET /v1/storage/health.
        retention: retentionStatus(),
        // The saved daemon defaults the child was spawned with (spawn
        // flags: default/subagent model per provider, effort, context
        // window, prompt caching, base URLs, ToolHive, aliases/slots, the
        // credentials-file PATH — never a key) — one poll for the UI.
        daemonDefaults,
        // The saved diagnostics options the child was spawned with (log
        // level, admin listener, product-metrics opt-out, controller-side
        // quiet) plus the effective product-metrics verdict — read-only,
        // nothing secret-shaped. The posture stays under `permissions`.
        diagnosticsOptions: diagnosticsStatus(),
        // The LIVE runtime admin surface: whether this child was spawned
        // with the loopback admin listener, the origin the controller chose
        // for it and the perf MCP mount — the same payload as GET /perf.
        perf: perfStatus(diagnosticsOptions, adminAddr),
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
    // Every row is shaped by describeProviderRow (src/lib/provider-auth.mjs,
    // vitest-covered): class, auth method + state (env shadowing included),
    // default model, next step — from BOOLEANS and non-secret definition
    // fields only. The running daemon's inventory decides the "restart to
    // load the key" hint; the spawn kind decides `active`.
    const inventory = await daemonProviderIds();
    const inInventory = (name) => (inventory ? inventory.has(name) : null);
    const savedDefaultModel = (kind) =>
      daemonDefaults.models[kind]?.defaultModel ?? "";
    const currentKind = preferredKind();
    const providers = authRows
      .filter((row) => !customNames.has(row.name))
      .map((row) => ({
        ...describeProviderRow({
          kind: row.name,
          hasBlock: true,
          keyPresent: row.keyPresent,
          envShadowed: providerEnvShadowed(row.name),
          active: currentKind === row.name,
          inDaemonInventory: inInventory(row.name),
          defaultModel: savedDefaultModel(row.name),
        }),
        testable: Object.hasOwn(providerKeyProbes, row.name),
      }));
    for (const definition of custom) {
      const keyed = definition.authMethod === "api_key";
      const authRow = authRows.find((row) => row.name === definition.name);
      providers.push({
        ...describeProviderRow({
          kind: definition.name,
          definition,
          hasBlock: Boolean(authRow),
          keyPresent: Boolean(authRow?.keyPresent),
          active: currentKind === definition.name,
          inDaemonInventory: inInventory(definition.name),
          defaultModel: savedDefaultModel(definition.name),
        }),
        testable:
          keyed &&
          customProviderProbeURL(definition.apiFlavor, definition.baseURL) !==
            "",
      });
    }
    // The UNCONFIGURED built-in kinds too (`configured: false`, or true when
    // their env var alone configures them), so the UI can describe any kind
    // the way `providers status PROVIDER` does and offer the guided add.
    const listed = new Set(providers.map((row) => row.name));
    for (const entry of KNOWN_AUTH_PROVIDERS) {
      if (listed.has(entry.name)) continue;
      providers.push({
        ...describeProviderRow({
          kind: entry.name,
          envShadowed: providerEnvShadowed(entry.name),
          active: currentKind === entry.name,
          inDaemonInventory: inInventory(entry.name),
          defaultModel: savedDefaultModel(entry.name),
        }),
        testable: false,
      });
    }
    // The ToolHive LLM gateway as an EXTERNAL-class row: reachability from
    // the loopback probe, whether thv is installed (so Studio can start the
    // proxy), and the probe URL for display.
    providers.push({
      ...describeToolhiveRow({
        reachable: toolhiveReady,
        active: provider === "ToolHive LLM gateway",
        baseURL: toolhiveGatewayURL,
        thvOnPath,
        enabled: daemonDefaults.toolhive.enabled,
      }),
      testable: false,
    });
    response.end(JSON.stringify({ providers }));
    return;
  }
  // The provider kinds the daemon understands, with the guided-add snippet
  // (a <YOUR_KEY> placeholder — this route never sees a real credential).
  if (request.method === "GET" && requestURL.pathname === "/providers/known") {
    response.end(JSON.stringify({ known: KNOWN_AUTH_PROVIDERS }));
    return;
  }
  // Starts the ToolHive LLM gateway proxy for the operator — the `providers
  // setup` delegation to `thv llm`. A DETACHED spawn with ignored stdio (the
  // proxy outlives the controller) of a FIXED argv, then a bounded readiness
  // poll that updates toolhiveReady. thv's INTERACTIVE browser login is
  // deliberately not driven from here: with no cached token the proxy blocks
  // on it, the poll simply times out, and the answer tells the operator to
  // run `thv llm login` in a terminal first. Nothing here restarts mecated —
  // "Set as active" on the row does that. Header-gated like every write.
  if (request.method === "POST" && requestURL.pathname === "/toolhive/start") {
    if (!thvOnPath) {
      jsonError(
        response,
        409,
        "ToolHive's thv CLI is not on the controller's PATH — install ToolHive, then run `thv llm proxy start` yourself.",
      );
      return;
    }
    if (!toolhiveReady) {
      try {
        const proxy = spawn("thv", ["llm", "proxy", "start"], {
          detached: true,
          stdio: "ignore",
        });
        proxy.on("error", (error) => {
          process.stderr.write(
            `[toolhive] thv llm proxy start failed: ${error.message || error}\n`,
          );
        });
        proxy.unref();
      } catch (error) {
        jsonError(
          response,
          500,
          `Could not start thv: ${error.message || error}`,
        );
        return;
      }
      // Up to ~8 s in total: each probe is itself bounded (2.5 s), so a proxy
      // parked on its login prompt cannot hold this request for long.
      const deadline = Date.now() + 8000;
      while (!toolhiveReady && Date.now() < deadline) {
        await delay(500);
        toolhiveReady = await detectToolhiveGateway();
      }
    }
    response.end(
      JSON.stringify({
        ok: true,
        available: toolhiveReady,
        hint: toolhiveReady
          ? ""
          : "The gateway did not answer within 8 seconds. If ToolHive has no cached login, run `thv llm login` in a terminal first, then re-check.",
      }),
    );
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
              ? daemonDefaults.toolhive.enabled
                ? "The ToolHive LLM gateway is not reachable right now"
                : "ToolHive gateway detection is switched off in Daemon defaults — enable it there first"
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
        // The choice is DURABLE from here: it is re-read at the next
        // controller boot (MECATL_STUDIO_PROVIDER, when set, still wins).
        // Persisting only after a successful start keeps a kind that could
        // not start out of the file. A failed write is logged, not fatal —
        // the daemon IS running on the new provider.
        try {
          applyDaemonDefaults({ ...daemonDefaults, activeProvider: kind });
          await persistDaemonDefaults(daemonDefaults);
        } catch (error) {
          process.stderr.write(
            `[daemon-defaults] active provider not persisted: ${error.message || error}\n`,
          );
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
  // Custom provider DEFINITION write (`providers add`, ADR 0238): POST
  // /providers/custom { id, apiFlavor, baseURL, defaultModel, authMethod }.
  // The definition is NON-secret by construction — an id, a wire flavor, an
  // HTTPS base URL, a model id and an auth METHOD — so the controller may
  // write it into the user-global settings.yaml (the file mecated reads at
  // the operator tier) the way it already writes the router/runtime files:
  // the same line-range discipline as removal (provider-auth.mjs), temp-file
  // + rename, the file's existing mode preserved (0600 when created). The
  // api_key itself still travels by hand into auth.yaml (Studio rule 3):
  // there is no key field here and none is accepted. Refused (409) while an
  // imported operator-settings.yaml is active — that CLI-tier file wins the
  // whole providers: section when it has one, and Studio never edits it —
  // and when the id is already configured anywhere. A keyless
  // (auth.method none) provider is complete on write, so the daemon
  // restarts at once; an api_key one waits for the key (the UI's Restart).
  // Checked BEFORE the /providers/{name} regex below, which would otherwise
  // read "custom" as a provider name.
  if (
    request.method === "POST" &&
    requestURL.pathname === "/providers/custom"
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
        (await readBody(request, 8192)).toString("utf8"),
      );
      const field = (key) =>
        typeof input?.[key] === "string" ? input[key].trim() : "";
      const definition = {
        id: field("id"),
        apiFlavor: field("apiFlavor"),
        baseURL: field("baseURL"),
        defaultModel: field("defaultModel"),
        authMethod: field("authMethod"),
      };
      const invalid = (message) =>
        Object.assign(new Error(message), { statusCode: 400 });
      if (!validCustomProviderId(definition.id))
        throw invalid(
          "not a valid custom provider id (lower-case letters, digits and hyphens, starting with a letter; built-in names are reserved)",
        );
      if (!CUSTOM_PROVIDER_API_FLAVORS.includes(definition.apiFlavor))
        throw invalid(
          `apiFlavor must be one of ${CUSTOM_PROVIDER_API_FLAVORS.join(", ")}`,
        );
      if (!validCustomProviderBaseURL(definition.baseURL))
        throw invalid(
          "baseURL must be an HTTPS URL without credentials, query, or fragment",
        );
      if (definition.defaultModel === "")
        throw invalid("defaultModel is required");
      if (
        definition.authMethod !== "api_key" &&
        definition.authMethod !== "none"
      )
        throw invalid('authMethod must be "api_key" or "none"');
      if (operatorSettingsActive)
        throw Object.assign(
          new Error(
            "Refused while an imported operator settings file is active: Studio does not write the settings file then. Add the providers: entry to that file by hand, then restart the daemon.",
          ),
          { statusCode: 409 },
        );
      const configured = new Set([
        ...(await listConfiguredProviderNames()),
        ...(await listCustomSettingsProviders()).map((entry) => entry.name),
      ]);
      if (configured.has(definition.id))
        throw Object.assign(
          new Error(
            `"${definition.id}" is already configured (an ${authFile} block or a providers: definition exists) — remove it first.`,
          ),
          { statusCode: 409 },
        );
      let currentText = "";
      let mode = 0o600;
      try {
        currentText = await readFile(userSettingsFile, "utf8");
        mode = (await stat(userSettingsFile)).mode & 0o777;
      } catch (error) {
        if (error?.code !== "ENOENT")
          throw Object.assign(
            new Error(
              `Could not read ${userSettingsFile}: ${error.message || error}`,
            ),
            { statusCode: 500 },
          );
      }
      const { text, written, reason } = upsertSettingsProvider(
        currentText,
        definition,
      );
      if (!written) throw Object.assign(new Error(reason), { statusCode: 409 });
      await mkdir(dirname(userSettingsFile), { recursive: true });
      const temp = `${userSettingsFile}.tmp`;
      await writeFile(temp, text, { mode });
      await rename(temp, userSettingsFile);
      // The definition STANDS from here: a failed restart is reported on
      // the success body (restarted:false + the cause), never as a failure
      // of the write the operator asked for.
      let restarted = false;
      let restartError = "";
      if (definition.authMethod === "none") {
        try {
          await queueRestart(() => startMecatl(preferredKind()));
          restarted = true;
        } catch (error) {
          restartError = error.message || String(error);
        }
      }
      response.end(
        JSON.stringify({
          ok: true,
          restarted,
          restartError,
          settingsFile: userSettingsFile,
        }),
      );
    } catch (error) {
      jsonError(
        response,
        error.statusCode || 400,
        error.message || "Could not save the provider definition",
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
      // Two scopes, the TUI's two verbs. `?scope=credential` (`providers
      // logout`) cuts ONLY the api_key line from the provider's auth.yaml
      // block — the block and, for a custom provider, its settings.yaml
      // definition stay, so it lists as "configured, no key". `?scope=all`
      // (`providers remove`, the default) cuts the whole auth.yaml block
      // AND a custom provider's definition from the user-global
      // settings.yaml (a built-in has no definition, so only the block
      // goes). Every cut is a conservative line-range edit
      // (provider-auth.mjs) written temp-file+rename — owner-only for
      // auth.yaml, the file's existing mode for settings.yaml — then a
      // daemon restart so the change is real. A definition living in the
      // ACTIVE imported operator-settings.yaml is refused (409) before
      // anything is written: Studio never edits that file. Removing the
      // LAST provider is allowed: mecated runs on the offline mock without
      // providers (the state the user already sees on first run) — the
      // UI's confirm warns, the controller doesn't refuse. If the restart
      // then fails (e.g. MECATL_STUDIO_PROVIDER still names the removed
      // provider), the removal STANDS — the operator asked for it to be
      // gone — and the startup error surfaces both in this response and on
      // /status.startupError. No value is ever logged or echoed.
      const scope = requestURL.searchParams.get("scope") || "all";
      if (scope !== "all" && scope !== "credential") {
        jsonError(response, 400, 'scope must be "credential" or "all"');
        return;
      }
      try {
        await queueRestart(async () => {
          const current = await readAuthFileText();
          let authText = current;
          let authChanged = false;
          if (scope === "credential") {
            const cut = removeAuthFileProviderKey(current, name);
            if (!cut.removed)
              throw Object.assign(
                new Error(
                  `No api_key found for providers.${name} in ${authFile}`,
                ),
                { statusCode: 404 },
              );
            authText = cut.text;
            authChanged = true;
          } else {
            if (operatorSettingsActive) {
              let operatorText = "";
              try {
                operatorText = await readFile(operatorSettingsFile, "utf8");
              } catch {
                // Unreadable = it defines nothing; the user-global file is
                // what mecated sees.
              }
              const owned = listSettingsProviders(operatorText);
              if (owned?.some((entry) => entry.name === name))
                throw Object.assign(
                  new Error(
                    `"${name}" is defined in the imported operator settings file (providers: section), which Studio never edits — refused while that file is active. Remove the entry there by hand, then restart the daemon.`,
                  ),
                  { statusCode: 409 },
                );
            }
            const cut = removeAuthFileProvider(current, name);
            authText = cut.text;
            authChanged = cut.removed;
            let settingsText = "";
            let settingsMode = 0o600;
            let hasSettings = false;
            try {
              settingsText = await readFile(userSettingsFile, "utf8");
              settingsMode = (await stat(userSettingsFile)).mode & 0o777;
              hasSettings = true;
            } catch (error) {
              if (error?.code !== "ENOENT")
                throw Object.assign(
                  new Error(
                    `Could not read ${userSettingsFile}: ${error.message || error}`,
                  ),
                  { statusCode: 500 },
                );
            }
            const definition = hasSettings
              ? removeSettingsProvider(settingsText, name)
              : { text: settingsText, removed: false };
            if (!authChanged && !definition.removed)
              throw Object.assign(
                new Error(
                  `No provider named "${name}" in ${authFile} or ${userSettingsFile}`,
                ),
                { statusCode: 404 },
              );
            if (definition.removed) {
              const temp = `${userSettingsFile}.tmp`;
              await writeFile(temp, definition.text, { mode: settingsMode });
              await rename(temp, userSettingsFile);
            }
          }
          if (authChanged) {
            const temp = `${authFile}.tmp`;
            await writeFile(temp, authText, { mode: 0o600 });
            await rename(temp, authFile);
          }
          // A DURABLE active-provider choice naming the removed provider
          // would wedge every later boot on a fail-fast --default-provider
          // (a keyed provider without its key is not startable either), so
          // it is cleared — and, when the whole provider goes, so is its
          // saved model pair (a credential-only cut keeps the pair with the
          // definition). The this-process override falls back too — unless
          // MECATL_STUDIO_PROVIDER pins it, which the UI's confirm warns
          // about and which the operator must change by hand.
          const dropModels =
            scope === "all" && Object.hasOwn(daemonDefaults.models, name);
          if (daemonDefaults.activeProvider === name || dropModels) {
            const { [name]: _dropped, ...models } = daemonDefaults.models;
            applyDaemonDefaults({
              ...daemonDefaults,
              models: dropModels ? models : daemonDefaults.models,
              activeProvider:
                daemonDefaults.activeProvider === name
                  ? null
                  : daemonDefaults.activeProvider,
            });
            await persistDaemonDefaults(daemonDefaults);
          }
          if (activeProviderOverride === name && configuredProvider !== name)
            activeProviderOverride = null;
          await startMecatl(preferredKind());
        });
        response.end(JSON.stringify({ ok: true, restarted: true, scope }));
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
  // Diagnostics options: log level, the loopback admin/metrics listener (+
  // perf MCP, goroutine alarm), product-metrics opt-out — spawn flags (see
  // src/lib/controller-diagnostics-options.mjs) — plus the controller-side
  // `quiet` switch. NOT in the header-free read-only allowlist (both verbs
  // need the server-set studio header). The POST takes a PARTIAL document;
  // a change to anything but `quiet` restarts the daemon, and a start
  // mecated refuses on the new flags rolls the previous document back,
  // restarts on it, and surfaces the refusal as the 400 body — the
  // /permissions rollback idiom. The operator posture is NOT accepted here
  // (mergeDiagnosticsOptions rejects the key): /permissions owns --posture.
  if (
    request.method === "GET" &&
    requestURL.pathname === "/diagnostics-options"
  ) {
    response.end(JSON.stringify(diagnosticsStatus()));
    return;
  }
  // The managed daemon's diagnostics log (see mecatedLogFile). GET /logs
  // serves the bounded in-memory tail (`?lines=`, default 200, max 2000)
  // with the file's path and size, the saved quiet/log-level knobs, liveness
  // and the last startup error — so the Diagnostics page can explain a
  // crash-at-start while the daemon itself is unreachable. GET
  // /logs/download streams the current generation as plain text (404 while
  // nothing has been written). The content is model-influenced, so the UI
  // renders it as text only; both routes need the server-set studio header
  // (neither is in the header-free read-only allowlist), and external mode
  // answers 409 at the proxy — the deployment owns its daemon's logging.
  if (request.method === "GET" && requestURL.pathname === "/logs") {
    try {
      const lines = parseTailLines(requestURL.searchParams.get("lines"));
      response.end(JSON.stringify(await daemonLogStatus(lines)));
    } catch (error) {
      jsonError(
        response,
        error.statusCode || 500,
        error.message || "Could not read the daemon log",
      );
    }
    return;
  }
  if (request.method === "GET" && requestURL.pathname === "/logs/download") {
    try {
      await stat(mecatedLogFile);
    } catch {
      jsonError(response, 404, "No daemon log has been written yet");
      return;
    }
    response.writeHead(200, {
      "Content-Type": "text/plain; charset=utf-8",
      "Content-Disposition": `attachment; filename="${LOG_FILE_NAME}"`,
      "Cache-Control": "no-store",
    });
    createReadStream(mecatedLogFile)
      .on("error", () => response.destroy())
      .pipe(response);
    return;
  }
  if (
    request.method === "POST" &&
    requestURL.pathname === "/diagnostics-options"
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
      const patch = JSON.parse(
        (await readBody(request, 16_384)).toString("utf8"),
      );
      await queueRestart(async () => {
        const previous = diagnosticsOptions;
        const next = normalizeDiagnosticsOptions(
          mergeDiagnosticsOptions(previous, patch),
        );
        diagnosticsOptions = next;
        await persistDiagnosticsOptions(next);
        if (!diagnosticsRestartRequired(previous, next)) return;
        try {
          await startMecatl(preferredKind());
        } catch (error) {
          diagnosticsOptions = previous;
          await persistDiagnosticsOptions(previous);
          await startMecatl(preferredKind());
          throw new Error(
            `${error.message || error} (previous diagnostics options restored)`,
          );
        }
      });
      response.end(JSON.stringify({ ok: true, ...diagnosticsStatus() }));
    } catch (error) {
      jsonError(
        response,
        error.statusCode || 400,
        error.message || "Could not update the diagnostics options",
      );
    }
    return;
  }
  // The runtime admin surface as the browser may see it (see
  // src/lib/controller-perf.mjs). GET /perf reports whether THIS child was
  // spawned with the loopback admin listener, the origin the controller
  // chose for it, the paths mecated mounts there and the perf MCP /
  // goroutine-alarm knobs. GET /perf/metrics and GET /perf/vars RELAY the
  // two TEXT endpoints — the browser's CSP is connect-src 'self', so it can
  // never fetch the loopback listener itself — verbatim (status + body,
  // redirects never followed), 409 while the surface is off and 503 while
  // no child is running. NEVER relayed: /debug/pprof and
  // /debug/flightrecorder (binary, potentially large — the Performance card
  // shows them as loopback links, which only resolve in a browser on the
  // daemon's host). /metrics can embed prompt text and file paths, so all
  // three routes need the server-set studio header (none is in the
  // header-free read-only allowlist), and external mode answers 409 at the
  // proxy — the deployment configures its own --metrics-addr / --perf-mcp.
  if (request.method === "GET" && requestURL.pathname === "/perf") {
    response.end(JSON.stringify(perfStatus(diagnosticsOptions, adminAddr)));
    return;
  }
  const perfRelay =
    request.method === "GET" ? perfProxyRoute(requestURL.pathname) : null;
  if (perfRelay) {
    if (adminAddr === "") {
      jsonError(response, 409, "the runtime admin surface is off");
      return;
    }
    if (!child) {
      jsonError(response, 503, "mecated is not running");
      return;
    }
    try {
      const upstream = await fetch(`http://${adminAddr}${perfRelay.path}`, {
        signal: AbortSignal.timeout(PERF_PROXY_TIMEOUT_MS),
        redirect: "manual",
      });
      const body = Buffer.from(await upstream.arrayBuffer());
      response.writeHead(upstream.status, {
        "Content-Type": perfRelay.contentType,
        "Cache-Control": "no-store",
      });
      response.end(body);
    } catch (error) {
      jsonError(
        response,
        502,
        `mecated's admin listener did not answer: ${error.message || error}`,
      );
    }
    return;
  }
  // Permissions: the operator posture ladder, project trust and shell-less
  // mode, as mecated spawn flags (see src/lib/controller-permissions.mjs).
  // Like /providers (and unlike /status) the GET is NOT in the header-free
  // read-only allowlist — it needs the server-set studio header. The write
  // restarts the daemon; a start the new flags make mecated refuse (auto/yolo
  // as root outside MECATL_SANDBOX) rolls the previous document back,
  // restarts on it, and surfaces the refusal as the 400 body.
  if (request.method === "GET" && requestURL.pathname === "/permissions") {
    response.end(
      JSON.stringify({
        config: permissionsConfig,
        trustOnce,
        operatorSettings: operatorSettingsActive,
      }),
    );
    return;
  }
  if (request.method === "POST" && requestURL.pathname === "/permissions") {
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
      const next = normalizePermissions(
        JSON.parse((await readBody(request, 16_384)).toString("utf8")),
      );
      // The trust ANCHOR is the controller's, never the client's: a FRESH
      // grant (the switch turning on) stamps the live anchor; a save that
      // merely keeps trustProject true (a posture or shell change) carries
      // the stored anchor forward, so an unrelated save can never quietly
      // re-accept drifted instructions — that takes the explicit
      // POST /permissions/trust below. Turning trust off clears it.
      next.trustAnchor = !next.trustProject
        ? ""
        : permissionsConfig.trustProject && permissionsConfig.trustAnchor
          ? permissionsConfig.trustAnchor
          : trustAnchor(workspace);
      await restartOnPermissions(next);
      response.end(JSON.stringify({ ok: true, config: permissionsConfig }));
    } catch (error) {
      jsonError(
        response,
        error.statusCode || 400,
        error.message || "Could not update the permissions",
      );
    }
    return;
  }
  // The trust prompt's two grant answers (the workspace banner; mecatui's
  // pre-TUI "trust / trust once / no"). Both are BODYLESS POSTs behind the
  // studio header; each restarts the daemon with --trust-project.
  //
  // /permissions/trust is the REMEMBERED grant: it persists trustProject
  // and stamps the LIVE anchor, so it is also how a drifted grant is
  // re-accepted after the user has reviewed the changed instructions. It
  // writes ONLY the controller's permissions.json — never the daemon's own
  // registry file.
  if (
    request.method === "POST" &&
    requestURL.pathname === "/permissions/trust"
  ) {
    try {
      await restartOnPermissions({
        ...permissionsConfig,
        trustProject: true,
        trustAnchor: trustAnchor(workspace),
      });
      response.end(JSON.stringify({ ok: true, config: permissionsConfig }));
    } catch (error) {
      jsonError(
        response,
        error.statusCode || 400,
        error.message || "Could not trust the project",
      );
    }
    return;
  }
  // /permissions/trust-once grants for THIS controller process only: the
  // in-memory flag rides every spawn until the controller exits and is
  // never persisted. A start the grant somehow makes mecated refuse drops
  // the grant again and restarts without it.
  if (
    request.method === "POST" &&
    requestURL.pathname === "/permissions/trust-once"
  ) {
    try {
      await queueRestart(async () => {
        trustOnce = true;
        try {
          await startMecatl(preferredKind());
        } catch (error) {
          trustOnce = false;
          await startMecatl(preferredKind());
          throw error;
        }
      });
      response.end(JSON.stringify({ ok: true }));
    } catch (error) {
      jsonError(
        response,
        error.statusCode || 400,
        error.message || "Could not trust the project for this session",
      );
    }
    return;
  }
  // Session store: POST /storage {persistence: "durable"|"memory", storeDir?}
  // (see src/lib/storage-settings.mjs). Header-gated like every write. The
  // daemon restarts on the new flag; a start the new document makes mecated
  // refuse (or a store directory that cannot be created) rolls the previous
  // document back — "no file" included — restarts on it, and surfaces the
  // failure as the 400 body. /status.storage is the read half.
  if (request.method === "POST" && requestURL.pathname === "/storage") {
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
      const next = normalizeStorageSettings(
        JSON.parse((await readBody(request, 16_384)).toString("utf8")),
        storageDefaults,
      );
      await queueRestart(async () => {
        const previous = storageSettings;
        storageSettings = next;
        await persistStorageSettings(next);
        try {
          await startMecatl(preferredKind());
        } catch (error) {
          storageSettings = previous;
          if (previous) await persistStorageSettings(previous);
          else await rm(storageStateFile, { force: true });
          await startMecatl(preferredKind());
          throw new Error(
            `${error.message || error} (previous session store restored)`,
          );
        }
      });
      response.end(
        JSON.stringify({ ok: true, storage: currentStorageStatus() }),
      );
    } catch (error) {
      jsonError(
        response,
        error.statusCode || 400,
        error.message || "Could not update the session store",
      );
    }
    return;
  }
  // Workspace root: POST /workspace { path } — the TUI's `--workspace`
  // deployment choice, made from Settings → Workspace. Header-gated like
  // every write (a loopback-origin page must never point mecated at a
  // directory of its choosing). The path is validated on the controller's
  // own filesystem (absolute, exists, a directory, not Studio's state dir;
  // src/lib/workspace-config.mjs) and the daemon restarts against it. A
  // start the new root makes mecated refuse rolls the previous root back,
  // restarts on it, and surfaces the failure as the 400 body.
  //
  // Trust does NOT travel with the root: a REMEMBERED grant was given for
  // the previous root's instruction files (its anchor is a content hash,
  // so two authority-less roots would otherwise read as the same grant),
  // and the this-process "trust once" likewise — both are withdrawn on a
  // change, and the new root's banner asks again if it carries authority.
  // /status.workspace is the read half; the same-root save is a no-op.
  if (request.method === "POST" && requestURL.pathname === "/workspace") {
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
      const body = JSON.parse(
        (await readBody(request, 16_384)).toString("utf8"),
      );
      const next = await validateWorkspacePath(body?.path, {
        realpath,
        stat,
        defaultWorkspace,
        forbidden: [studioStateDir],
      });
      let changed = false;
      if (next !== workspace) {
        await queueRestart(async () => {
          const previous = workspace;
          const previousPermissions = permissionsConfig;
          const previousTrustOnce = trustOnce;
          applyWorkspace(next);
          await persistWorkspace(next);
          trustOnce = false;
          if (permissionsConfig.trustProject) {
            permissionsConfig = {
              ...permissionsConfig,
              trustProject: false,
              trustAnchor: "",
            };
            await persistPermissions(permissionsConfig);
          }
          try {
            await startMecatl(preferredKind());
            changed = true;
          } catch (error) {
            applyWorkspace(previous);
            await persistWorkspace(previous);
            trustOnce = previousTrustOnce;
            if (permissionsConfig !== previousPermissions) {
              permissionsConfig = previousPermissions;
              await persistPermissions(permissionsConfig);
            }
            await startMecatl(preferredKind());
            throw new Error(
              `${error.message || error} (previous workspace root restored)`,
            );
          }
        });
      }
      response.end(
        JSON.stringify({ ok: true, changed, workspace, defaultWorkspace }),
      );
    } catch (error) {
      jsonError(
        response,
        error.statusCode || 400,
        error.message || "Could not change the workspace root",
      );
    }
    return;
  }
  // Daemon defaults: GET reads the saved document (header-free read-only,
  // like /status and /model-router); PUT replaces it whole, restarts mecated
  // on the new flags and rolls back to the previous document when the new
  // ones refuse to start (mecated validates --default-model /
  // --subagent-model fail-fast; its stderr excerpt is the 400 body). The
  // active provider is NOT settable here — POST /providers/active owns it
  // with its selectable-kind check — so the body's activeProvider is
  // replaced by the current one before normalisation.
  if (request.method === "GET" && requestURL.pathname === "/daemon-defaults") {
    response.end(
      JSON.stringify({ defaults: daemonDefaults, managedBy: "studio" }),
    );
    return;
  }
  if (request.method === "PUT" && requestURL.pathname === "/daemon-defaults") {
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
        (await readBody(request, 16_384)).toString("utf8"),
      );
      const next = normalizeDaemonDefaults(
        {
          ...(input && typeof input === "object" && !Array.isArray(input)
            ? input
            : {}),
          activeProvider: daemonDefaults.activeProvider,
        },
        { configDir: mecatlConfigDir },
      );
      await queueRestart(async () => {
        const previous = daemonDefaults;
        applyDaemonDefaults(next);
        await persistDaemonDefaults(next);
        try {
          await startMecatl(preferredKind());
        } catch (error) {
          applyDaemonDefaults(previous);
          await persistDaemonDefaults(previous);
          await startMecatl(preferredKind());
          throw error;
        }
      });
      response.end(JSON.stringify({ ok: true, defaults: daemonDefaults }));
    } catch (error) {
      jsonError(
        response,
        error.statusCode || 400,
        error.message || "Could not save the daemon defaults",
      );
    }
    return;
  }
  // Retention: POST /retention {main, child, scheduled: {maxAge?, maxCount?},
  // sweepCadence?, acknowledgeMainDeletion?} (see
  // src/lib/retention-settings.mjs). Header-gated like every write. The
  // document becomes mecated CLI flags on the restart; a start mecated refuses
  // (it re-checks the main-deletion acknowledgement itself) rolls the previous
  // document back — "no file" included — restarts on it, and surfaces the
  // failure as the 400 body. Refused while an imported operator settings file
  // is active: the controller passes no retention flags alongside that file.
  // /status.retention is the read half.
  if (request.method === "POST" && requestURL.pathname === "/retention") {
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
      if (operatorSettingsActive)
        throw new Error(
          "Retention is managed by the imported operator settings file while it is active. Edit its retention: block instead.",
        );
      const next = normalizeRetentionSettings(
        JSON.parse((await readBody(request, 16_384)).toString("utf8")),
      );
      await queueRestart(async () => {
        const previous = retentionSettings;
        retentionSettings = next;
        await persistRetentionSettings(next);
        try {
          await startMecatl(preferredKind());
        } catch (error) {
          retentionSettings = previous;
          if (previous) await persistRetentionSettings(previous);
          else await rm(retentionStateFile, { force: true });
          await startMecatl(preferredKind());
          throw new Error(
            `${error.message || error} (previous retention policy restored)`,
          );
        }
      });
      response.end(JSON.stringify({ ok: true, retention: retentionStatus() }));
    } catch (error) {
      jsonError(
        response,
        error.statusCode || 400,
        error.message || "Could not update the retention policy",
      );
    }
    return;
  }
  // Runtime settings: learning mode/sensitivity, the steer opt-out, the soul
  // flags (src/lib/runtime-settings.mjs). GET reads the saved document plus
  // the inherited settings.yaml values and the effective fold (header-gated:
  // the soul path and candidate list name files on this machine); PUT
  // replaces it whole, restarts mecated, and rolls the previous document
  // back when the new flags/file make it refuse to start. Learning is
  // refused (409) while an imported operator settings file is active — the
  // controller passes no learning file alongside it, so a save would lie.
  if (request.method === "GET" && requestURL.pathname === "/runtime-settings") {
    response.end(JSON.stringify(await runtimeSettingsDocument()));
    return;
  }
  if (request.method === "PUT" && requestURL.pathname === "/runtime-settings") {
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
      const next = normalizeRuntimeSettings(
        JSON.parse((await readBody(request, 16_384)).toString("utf8")),
      );
      if (
        operatorSettingsActive &&
        JSON.stringify(next.learning) !==
          JSON.stringify(runtimeSettings.learning)
      )
        throw Object.assign(
          new Error(
            "Learning mode and sensitivity are managed by the imported operator settings file while it is active. Edit its learning: block instead.",
          ),
          { statusCode: 409 },
        );
      if (next.soul.enabled && next.soul.file)
        await validateSoulFile(next.soul.file);
      await queueRestart(async () => {
        const previous = runtimeSettings;
        runtimeSettings = next;
        await persistRuntimeState(next);
        try {
          await startMecatl(preferredKind());
        } catch (error) {
          runtimeSettings = previous;
          await persistRuntimeState(previous);
          await startMecatl(preferredKind());
          throw new Error(
            `${error.message || error} (previous runtime settings restored)`,
          );
        }
      });
      response.end(JSON.stringify({ ok: true, config: runtimeSettings }));
    } catch (error) {
      jsonError(
        response,
        error.statusCode || 400,
        error.message || "Could not update the runtime settings",
      );
    }
    return;
  }
  // Accept the current soul as the drift baseline: ONE spawn with
  // --approve-soul (mecated rewrites <soul>.sha256), then the flag is gone
  // again. A failed start retries WITHOUT the flag so the daemon is never
  // left down by an approval. A no-op for a driver-provenance soul
  // (--soul-source-url) and while the soul is disabled — the UI offers it
  // only for a user/project soul.
  if (request.method === "POST" && requestURL.pathname === "/soul/approve") {
    try {
      if (!runtimeSettings.soul.enabled)
        throw Object.assign(
          new Error(
            "The persona is disabled; enable it before accepting a baseline.",
          ),
          { statusCode: 409 },
        );
      await queueRestart(async () => {
        approveSoulPending = true;
        try {
          await startMecatl(preferredKind());
        } catch (error) {
          approveSoulPending = false;
          await startMecatl(preferredKind());
          throw new Error(
            `${error.message || error} (restarted without --approve-soul)`,
          );
        } finally {
          approveSoulPending = false;
        }
      });
      response.end(JSON.stringify({ ok: true, restarted: true }));
    } catch (error) {
      jsonError(
        response,
        error.statusCode || 400,
        error.message || "Could not accept the persona baseline",
      );
    }
    return;
  }
  // Daemon options: the two memory stores, skill discovery, slash commands,
  // MCP discovery (src/lib/daemon-options.mjs). GET reads the saved document
  // plus the fallback locations and the directories this spawn resolved to
  // (studio-header-gated: it names directories on this machine); PUT
  // replaces it whole, restarts mecated, and rolls the previous document
  // back when the new flags make it refuse to start. Every set directory is
  // confined to the workspace / default root / mecatl config dir BEFORE
  // anything is persisted or created.
  if (request.method === "GET" && requestURL.pathname === "/daemon-options") {
    response.end(JSON.stringify(daemonOptionsDocument()));
    return;
  }
  if (request.method === "PUT" && requestURL.pathname === "/daemon-options") {
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
      const next = normalizeDaemonOptions(
        JSON.parse((await readBody(request, 16_384)).toString("utf8")),
      );
      await validateOptionDirs(next);
      await queueRestart(async () => {
        const previous = daemonOptions;
        applyDaemonOptions(next);
        await persistDaemonOptions(next);
        try {
          await startMecatl(preferredKind());
        } catch (error) {
          applyDaemonOptions(previous);
          await persistDaemonOptions(previous);
          await startMecatl(preferredKind());
          throw new Error(
            `${error.message || error} (previous daemon options restored)`,
          );
        }
      });
      response.end(JSON.stringify({ ok: true, options: daemonOptions }));
    } catch (error) {
      jsonError(
        response,
        error.statusCode || 400,
        error.message || "Could not update the daemon options",
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
  runtimeSettings = await loadRuntimeSettings();
  permissionsConfig = await loadPermissions();
  diagnosticsOptions = await loadDiagnosticsOptions();
  storageSettings = await loadStorageSettings();
  // The saved daemon options BEFORE the root: applyWorkspace derives the
  // skills/memory directories from both (an override, else the root's
  // pinned/default location).
  daemonOptions = await loadDaemonOptions();
  // The saved root (or the default) BEFORE the first spawn: the spawn
  // flags, the trust probes and the derived directories all read it.
  applyWorkspace(await loadWorkspace());
  retentionSettings = await loadRetentionSettings();
  applyDaemonDefaults(await loadDaemonDefaults());
  toolhiveReady = await detectToolhiveGateway();
  thvOnPath = await detectThvOnPath();
  process.stdout.write(
    toolhiveReady
      ? `ToolHive LLM gateway detected at ${toolhiveGatewayURL}\n`
      : `ToolHive LLM gateway not reachable at ${toolhiveGatewayURL} (start it with "thv llm proxy start"${thvOnPath ? ", or from Settings → Model provider" : ""}); falling back to the offline mock\n`,
  );
  // The DURABLE active-provider choice seeds the live selection unless
  // MECATL_STUDIO_PROVIDER pins one (env stays authoritative). A saved kind
  // that is not selectable right now (its auth.yaml block removed, its
  // gateway down) is dropped rather than handed to a fail-fast boot, so the
  // daemon comes up on the fallback instead of not at all.
  if (!configuredProvider && daemonDefaults.activeProvider) {
    const saved = daemonDefaults.activeProvider;
    if (isSelectableProviderKind(saved, await listSelectableProviderNames())) {
      activeProviderOverride = saved;
    } else {
      process.stderr.write(
        `[daemon-defaults] saved active provider "${saved}" is not selectable right now; falling back\n`,
      );
      applyDaemonDefaults({ ...daemonDefaults, activeProvider: null });
    }
  }
  try {
    await startMecatl(preferredKind());
  } catch (error) {
    if (!startupError) {
      // Not via startupFailure (a gateway handshake failure, say): record
      // it in the log file ourselves so the crash-at-start is on disk.
      startupError = error.message || "mecated could not start";
      recordDaemonLog(`[supervisor] ${startupError}\n`);
    }
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
