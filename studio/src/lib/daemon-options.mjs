/**
 * The controller's DAEMON OPTIONS — the mecated startup flags mecatui's
 * docs list for the tool catalog and the two memory stores that Studio had
 * no surface for: the per-project memory store (`--memory-dir`, omitted =
 * Remember/Recall off), the cross-project user model (`--no-user-model`,
 * `--user-model-dir`, `--user-model-review-interval`), skill discovery
 * (`--skills-dir`, omitted = the Skill tool off), slash-command templates
 * (`--commands-dir` / `--enable-commands`) and MCP discovery
 * (`--toolhive=false`, `--toolhive-group`, `--mcp-resource-tools=false`,
 * `--mcp-prompts=false`). Shared by the controller
 * (`scripts/local-controller.mjs`), the browser client
 * (`src/lib/harness/daemon-options.ts`) and the vitest suite — the same
 * dual-import pattern as runtime-settings.mjs — so the document shape, the
 * path grammar and the flag rendering cannot drift between the process that
 * spawns mecated and the form that edits the document.
 *
 * Every knob here is a SPAWN FLAG (the CLI tier out-ranks any settings
 * file per flag), so nothing is ever written into a settings.yaml. Knobs
 * with another owner are deliberately NOT here: `--no-shell` is the
 * permissions document's (Settings → Permissions), learning mode /
 * sensitivity, steer and the soul flags are runtime-settings.mjs's — one
 * writer per flag.
 *
 * The DEFAULT document renders the pre-feature command line byte for byte:
 * `--skills-dir <pinned>` and `--memory-dir <default>` and nothing else
 * (mecated's own defaults: user model on, commands off, ToolHive discovery
 * plus resource meta-tools and prompt expansion on).
 *
 * Directories are TRUST BOUNDARIES (mecated's flag help: a SKILL.md or a
 * command template steers the model like AGENTS.md). The shape check here
 * needs no filesystem — "" (the default location), an absolute POSIX path,
 * or a workspace-relative one with no `.`/`..` segments — and the controller
 * adds the checks that do: every set directory must sit under the live
 * workspace, the default workspace or the mecatl config dir, lexically AND
 * after symlink resolution, before it is created or handed to mecated.
 * This module never imports `node:*` because the browser bundles it.
 */

/** mecated's own default, and the value that emits no interval flag. */
export const DEFAULT_REVIEW_INTERVAL = 1;
/** A bound on `--user-model-review-interval`, not a daemon limit. */
export const MAX_REVIEW_INTERVAL = 1000;
/** The longest directory value accepted (a bound, not a filesystem limit). */
const DIR_MAX_CHARS = 512;
/** ToolHive group names: the workload-group grammar, bounded. */
export const TOOLHIVE_GROUP_PATTERN = /^[A-Za-z0-9._-]{0,64}$/;
/** mecated's default command directories when `--enable-commands` is passed
 *  with no `--commands-dir` (cmd/mecated flag help). */
export const DEFAULT_COMMAND_DIRS = Object.freeze([
  ".mecatl/commands",
  ".claude/commands",
]);

/** The document every field defaults to: byte-identical spawn arguments to
 *  the pre-feature controller. */
export const DEFAULT_DAEMON_OPTIONS = Object.freeze({
  projectMemory: Object.freeze({ enabled: true, dir: "" }),
  userModel: Object.freeze({
    enabled: true,
    dir: "",
    reviewInterval: DEFAULT_REVIEW_INTERVAL,
  }),
  skills: Object.freeze({ enabled: true, dir: "" }),
  commands: Object.freeze({ enabled: false, dir: "" }),
  mcp: Object.freeze({
    toolhive: true,
    toolhiveGroup: "",
    resourceTools: true,
    prompts: true,
  }),
});

const bad = (message) => Object.assign(new Error(message), { statusCode: 400 });

/** True when the string carries a C0 control character (NUL, newline, tab,
 *  ...) or DEL — none of which belongs in a path a browser hands over. */
const hasControlCharacter = (text) =>
  Array.from(text).some((char) => {
    const code = char.charCodeAt(0);
    return code < 0x20 || code === 0x7f;
  });

const section = (raw, name) => {
  if (raw === undefined || raw === null) return {};
  if (typeof raw !== "object" || Array.isArray(raw))
    throw bad(`${name} must be an object`);
  return raw;
};

const flag = (raw, name, fallback) => {
  if (raw === undefined || raw === null) return fallback;
  if (typeof raw !== "boolean") throw bad(`${name} must be true or false`);
  return raw;
};

/**
 * The shape check on a directory value: "" (the default location) or a
 * POSIX path — absolute, or relative to the workspace — with no empty, `.`
 * or `..` segments and no control characters, at most 512 characters. A
 * trailing slash is dropped. Throws a 400-shaped error naming the field.
 * @param {unknown} raw
 * @param {string} name
 * @returns {string}
 */
export function normalizeOptionDir(raw, name) {
  if (raw === undefined || raw === null) return "";
  if (typeof raw !== "string") throw bad(`${name} must be a string`);
  if (hasControlCharacter(raw))
    throw bad(`${name} must not contain control characters`);
  let value = raw.trim();
  if (value === "") return "";
  if (value.length > DIR_MAX_CHARS)
    throw bad(`${name} must be at most ${DIR_MAX_CHARS} characters`);
  if (value.length > 1 && value.endsWith("/")) value = value.slice(0, -1);
  if (value === "/") throw bad(`${name} must not be the filesystem root`);
  const segments = value.startsWith("/")
    ? value.split("/").slice(1)
    : value.split("/");
  if (
    segments.some(
      (segment) => segment === "" || segment === "." || segment === "..",
    )
  )
    throw bad(`${name} must not contain empty, . or .. path segments`);
  return value;
}

/**
 * A saved document or PUT body normalised to the exact shape the controller
 * keeps and the client types, or a 400-shaped throw naming the field.
 * Unknown keys are dropped; absent sections take their defaults.
 * @param {unknown} input
 * @returns {{projectMemory: {enabled: boolean, dir: string},
 *            userModel: {enabled: boolean, dir: string, reviewInterval: number},
 *            skills: {enabled: boolean, dir: string},
 *            commands: {enabled: boolean, dir: string},
 *            mcp: {toolhive: boolean, toolhiveGroup: string,
 *                  resourceTools: boolean, prompts: boolean}}}
 */
export function normalizeDaemonOptions(input) {
  const body = section(input, "daemon options");
  const projectMemory = section(body.projectMemory, "projectMemory");
  const userModel = section(body.userModel, "userModel");
  const skills = section(body.skills, "skills");
  const commands = section(body.commands, "commands");
  const mcp = section(body.mcp, "mcp");
  const d = DEFAULT_DAEMON_OPTIONS;

  let reviewInterval = userModel.reviewInterval;
  if (reviewInterval === undefined || reviewInterval === null)
    reviewInterval = d.userModel.reviewInterval;
  if (typeof reviewInterval === "string" && /^\d+$/.test(reviewInterval.trim()))
    reviewInterval = Number(reviewInterval.trim());
  if (
    typeof reviewInterval !== "number" ||
    !Number.isInteger(reviewInterval) ||
    reviewInterval < 1 ||
    reviewInterval > MAX_REVIEW_INTERVAL
  )
    throw bad(
      `userModel.reviewInterval must be a whole number from 1 to ${MAX_REVIEW_INTERVAL}`,
    );

  let toolhiveGroup = mcp.toolhiveGroup;
  if (toolhiveGroup === undefined || toolhiveGroup === null) toolhiveGroup = "";
  if (typeof toolhiveGroup !== "string")
    throw bad("mcp.toolhiveGroup must be a string");
  toolhiveGroup = toolhiveGroup.trim();
  if (!TOOLHIVE_GROUP_PATTERN.test(toolhiveGroup))
    throw bad(
      "mcp.toolhiveGroup may only use letters, digits, dots, hyphens and underscores (max 64 characters)",
    );

  return {
    projectMemory: {
      enabled: flag(
        projectMemory.enabled,
        "projectMemory.enabled",
        d.projectMemory.enabled,
      ),
      dir: normalizeOptionDir(projectMemory.dir, "projectMemory.dir"),
    },
    userModel: {
      enabled: flag(
        userModel.enabled,
        "userModel.enabled",
        d.userModel.enabled,
      ),
      dir: normalizeOptionDir(userModel.dir, "userModel.dir"),
      reviewInterval,
    },
    skills: {
      enabled: flag(skills.enabled, "skills.enabled", d.skills.enabled),
      dir: normalizeOptionDir(skills.dir, "skills.dir"),
    },
    commands: {
      enabled: flag(commands.enabled, "commands.enabled", d.commands.enabled),
      dir: normalizeOptionDir(commands.dir, "commands.dir"),
    },
    mcp: {
      toolhive: flag(mcp.toolhive, "mcp.toolhive", d.mcp.toolhive),
      toolhiveGroup,
      resourceTools: flag(
        mcp.resourceTools,
        "mcp.resourceTools",
        d.mcp.resourceTools,
      ),
      prompts: flag(mcp.prompts, "mcp.prompts", d.mcp.prompts),
    },
  };
}

/** Collapses repeated slashes and drops a trailing one (POSIX, lexical). */
const tidy = (path) => {
  const collapsed = path.replace(/\/{2,}/g, "/");
  return collapsed.length > 1 && collapsed.endsWith("/")
    ? collapsed.slice(0, -1)
    : collapsed;
};

/**
 * The absolute directory a normalised value names: "" stays "" (the
 * default location), an absolute path stands, a relative one joins onto
 * the workspace. Lexical only — the segment grammar above already forbids
 * `.`/`..`, so no further normalisation is needed.
 * @param {string} dir
 * @param {string} workspace
 * @returns {string}
 */
export function resolveOptionDir(dir, workspace) {
  if (!dir) return "";
  return dir.startsWith("/") ? tidy(dir) : tidy(`${workspace}/${dir}`);
}

/**
 * True when `path` equals one of `roots` or sits under it (lexical,
 * separator-aware). The controller pairs it with a realpath() re-check so a
 * symlinked parent cannot escape.
 * @param {string} path
 * @param {readonly string[]} roots
 * @returns {boolean}
 */
export function optionDirWithinRoots(path, roots) {
  return roots.some((root) => {
    const base = tidy(root);
    return base !== "/" && (path === base || path.startsWith(`${base}/`));
  });
}

/**
 * Where a resolved directory lives, for `/status`: "project" under the live
 * workspace, "user" under the mecatl config dir, "studio" under the default
 * workspace's state (a non-default root's memory store), "other" otherwise.
 * @param {string} path
 * @param {{workspace: string, defaultWorkspace?: string, configDir?: string}} roots
 * @returns {"project" | "user" | "studio" | "other"}
 */
export function optionDirScope(
  path,
  { workspace, defaultWorkspace = "", configDir = "" },
) {
  if (optionDirWithinRoots(path, [workspace])) return "project";
  if (configDir && optionDirWithinRoots(path, [configDir])) return "user";
  if (defaultWorkspace && optionDirWithinRoots(path, [defaultWorkspace]))
    return "studio";
  return "other";
}

/**
 * The directories a document resolves to for THIS spawn: the skills and
 * memory stores always have one (the override, else the controller's
 * pinned/default location — reported even while the store is disabled, so
 * a re-enable lands where the user expects), the user-model and commands
 * directories only when overridden ("" = mecated's own default).
 * @param {ReturnType<typeof normalizeDaemonOptions>} options
 * @param {{workspace: string, pinnedSkillsDir: string, defaultMemoryDir: string}} context
 * @returns {{skillsDir: string, memoryDir: string, userModelDir: string, commandsDir: string}}
 */
export function effectiveDaemonDirs(
  options,
  { workspace, pinnedSkillsDir, defaultMemoryDir },
) {
  return {
    skillsDir:
      resolveOptionDir(options.skills.dir, workspace) || pinnedSkillsDir,
    memoryDir:
      resolveOptionDir(options.projectMemory.dir, workspace) ||
      defaultMemoryDir,
    userModelDir: resolveOptionDir(options.userModel.dir, workspace),
    commandsDir: resolveOptionDir(options.commands.dir, workspace),
  };
}

/**
 * The spawn flags a document becomes, in a fixed order. Absence is
 * meaningful twice: no `--memory-dir` is how mecated turns Remember/Recall
 * OFF, and no `--skills-dir` is how it drops the Skill tool
 * (`--skills-conventional` is NEVER passed — studio/CLAUDE.md rule 12).
 * `--no-user-model` wins in mecated, so the user-model dir/interval are
 * emitted only while the store is enabled; the commands dir is emitted
 * only while commands are enabled (the switch is the one control).
 * `--toolhive-group` is only consulted while discovery is on, so it rides
 * only then. The DEFAULT document renders exactly
 * `["--skills-dir", pinnedSkillsDir, "--memory-dir", defaultMemoryDir]`.
 * @param {ReturnType<typeof normalizeDaemonOptions>} options
 * @param {{workspace: string, pinnedSkillsDir: string, defaultMemoryDir: string}} context
 * @returns {string[]}
 */
export function daemonOptionArgs(options, context) {
  const dirs = effectiveDaemonDirs(options, context);
  const args = [];
  if (options.skills.enabled) args.push("--skills-dir", dirs.skillsDir);
  if (options.projectMemory.enabled) args.push("--memory-dir", dirs.memoryDir);
  if (!options.userModel.enabled) {
    args.push("--no-user-model");
  } else {
    if (dirs.userModelDir) args.push("--user-model-dir", dirs.userModelDir);
    if (options.userModel.reviewInterval !== DEFAULT_REVIEW_INTERVAL)
      args.push(
        "--user-model-review-interval",
        String(options.userModel.reviewInterval),
      );
  }
  if (options.commands.enabled) {
    if (dirs.commandsDir) args.push("--commands-dir", dirs.commandsDir);
    else args.push("--enable-commands");
  }
  if (!options.mcp.toolhive) args.push("--toolhive=false");
  else if (options.mcp.toolhiveGroup)
    args.push("--toolhive-group", options.mcp.toolhiveGroup);
  if (!options.mcp.resourceTools) args.push("--mcp-resource-tools=false");
  if (!options.mcp.prompts) args.push("--mcp-prompts=false");
  return args;
}

/** The set (non-empty) directory values of a document with their field
 *  names — what the controller's filesystem check iterates. */
export function daemonOptionDirFields(options) {
  return [
    ["projectMemory.dir", options.projectMemory.dir],
    ["userModel.dir", options.userModel.dir],
    ["skills.dir", options.skills.dir],
    ["commands.dir", options.commands.dir],
  ].filter(([, dir]) => dir !== "");
}
