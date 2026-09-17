/**
 * The daemon-wide operator posture ladder and what each tier switches on —
 * the pure half of the chat status strip's posture summary, mirroring the
 * TUI's `/posture` one-liner (`cmd/mecatui/ui/builtins.go` `postureSummary`)
 * so a Studio user and a mecatui user read the SAME sentence for the same
 * tier.
 *
 * The tier itself arrives as `serverCapabilities.posture` off the daemon's
 * compatibility document (the SDK's `ServerPosture` values); nothing here
 * decides anything — it only spells out the daemon's own decision. The
 * one always-true caveat rides along: a Deny in any scope and a configured
 * Ask apply at EVERY tier, yolo included.
 */

/** mecated's posture ladder, lowest tier first (`--posture`). */
export const POSTURE_TIERS = ["strict", "trusted", "auto", "yolo"] as const;

type PostureTier = (typeof POSTURE_TIERS)[number];

/** The label the TUI prints for an empty tier (`cmd/mecatui/ui/model.go`
 *  `unknownLabel`). */
const UNKNOWN_POSTURE_LABEL = "unknown";

/** The four defenses a tier switches on or off, as booleans (true = on). */
interface PostureDefenses {
  /** Allow-all: the built-in ask before every mutating tool is waived
   *  (auto and yolo). */
  allowAll: boolean;
  /** The MAIN session auto-runs `$()`/backtick/heredoc substitutions in
   *  Shell instead of surfacing an ask (auto and yolo). */
  mainSubstitution: boolean;
  /** A CHILD (subagent) auto-runs those substitutions too — the child
   *  prompt-injection defense is OFF (yolo only). */
  childSubstitution: boolean;
  /** Project trust: the checked-in allow rules, AGENTS.md, soul, agents,
   *  commands and skills are honoured (trusted and above). */
  projectTrust: boolean;
}

/** Narrows an arbitrary wire value to a known tier, or null. */
function knownPostureTier(value: unknown): PostureTier | null {
  return typeof value === "string" &&
    (POSTURE_TIERS as readonly string[]).includes(value)
    ? (value as PostureTier)
    : null;
}

/**
 * Which defenses a tier switches on. An unknown or empty tier reads as
 * everything off — the same degrade the TUI's summary prints — because
 * claiming a defense is waived on a tier Studio does not know would be a
 * guess in the unsafe direction.
 */
function postureDefenses(tier: string): PostureDefenses {
  const known = knownPostureTier(tier);
  const allowAll = known === "auto" || known === "yolo";
  return {
    allowAll,
    mainSubstitution: allowAll,
    childSubstitution: known === "yolo",
    projectTrust: known === "trusted" || known === "auto" || known === "yolo",
  };
}

const onoff = (on: boolean) => (on ? "on" : "off");

/**
 * The TUI's `/posture` sentence, byte-for-byte
 * (`cmd/mecatui/ui/builtins.go` `postureSummary`): the tier label (or
 * "unknown" for an empty token) followed by the four defenses and the
 * always-on caveat. Pinned against the Go table by posture.test.ts.
 */
export function postureSummary(tier: string): string {
  const defenses = postureDefenses(tier);
  const label = tier === "" ? UNKNOWN_POSTURE_LABEL : tier;
  return (
    `posture ${label}` +
    ` — allow-all ${onoff(defenses.allowAll)}` +
    `; main $()/heredoc auto-run ${onoff(defenses.mainSubstitution)}` +
    `; child $()/heredoc auto-run (injection-defense off) ${onoff(defenses.childSubstitution)}` +
    `; project-trust ${onoff(defenses.projectTrust)}` +
    " (Deny & configured Ask always apply)"
  );
}

export type PostureTone = "muted" | "warning" | "danger";

/**
 * How loudly the tier badge should read: quiet for the asking tiers (and
 * an unknown token), a warning for allow-all, danger for yolo — the only
 * tier that also drops the child prompt-injection defense.
 */
export function postureTone(tier: string): PostureTone {
  switch (knownPostureTier(tier)) {
    case "yolo":
      return "danger";
    case "auto":
      return "warning";
    default:
      return "muted";
  }
}
