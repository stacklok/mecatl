/**
 * The "features on this daemon" rows of the help reference — Studio's
 * analogue of the TUI help overlay's capability-gated lines
 * (cmd/mecatui/ui/help.go: `notEnabledTag`).
 *
 * Pure: it reads the SNAKE_CASE wire keys the runtime-status provider exposes
 * as `serverCapabilities` (projected by `toWireCapabilities` from
 * GET /v1/compatibility) plus the open feature registry, and says for each
 * feature whether the connected deployment enables it. Every check is an
 * explicit `=== true`: an absent key is "not enabled", never assumed — an
 * older daemon that omits a capability must read as lacking it (Studio's
 * daemon-only rule). The rows describe the DEPLOYMENT, not the open chat: a
 * text-only session model can still refuse an image the deployment allows.
 */

export interface HelpFeatureRow {
  /** Stable id — the wire key it derives from (`steer` also reads `http_steer`). */
  readonly id: string;
  /** Short name shown in the list. */
  readonly label: string;
  /** One line saying where the feature shows up in Studio. */
  readonly hint: string;
  /** True when the connected deployment enables it. */
  readonly enabled: boolean;
}

/** One capability row before the enabled flag is derived. */
interface HelpFeatureSpec {
  readonly id: string;
  readonly label: string;
  readonly hint: string;
  readonly enabled: (
    caps: Record<string, unknown>,
    features: ReadonlySet<string>,
  ) => boolean;
}

/** `caps[key] === true` — absence and every non-boolean read as off. */
function flag(key: string): HelpFeatureSpec["enabled"] {
  return (caps) => caps[key] === true;
}

/** True for a non-null object with at least one own key. */
function nonEmptyObject(value: unknown): boolean {
  return (
    typeof value === "object" &&
    value !== null &&
    !Array.isArray(value) &&
    Object.keys(value).length > 0
  );
}

/**
 * Row order is the order the page renders. Hints name the Studio surface
 * that uses the feature so the list doubles as a where-is-it guide.
 */
const HELP_FEATURES: readonly HelpFeatureSpec[] = [
  {
    id: "skills",
    label: "Skills",
    hint: "Type / in the composer to insert a skill; browse them under Skills",
    enabled: flag("skills"),
  },
  {
    id: "agents",
    label: "Agents",
    hint: "Type @ in the composer to mention an agent",
    enabled: flag("agents"),
  },
  {
    id: "slash_commands",
    label: "Slash commands",
    hint: "Type / in the composer to browse this chat's commands",
    enabled: flag("slash_commands"),
  },
  {
    id: "memory",
    label: "Memory",
    hint: "Cross-session memory carries context between runs (Settings → Memory)",
    enabled: flag("memory"),
  },
  {
    id: "steer",
    label: "Steer",
    hint: "Enter or Shift+Enter can steer a running turn instead of queueing",
    // The SAME gate the chat hook uses (use-agent-chat `steerSupported`):
    // the live capability, or the feature-registry row a rebuilt daemon serves.
    enabled: (caps, features) =>
      caps.steer === true || features.has("http_steer"),
  },
  {
    id: "manual_compaction",
    label: "Compaction",
    hint: "Compact conversation in the chat ··· menu",
    enabled: flag("manual_compaction"),
  },
  {
    id: "scheduling",
    label: "Scheduling",
    hint: "Scheduled runs live under the Scheduled tab",
    enabled: flag("scheduling"),
  },
  {
    id: "teams",
    label: "Teams",
    hint: "The agent can delegate work to a coordinated team of agents",
    enabled: flag("teams"),
  },
  {
    id: "image",
    label: "Image attachments",
    hint: "Attach images as media on this deployment; otherwise files go inline as text",
    enabled: flag("image"),
  },
  {
    id: "audio",
    label: "Audio attachments",
    hint: "Attach audio as media on this deployment",
    enabled: flag("audio"),
  },
  {
    id: "session_debug",
    label: "Debug with AI",
    hint: "Open a debug chat about a session from the chat list's ··· menu",
    enabled: flag("session_debug"),
  },
  {
    id: "learned_skills",
    label: "Learned skills",
    hint: "Skills the agent drafted from its own runs, under Skills → Learned",
    enabled: flag("learned_skills"),
  },
  {
    id: "manual_dream",
    label: "Memory consolidation",
    hint: "Consolidate memory on demand under Settings → Memory",
    // A per-target object ({project_memory: {generate, decide, …}}); empty or
    // absent means no target can be consolidated.
    enabled: (caps) => nonEmptyObject(caps.manual_dream),
  },
  {
    id: "worktrees",
    label: "Worktrees",
    hint: "The daemon can place a chat in a git worktree of the workspace",
    enabled: flag("worktrees"),
  },
  {
    id: "mcp",
    label: "MCP servers",
    hint: "Tools from MCP servers reach the agent through the gateway (Settings → Gateway)",
    enabled: flag("mcp"),
  },
];

/**
 * Derives the help rows for one connected deployment. Pass the runtime
 * status's `serverCapabilities` and `features` verbatim; against an older
 * daemon both are empty and every row comes back disabled.
 */
export function deriveHelpFeatures(
  caps: Record<string, unknown>,
  features: ReadonlySet<string>,
): HelpFeatureRow[] {
  return HELP_FEATURES.map((spec) => ({
    id: spec.id,
    label: spec.label,
    hint: spec.hint,
    enabled: spec.enabled(caps, features),
  }));
}
