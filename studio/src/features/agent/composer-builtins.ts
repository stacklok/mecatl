/**
 * Studio's client-owned slash commands — the web analogue of the TUI's
 * always-present built-in layer (cmd/mecatui/ui/builtins.go). They sit ahead
 * of the daemon's per-session commands in the composer's `/` palette and are
 * intercepted locally on send: a bare `/clear` never reaches the model.
 *
 * Pure module: the list, its capability gates, and the classifier the
 * composer runs on every submission. No daemon call lives here — the daemon's
 * own command list stays in composer-capabilities.ts.
 *
 * `/quit` and `/exit` have no web analogue and are deliberately omitted.
 */

export type StudioBuiltinCommand =
  | "clear"
  | "help"
  | "session"
  | "retry"
  | "diagnostics"
  | "compact"
  /** Rename this chat (the chat ··· menu's Rename prompt). */
  | "title"
  /** The MCP tools panel (`mcp` or `mcp_connector_status`). */
  | "mcp"
  /** The "Insert from MCP" prompt picker (`mcp`). */
  | "prompts"
  /** The "Insert from MCP" resource picker (`mcp`). */
  | "resources"
  /** Opens the `@` agent roster in the composer (`agents`). */
  | "agents"
  /** The Skills page (`skills`). */
  | "skills"
  /** The daemon's resolved soul (`soul`). */
  | "soul"
  /** Settings → Memory, the user model (`user_model`). */
  | "usermodel"
  /** Settings → Learning, the proposals (`learning_proposals`). */
  | "reflections"
  /** Settings → Learning, run a reflection (`reflection`). */
  | "reflect"
  /** Settings → Memory, consolidate on demand (`manual_dream`). */
  | "dream"
  /** The model and effort picker (`model_selection`). */
  | "models"
  | "effort"
  /** The Scheduled tab (`scheduling`). */
  | "schedule"
  /** Workspace-services enrollment (`workspace_enrollment`). */
  | "tools-connect"
  | "tools-cancel"
  /** Settings → Learning (the operator's learning mode). */
  | "learning"
  /** The daemon-reported effective posture (`posture`). */
  | "posture"
  /** Developer tools only: inject a FAKE permission ask (debug-ask.ts). */
  | "debug-ask";

/** What the daemon enables; a hidden built-in typed anyway is refused with
 *  a local warning rather than sent to the model. */
export interface BuiltinGates {
  /** `serverCapabilities.manual_compaction === true` (ADR 0244). */
  readonly manualCompaction: boolean;
  /** Settings → Labs "Developer tools" is on: offers `/debug-ask`. A
   *  browser preference, not a daemon capability — absent reads as off. */
  readonly developerTools?: boolean;
  /** The daemon's compatibility capabilities as the runtime-status provider
   *  exposes them (SNAKE_CASE wire keys, `serverCapabilities`). Gates the
   *  capability-gated set (`/mcp /agents /skills /soul …`); absent — a
   *  surface with no runtime status — hides all of them (fail-closed). */
  readonly capabilities?: Readonly<Record<string, unknown>>;
}

/** Fail-closed default: every gated built-in hidden. */
export const CLOSED_BUILTIN_GATES: BuiltinGates = {
  manualCompaction: false,
  developerTools: false,
};

export interface BuiltinSlashCommand {
  readonly name: StudioBuiltinCommand;
  readonly description: string;
  /** Marks the row for the palette (Terminal glyph) and the send path. */
  readonly builtin: true;
}

/**
 * The fixed palette order: the always-present six first (the TUI's, minus
 * `/quit`), then the capability-gated set in the TUI's order, then the two
 * operator-setting rows. Not ported — no Studio surface in this area:
 * `/quit`, `/sessions`, `/connect`, `/team`, `/worktrees`.
 */
const BUILTIN_ORDER: readonly StudioBuiltinCommand[] = [
  "clear",
  "help",
  "session",
  "retry",
  "diagnostics",
  "compact",
  "title",
  "mcp",
  "prompts",
  "resources",
  "agents",
  "skills",
  "soul",
  "usermodel",
  "reflections",
  "reflect",
  "dream",
  "models",
  "effort",
  "schedule",
  "tools-connect",
  "tools-cancel",
  "posture",
  "learning",
];

/**
 * The developer-tools built-ins: offered only while Settings → Labs
 * "Developer tools" is on (the TUI registers `/debug-ask` only in client
 * debug mode), after the regular palette, and NOT part of the documented
 * reference (`STUDIO_BUILTIN_COMMANDS`).
 */
const DEVELOPER_ORDER: readonly StudioBuiltinCommand[] = ["debug-ask"];

/** Descriptions mirror builtins.go so the two clients read alike. */
const BUILTIN_DESCRIPTIONS: Readonly<Record<StudioBuiltinCommand, string>> = {
  clear: "clear the conversation",
  help: "show keys & features",
  session: "show active session details and copy its exact ID",
  retry:
    "retry the last eligible failed model step without resending its prompt",
  diagnostics: "send a concise client and server diagnostics report",
  compact: "compact this session's model history",
  title: "rename this chat",
  mcp: "show the MCP tools panel — this chat's connectors and sources",
  prompts: "insert an MCP prompt into the draft",
  resources: "insert an MCP resource into the draft",
  agents: "open the @ agent roster",
  skills: "browse the skills",
  soul: "show the soul the daemon applies",
  usermodel: "show the user model (Settings → Memory)",
  reflections: "review learning proposals (Settings → Learning)",
  reflect: "run a reflection (Settings → Learning)",
  dream: "consolidate memory on demand (Settings → Memory)",
  models: "open the model and effort picker",
  effort: "open the model and effort picker on the Effort tiers",
  schedule: "open the scheduled runs",
  "tools-connect": "connect workspace services for this chat",
  "tools-cancel": "cancel the workspace services setup in progress",
  learning: "learning mode settings (Settings → Learning)",
  posture: "show the daemon's effective posture",
  "debug-ask":
    "inject a fake permission ask to exercise the approval panel (developer tools; never sent to the daemon)",
};

/**
 * Every built-in, in palette order, gates applied or not. The ungated list
 * is what the help reference documents; the gated one is what the palette
 * offers.
 */
export const STUDIO_BUILTIN_COMMANDS: readonly BuiltinSlashCommand[] =
  BUILTIN_ORDER.map((name) => ({
    name,
    description: BUILTIN_DESCRIPTIONS[name],
    builtin: true,
  }));

/** The developer-tools rows, offered after the regular palette when on. */
export const DEVELOPER_BUILTIN_COMMANDS: readonly BuiltinSlashCommand[] =
  DEVELOPER_ORDER.map((name) => ({
    name,
    description: BUILTIN_DESCRIPTIONS[name],
    builtin: true,
  }));

/** True when `name` is one of Studio's own slash commands. */
export function isStudioBuiltinCommand(
  name: string,
): name is StudioBuiltinCommand {
  return (
    (BUILTIN_ORDER as readonly string[]).includes(name) ||
    (DEVELOPER_ORDER as readonly string[]).includes(name)
  );
}

/** True for a non-null, non-array object with at least one own key — the
 *  `manual_dream` capability is a per-target object, empty when no target
 *  can be consolidated (help-features.ts reads it the same way). */
function nonEmptyObject(value: unknown): boolean {
  return (
    typeof value === "object" &&
    value !== null &&
    !Array.isArray(value) &&
    Object.keys(value).length > 0
  );
}

/**
 * The capability each gated built-in needs, read off the SNAKE_CASE wire
 * document. Every check is an explicit `=== true` (absence is "not
 * enabled", Studio's daemon-only rule — help-features.ts reads the same
 * keys the same way), with ONE exception: `/models` and `/effort` mirror
 * the composer's own picker gate (`effortSupported`, chat-input.tsx) —
 * the picker is shown unless the daemon says `model_selection: false`, so
 * the palette never hides a picker the toolbar shows, nor offers one it
 * hides. `/posture` needs the non-empty tier word, `/dream` the non-empty
 * per-target object.
 */
function capabilityGatedOff(
  name: StudioBuiltinCommand,
  caps: Readonly<Record<string, unknown>>,
): boolean {
  switch (name) {
    case "mcp":
      return !(caps.mcp === true || caps.mcp_connector_status === true);
    case "prompts":
    case "resources":
      return caps.mcp !== true;
    case "agents":
      return caps.agents !== true;
    case "skills":
      return caps.skills !== true;
    case "soul":
      return caps.soul !== true;
    case "usermodel":
      return caps.user_model !== true;
    case "reflections":
      return caps.learning_proposals !== true;
    case "reflect":
      return caps.reflection !== true;
    case "dream":
      return !nonEmptyObject(caps.manual_dream);
    case "models":
    case "effort":
      return caps.model_selection === false;
    case "schedule":
      return caps.scheduling !== true;
    case "tools-connect":
    case "tools-cancel":
      return caps.workspace_enrollment !== true;
    case "posture":
      return typeof caps.posture !== "string" || caps.posture === "";
    default:
      return false;
  }
}

/** The built-ins that need a capability document at all; with none (a
 *  surface outside the runtime-status provider) every one is hidden. */
const CAPABILITY_GATED: ReadonlySet<StudioBuiltinCommand> =
  new Set<StudioBuiltinCommand>([
    "mcp",
    "prompts",
    "resources",
    "agents",
    "skills",
    "soul",
    "usermodel",
    "reflections",
    "reflect",
    "dream",
    "models",
    "effort",
    "schedule",
    "tools-connect",
    "tools-cancel",
    "posture",
  ]);

/** A built-in the daemon's capabilities — or, for the developer tools,
 *  the Labs preference — hide from the palette. The ONE gate the palette,
 *  the send-path classifier and the dispatcher all read, so a row the
 *  palette shows is never refused as unavailable and vice versa. */
export function isBuiltinGatedOff(
  name: StudioBuiltinCommand,
  gates: BuiltinGates,
): boolean {
  return isGatedOff(name, gates);
}

function isGatedOff(name: StudioBuiltinCommand, gates: BuiltinGates): boolean {
  if (name === "compact") return !gates.manualCompaction;
  if (name === "debug-ask") return gates.developerTools !== true;
  if (CAPABILITY_GATED.has(name)) {
    if (!gates.capabilities) return true;
    return capabilityGatedOff(name, gates.capabilities);
  }
  return false;
}

/** The palette rows: built-ins in fixed order, gated ones hidden, the
 *  developer-tools rows last. */
export function builtinSlashCommands(
  gates: BuiltinGates,
): readonly BuiltinSlashCommand[] {
  return [...STUDIO_BUILTIN_COMMANDS, ...DEVELOPER_BUILTIN_COMMANDS].filter(
    (command) => !isGatedOff(command.name, gates),
  );
}

export type SlashLineClass =
  /** Not a built-in: the text goes to the daemon as an ordinary prompt
   *  (workspace commands keep their server-side expansion). */
  | { readonly kind: "pass" }
  /** A bare built-in: run it locally, never send it. */
  | { readonly kind: "builtin"; readonly name: StudioBuiltinCommand }
  /** A built-in the daemon hides: refuse with a local warning. */
  | {
      readonly kind: "gated";
      readonly name: StudioBuiltinCommand;
      readonly reason: string;
    }
  /** A built-in with trailing text: hold it — no built-in takes arguments. */
  | {
      readonly kind: "held";
      readonly name: StudioBuiltinCommand;
      readonly reason: string;
    };

/** The warning shown when a built-in is typed with text after it. */
export function heldReason(name: StudioBuiltinCommand): string {
  return `/${name} takes no arguments — remove the text to run it`;
}

/** The warning shown when a capability-hidden built-in is typed anyway. */
export function gatedReason(name: StudioBuiltinCommand): string {
  if (name === "debug-ask") {
    return "/debug-ask needs Developer tools — turn it on in Settings → Labs";
  }
  return `/${name} is not available on this daemon`;
}

/**
 * Classifies one composer submission. Only the FIRST line is read: it must be
 * exactly `/name` or `/name args` (whitespace-trimmed, name case-insensitive)
 * to be a candidate at all. Every built-in is argument-free, so `/name args`
 * — or a second line — is `held`, a hidden built-in is `gated`, an unknown
 * `/x` (a daemon workspace command) or any other text is `pass`.
 */
export function classifySlashLine(
  text: string,
  gates: BuiltinGates,
): SlashLineClass {
  const newline = text.indexOf("\n");
  const firstLine = (newline === -1 ? text : text.slice(0, newline)).trim();
  const rest = newline === -1 ? "" : text.slice(newline + 1).trim();
  const match = /^\/(\S+)(?:\s+(.*))?$/.exec(firstLine);
  if (!match) return { kind: "pass" };
  const name = (match[1] ?? "").toLowerCase();
  if (!isStudioBuiltinCommand(name)) return { kind: "pass" };
  if (isGatedOff(name, gates)) {
    return { kind: "gated", name, reason: gatedReason(name) };
  }
  const args = (match[2] ?? "").trim();
  if (args || rest) return { kind: "held", name, reason: heldReason(name) };
  return { kind: "builtin", name };
}

/**
 * What a built-in dispatcher reports back to the composer: `ok` clears the
 * editor (then types `insertText`, when set, into the emptied field — how
 * `/agents` opens the `@` roster); a refusal keeps the text in place and
 * shows `warning` above it.
 */
export type BuiltinOutcome =
  | { readonly ok: true; readonly insertText?: string }
  | { readonly ok: false; readonly warning: string };
