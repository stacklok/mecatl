import {
  DEFAULT_COMMAND_DIRS,
  DEFAULT_DAEMON_OPTIONS,
  DEFAULT_REVIEW_INTERVAL,
  MAX_REVIEW_INTERVAL,
} from "@/lib/daemon-options.mjs";
import { apiError } from "./errors";

/**
 * The managed daemon's DAEMON OPTIONS — the mecated startup flags for the
 * two memory stores (`--memory-dir`, `--no-user-model`, `--user-model-dir`,
 * `--user-model-review-interval`), skill discovery (`--skills-dir`), slash
 * commands (`--commands-dir` / `--enable-commands`) and MCP discovery
 * (`--toolhive`, `--toolhive-group`, `--mcp-resource-tools`,
 * `--mcp-prompts`), owned by the controller (`GET|PUT /daemon-options`),
 * never a daemon route. Every knob is a spawn flag; the browser never
 * composes a flag itself — it edits this document and the controller
 * renders the command line (`src/lib/daemon-options.mjs`). Every write
 * RESTARTS the daemon (in-flight runs end) and the controller rolls back a
 * document mecated refuses to start on. External mode answers 409 for both
 * verbs: the deployment owns its own flags.
 *
 * Directories are display paths on the operator's OWN machine (managed mode
 * only), confined server-side to the workspace, the default root or the
 * mecatl config dir; no directory CONTENT ever travels here. The daemon's
 * own capability document (`serverCapabilities.memory` / `user_model` /
 * `skills` / `slash_commands` / `bash`) remains the effective truth of what
 * the running daemon registered.
 */

const CONTROL_API = "/api/mecatl-control";

/** The controller's saved document (PUT /daemon-options body). */
export interface HarnessDaemonOptions {
  /** The per-project Remember/Recall store: off → no `--memory-dir` at all. */
  projectMemory: { enabled: boolean; dir: string };
  /** The cross-project user model: off → `--no-user-model`. */
  userModel: { enabled: boolean; dir: string; reviewInterval: number };
  /** Skill discovery: off → no `--skills-dir`, which drops the Skill tool. */
  skills: { enabled: boolean; dir: string };
  /** Slash-command templates: on → `--commands-dir <dir>` or
   *  `--enable-commands` (mecated's default directories). */
  commands: { enabled: boolean; dir: string };
  mcp: {
    /** ToolHive workload discovery (`--toolhive`, default on). */
    toolhive: boolean;
    /** "" = ToolHive's "default" group. */
    toolhiveGroup: string;
    /** The ListMcpResources/ReadMcpResource meta-tools. */
    resourceTools: boolean;
    /** `/mcp__<server>__<prompt>` expansion. */
    prompts: boolean;
  };
}

/** GET /daemon-options: the document plus what it lands on. */
export interface HarnessDaemonOptionsDoc {
  options: HarnessDaemonOptions;
  /** The locations an empty `dir` falls back to — the form's placeholders. */
  defaults: {
    skillsDir: string;
    memoryDir: string;
    userModelDir: string;
    /** mecated's default command directories (workspace-relative). */
    commandDirs: readonly string[];
  };
  /** The directories THIS spawn resolved to ("" = mecated's own default
   *  for the user model / commands). */
  effective: {
    skillsDir: string;
    memoryDir: string;
    userModelDir: string;
    commandsDir: string;
  };
  /** Where a directory may sit (the controller refuses anything outside). */
  allowedRoots: string[];
}

export const EMPTY_DAEMON_OPTIONS: HarnessDaemonOptions = {
  projectMemory: { ...DEFAULT_DAEMON_OPTIONS.projectMemory },
  userModel: { ...DEFAULT_DAEMON_OPTIONS.userModel },
  skills: { ...DEFAULT_DAEMON_OPTIONS.skills },
  commands: { ...DEFAULT_DAEMON_OPTIONS.commands },
  mcp: { ...DEFAULT_DAEMON_OPTIONS.mcp },
};

const asRecord = (raw: unknown): Record<string, unknown> =>
  raw && typeof raw === "object" ? (raw as Record<string, unknown>) : {};

const bool = (raw: unknown, fallback: boolean) =>
  typeof raw === "boolean" ? raw : fallback;

const text = (raw: unknown) => (typeof raw === "string" ? raw : "");

const interval = (raw: unknown) =>
  typeof raw === "number" &&
  Number.isInteger(raw) &&
  raw >= 1 &&
  raw <= MAX_REVIEW_INTERVAL
    ? raw
    : DEFAULT_REVIEW_INTERVAL;

/**
 * Decodes a saved document defensively: a non-boolean flag or an
 * out-of-range interval falls back to the default rather than reaching a
 * control that cannot show it.
 */
export function readDaemonOptions(raw: unknown): HarnessDaemonOptions {
  const body = asRecord(raw);
  const projectMemory = asRecord(body.projectMemory);
  const userModel = asRecord(body.userModel);
  const skills = asRecord(body.skills);
  const commands = asRecord(body.commands);
  const mcp = asRecord(body.mcp);
  const d = EMPTY_DAEMON_OPTIONS;
  return {
    projectMemory: {
      enabled: bool(projectMemory.enabled, d.projectMemory.enabled),
      dir: text(projectMemory.dir),
    },
    userModel: {
      enabled: bool(userModel.enabled, d.userModel.enabled),
      dir: text(userModel.dir),
      reviewInterval: interval(userModel.reviewInterval),
    },
    skills: {
      enabled: bool(skills.enabled, d.skills.enabled),
      dir: text(skills.dir),
    },
    commands: {
      enabled: bool(commands.enabled, d.commands.enabled),
      dir: text(commands.dir),
    },
    mcp: {
      toolhive: bool(mcp.toolhive, d.mcp.toolhive),
      toolhiveGroup: text(mcp.toolhiveGroup),
      resourceTools: bool(mcp.resourceTools, d.mcp.resourceTools),
      prompts: bool(mcp.prompts, d.mcp.prompts),
    },
  };
}

/** Decodes GET /daemon-options, filling anything an older controller does
 *  not report with the empty/default shape. */
export function readDaemonOptionsDoc(raw: unknown): HarnessDaemonOptionsDoc {
  const body = asRecord(raw);
  const defaults = asRecord(body.defaults);
  const effective = asRecord(body.effective);
  const commandDirs = Array.isArray(defaults.commandDirs)
    ? defaults.commandDirs.filter((d): d is string => typeof d === "string")
    : [];
  return {
    options: readDaemonOptions(body.options),
    defaults: {
      skillsDir: text(defaults.skillsDir),
      memoryDir: text(defaults.memoryDir),
      userModelDir: text(defaults.userModelDir),
      commandDirs: commandDirs.length ? commandDirs : DEFAULT_COMMAND_DIRS,
    },
    effective: {
      skillsDir: text(effective.skillsDir),
      memoryDir: text(effective.memoryDir),
      userModelDir: text(effective.userModelDir),
      commandsDir: text(effective.commandsDir),
    },
    allowedRoots: Array.isArray(body.allowedRoots)
      ? body.allowedRoots.filter((r): r is string => typeof r === "string")
      : [],
  };
}

/** Reads the saved document plus its fallbacks and effective directories. */
export async function fetchHarnessDaemonOptions(
  signal?: AbortSignal,
): Promise<HarnessDaemonOptionsDoc> {
  const response = await fetch(`${CONTROL_API}/daemon-options`, {
    cache: "no-store",
    signal,
  });
  if (!response.ok) throw await apiError(response);
  return readDaemonOptionsDoc(await response.json().catch(() => null));
}

/**
 * Replaces the document whole. RESTARTS the daemon (in-flight runs end);
 * a document mecated refuses to start on is rolled back by the controller
 * and surfaces here as the thrown error, as does a directory outside the
 * allowed roots (400) or external mode (409).
 */
export async function saveHarnessDaemonOptions(
  options: HarnessDaemonOptions,
): Promise<HarnessDaemonOptions> {
  const response = await fetch(`${CONTROL_API}/daemon-options`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(options),
  });
  if (!response.ok) throw await apiError(response);
  const body = asRecord(await response.json().catch(() => null));
  return readDaemonOptions(body.options ?? options);
}
