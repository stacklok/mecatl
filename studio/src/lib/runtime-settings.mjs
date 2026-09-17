/**
 * The controller's RUNTIME SETTINGS — the handful of daemon knobs mecatui
 * exposes as slash commands and flags that Studio had no surface for:
 * learning mode / sensitivity (`/learning`, `/learning-sensitivity`), the
 * mid-run steer opt-out (`--no-steer`), and the persona/soul flags
 * (`--no-soul`, `--soul-strict`, `--soul-file`, the one-shot
 * `--approve-soul`). Shared by the controller (`scripts/local-controller.mjs`),
 * the browser client (`src/lib/harness/runtime-settings.ts`) and the vitest
 * suite, the same dual-import pattern as retention-settings.mjs, so the
 * vocabularies, the flag grammar and the YAML the daemon reads cannot drift
 * between the process that spawns mecated and the form that validates a draft.
 *
 * Two carriers, chosen per knob by what mecated accepts:
 *
 * - Steer and every soul knob are SPAWN FLAGS. The CLI out-ranks any
 *   settings file per flag, so they hold even under an imported
 *   operator-settings.yaml, and `--no-steer` can only ever TIGHTEN (steer
 *   is on by default; a `steer: false` in the operator's own file still
 *   stands when Studio leaves it enabled).
 * - Learning mode / sensitivity have NO flag: mecated reads them only from
 *   the `learning:` section of a settings file, captured WHOLE-BLOCK,
 *   first file wins, CLI-tier files before the user-global one
 *   (`internal/adapter/permconfig/resolve.go`, `captureLearning`). A
 *   CLI-tier file carrying just `learning: {mode: review}` would therefore
 *   make the daemon drop the operator's `skills:` and `automatic:` budgets
 *   silently. So the rendered file is the FULL merged section: Studio's
 *   mode/sensitivity override the user-global values, every other child of
 *   the user-global block is carried verbatim (`readLearningBlock` +
 *   `renderRuntimeSettingsYAML`). The user-global settings.yaml itself is
 *   never edited — unlike mecatui, which rewrites it in place.
 *
 * "" for a learning field means "leave whatever the operator's settings.yaml
 * says (or the daemon default)". Nothing here is a credential.
 */

/** mecated's closed learning-mode vocabulary (engine/learning ParseMode). */
export const LEARNING_MODES = Object.freeze(["off", "review", "auto"]);

/** mecated's closed sensitivity vocabulary (engine/learning ParseSensitivity). */
export const LEARNING_SENSITIVITIES = Object.freeze([
  "conservative",
  "balanced",
  "eager",
]);

/** The daemon's load-time byte ceiling on a soul body
 *  (`internal/adapter/soul/store.go`, DefaultMaxBytes): over it the soul is
 *  REJECTED, not truncated, so a save must refuse such a file up front. */
export const SOUL_MAX_BYTES = 20 * 1024;

/** The longest soul path accepted (a bound, not a filesystem limit). */
const SOUL_PATH_MAX_CHARS = 1024;

/** The document every field defaults to: nothing overridden, steer on, the
 *  conventional soul loaded fail-soft, drift warned about rather than
 *  refused. Byte-identical spawn arguments to the pre-feature controller. */
export const DEFAULT_RUNTIME_SETTINGS = Object.freeze({
  learning: Object.freeze({ mode: "", sensitivity: "" }),
  steer: Object.freeze({ enabled: true }),
  soul: Object.freeze({ enabled: true, strict: false, file: "" }),
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

const vocab = (raw, name, allowed) => {
  if (raw === undefined || raw === null) return "";
  if (typeof raw !== "string") throw bad(`${name} must be a string`);
  const value = raw.trim().toLowerCase();
  if (value === "") return "";
  if (!allowed.includes(value))
    throw bad(`${name} must be one of ${allowed.join(", ")} (or empty)`);
  return value;
};

/**
 * The shape check on a soul path that needs no filesystem: "" (the
 * conventional location) or an absolute POSIX path to a `.md` file whose
 * name is not a dotfile, with no `.`/`..` segments and no control
 * characters. The controller adds the checks that DO need the filesystem
 * (an allow-listed root after symlink resolution, a regular file, the byte
 * cap). Throws a 400-shaped error naming the reason.
 * @param {unknown} raw
 * @returns {string}
 */
export function normalizeSoulFile(raw) {
  if (raw === undefined || raw === null) return "";
  if (typeof raw !== "string") throw bad("soul.file must be a string");
  if (hasControlCharacter(raw))
    throw bad("soul.file must not contain control characters");
  const value = raw.trim();
  if (value === "") return "";
  if (value.length > SOUL_PATH_MAX_CHARS)
    throw bad(`soul.file must be at most ${SOUL_PATH_MAX_CHARS} characters`);
  if (!value.startsWith("/")) throw bad("soul.file must be an absolute path");
  const segments = value.split("/").slice(1);
  if (
    segments.some(
      (segment) => segment === "" || segment === "." || segment === "..",
    )
  )
    throw bad("soul.file must not contain empty, . or .. path segments");
  const name = segments[segments.length - 1];
  if (name.startsWith(".")) throw bad("soul.file must not name a dotfile");
  if (!name.endsWith(".md") || name === ".md")
    throw bad("soul.file must be a Markdown (.md) file");
  return value;
}

/**
 * True when `file` (already normalised) sits strictly INSIDE one of `roots`
 * — a lexical containment check the controller pairs with a realpath()
 * re-check so a symlinked parent cannot escape.
 * @param {string} file
 * @param {readonly string[]} roots
 * @returns {boolean}
 */
export function soulFileWithinRoots(file, roots) {
  return roots.some((root) => {
    const base = root.endsWith("/") ? root : `${root}/`;
    return base !== "/" && file.startsWith(base);
  });
}

/**
 * A saved document or PUT body normalised to the exact shape the controller
 * keeps and the client types, or a 400-shaped throw naming the field.
 * Unknown keys are dropped; absent sections take their defaults.
 * @param {unknown} input
 * @returns {{learning: {mode: string, sensitivity: string},
 *            steer: {enabled: boolean},
 *            soul: {enabled: boolean, strict: boolean, file: string}}}
 */
export function normalizeRuntimeSettings(input) {
  const body = section(input, "runtime settings");
  const learning = section(body.learning, "learning");
  const steer = section(body.steer, "steer");
  const soul = section(body.soul, "soul");
  return {
    learning: {
      mode: vocab(learning.mode, "learning.mode", LEARNING_MODES),
      sensitivity: vocab(
        learning.sensitivity,
        "learning.sensitivity",
        LEARNING_SENSITIVITIES,
      ),
    },
    steer: { enabled: flag(steer.enabled, "steer.enabled", true) },
    soul: {
      enabled: flag(soul.enabled, "soul.enabled", true),
      strict: flag(soul.strict, "soul.strict", false),
      file: normalizeSoulFile(soul.file),
    },
  };
}

/**
 * True when the document overrides a learning field and so needs the
 * rendered CLI-tier settings file passed to mecated at all.
 * @param {{learning: {mode: string, sensitivity: string}}} config
 * @returns {boolean}
 */
export function runtimeSettingsHasYAML(config) {
  return Boolean(config?.learning?.mode || config?.learning?.sensitivity);
}

/**
 * The spawn flags a document becomes. `--no-steer` only when steer is off;
 * the soul flags only while the soul is enabled (`--no-soul` wins in
 * mecated anyway, so emitting the others alongside it would only mislead
 * someone reading the command line); `--approve-soul` exactly when the
 * caller asks for this ONE spawn — it rewrites the drift baseline, so it
 * must never be persisted.
 * @param {{steer: {enabled: boolean},
 *          soul: {enabled: boolean, strict: boolean, file: string}}} config
 * @param {{approveSoul?: boolean}} [options]
 * @returns {string[]}
 */
export function runtimeSettingsArgs(config, { approveSoul = false } = {}) {
  const args = [];
  if (!config.steer.enabled) args.push("--no-steer");
  if (!config.soul.enabled) {
    args.push("--no-soul");
    return args;
  }
  if (config.soul.strict) args.push("--soul-strict");
  if (config.soul.file) args.push("--soul-file", config.soul.file);
  if (approveSoul) args.push("--approve-soul");
  return args;
}

/** A settings scalar as a YAML reader would see it: trimmed, matched quotes
 *  stripped, a comment-only remainder read as "". */
function settingsScalar(raw) {
  const value = (raw ?? "").trim();
  if (value === "" || value.startsWith("#")) return "";
  const quote = value[0];
  if (quote === '"' || quote === "'") {
    const close = value.indexOf(quote, 1);
    return close === -1 ? value.slice(1) : value.slice(1, close);
  }
  const comment = value.search(/\s#/);
  return comment === -1 ? value : value.slice(0, comment).trim();
}

const neutralLine = (line) => line.trim() === "" || /^\s*#/.test(line);

/**
 * The top-level `learning:` section of a settings.yaml text: the two
 * scalars Studio overrides plus EVERY OTHER LINE of the block verbatim
 * (`skills:`, `automatic:`, comments), so a rendered CLI-tier file can carry
 * them and mecated's whole-block capture loses nothing. A line scan, never a
 * YAML parse, like provider-auth.mjs. Returns NULL when the text has no
 * top-level `learning:` key at all — distinct from an empty block — because
 * mecated captures the section first-non-nil across its operator-tier files
 * and a caller folding several sources needs the same distinction. A
 * `mode:`/`sensitivity:` at deeper indentation (a nested key) is carried,
 * not read.
 * @param {string} text
 * @returns {{mode: string, sensitivity: string, extraLines: string[]} | null}
 */
export function readLearningBlock(text) {
  const lines = String(text ?? "").split("\n");
  const at = lines.findIndex((line) =>
    /^learning:\s*(\{\s*\}\s*)?(#.*)?$/.test(line),
  );
  if (at === -1) return null;
  const block = { mode: "", sensitivity: "", extraLines: [] };
  let childIndent = -1;
  const pending = [];
  for (const line of lines.slice(at + 1)) {
    if (neutralLine(line)) {
      pending.push(line);
      continue;
    }
    if (/^\S/.test(line)) break; // dedented past the block
    const indent = line.match(/^ */)[0].length;
    if (childIndent === -1) childIndent = indent;
    block.extraLines.push(...pending.splice(0));
    if (indent === childIndent) {
      const scalar = line.match(/^ *(mode|sensitivity):(.*)$/);
      if (scalar) {
        block[scalar[1]] = settingsScalar(scalar[2]).toLowerCase();
        continue;
      }
    }
    block.extraLines.push(line);
  }
  return block;
}

/**
 * The top-level `steer:` scalar of a settings.yaml text: true/false, or
 * null when absent or not a plain boolean. The operator-tier `steer: false`
 * disables the mid-run steer inbox even when Studio leaves it on, so the
 * read half reports it; the daemon's own `capabilities.steer` remains the
 * effective truth.
 * @param {string} text
 * @returns {boolean | null}
 */
export function readSteerScalar(text) {
  for (const line of String(text ?? "").split("\n")) {
    const match = line.match(/^steer:(.*)$/);
    if (!match) continue;
    const value = settingsScalar(match[1]).toLowerCase();
    if (value === "true") return true;
    if (value === "false") return false;
    return null;
  }
  return null;
}

/** The inherited (operator-tier settings.yaml) values when no file says
 *  anything: no learning block, no steer scalar. */
export const NO_INHERITED_SETTINGS = Object.freeze({
  learning: Object.freeze({ mode: "", sensitivity: "", extraLines: [] }),
  steer: null,
});

/** Re-indents a carried block so its children sit at two spaces under the
 *  new header, preserving relative nesting; comment lines shallower than the
 *  block's children lose only what indentation they have. */
function reindentCarried(lines) {
  const trimmed = [...lines];
  while (trimmed.length && neutralLine(trimmed[trimmed.length - 1]))
    trimmed.pop();
  while (trimmed.length && trimmed[0].trim() === "") trimmed.shift();
  const widths = trimmed
    .filter((line) => !neutralLine(line))
    .map((line) => line.match(/^ */)[0].length);
  const min = widths.length ? Math.min(...widths) : 0;
  const strip = new RegExp(`^ {0,${min}}`);
  return trimmed.map((line) =>
    line.trim() === "" ? "" : `  ${line.replace(strip, "")}`,
  );
}

/**
 * The CLI-tier settings file mecated reads for the learning knobs: the FULL
 * `learning:` section — Studio's mode/sensitivity where set, the inherited
 * value otherwise, then every other line of the inherited block verbatim.
 * Only a `learning:` block is ever rendered (steer and the soul knobs are
 * flags), and only when the document overrides something; the header-only
 * text is what an unset document renders to, and the controller then does
 * not pass the file at all.
 * @param {{learning: {mode: string, sensitivity: string}}} config
 * @param {{learning: {mode: string, sensitivity: string, extraLines: string[]}}} [inherited]
 * @returns {string}
 */
export function renderRuntimeSettingsYAML(
  config,
  inherited = NO_INHERITED_SETTINGS,
) {
  const header =
    "# Managed by Mecatl Studio (CLI tier). Edit it from Settings, not here.\n";
  if (!runtimeSettingsHasYAML(config)) return header;
  const base = inherited?.learning ?? NO_INHERITED_SETTINGS.learning;
  const mode = config.learning.mode || base.mode;
  const sensitivity = config.learning.sensitivity || base.sensitivity;
  const lines = [
    "# The user-global settings.yaml learning block is merged below so mecated's",
    "# whole-block capture keeps its skills/automatic settings.",
    "learning:",
  ];
  if (mode) lines.push(`  mode: ${JSON.stringify(mode)}`);
  if (sensitivity) lines.push(`  sensitivity: ${JSON.stringify(sensitivity)}`);
  lines.push(...reindentCarried(base.extraLines ?? []));
  return `${header}${lines.join("\n")}\n`;
}

/**
 * What the daemon will actually run with: Studio's override where the
 * controller passes it, the inherited settings.yaml value otherwise, the
 * daemon default last (learning off, balanced sensitivity, steer on).
 * While an imported operator-settings.yaml is active the controller passes
 * no learning file of its own, so the inherited values stand alone; the
 * `--no-steer` flag holds either way but can only tighten.
 * @param {{learning: {mode: string, sensitivity: string}, steer: {enabled: boolean}}} config
 * @param {{learning: {mode: string, sensitivity: string}, steer: boolean | null}} inherited
 * @param {{operatorSettingsActive?: boolean}} [options]
 * @returns {{learning: {mode: string, sensitivity: string}, steer: boolean}}
 */
export function effectiveRuntimeSettings(
  config,
  inherited = NO_INHERITED_SETTINGS,
  { operatorSettingsActive = false } = {},
) {
  const own = operatorSettingsActive
    ? { mode: "", sensitivity: "" }
    : config.learning;
  return {
    learning: {
      mode: own.mode || inherited.learning?.mode || "off",
      sensitivity:
        own.sensitivity || inherited.learning?.sensitivity || "balanced",
    },
    steer: config.steer.enabled && inherited.steer !== false,
  };
}
