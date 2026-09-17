import { apiError } from "./errors";
import { getHarnessClient, harness } from "./sdk";

/**
 * Project trust as the browser sees it: the CONTROLLER's own registry
 * (`/status.trust`, the grant routes) plus the one daemon-side signal that
 * can contradict it (`GET /v1/soul`).
 *
 * mecated never prompts for trust. mecatui's pre-TUI prompt decides through
 * the Go composition layer; Studio's controller keeps its OWN registry
 * (`trustProject` + a workspace identity anchor in permissions.json) and
 * mirrors the daemon's authority/anchor probes. That registry cannot see the
 * daemon's own grants — `~/.config/mecatl/trust.yaml` (remembered through
 * mecatui) and the user-global `trustedWorkspaces:` list — so the daemon may
 * already trust a workspace Studio reads as untrusted. `fetchDaemonSoulTrust`
 * is the cheapest cross-check the wire offers: a PROJECT soul the daemon
 * reports as trusted means the daemon admitted the project, whoever granted
 * it. There is no client-visible trust field beyond that (the compatibility
 * document carries only the posture).
 */

const CONTROL_API = "/api/mecatl-control";

/** What the controller's spawn actually got. */
type HarnessTrustDecision = "trusted" | "once" | "drifted" | "untrusted";

/** Who granted it: Studio's registry, the posture floor, or nobody. */
type HarnessTrustSource = "studio" | "posture" | "none";

export interface HarnessTrustState {
  /** The workspace carries a project soul, agents, commands, skills or
   *  allow rules — something a grant would admit. No authority, no prompt. */
  hasAuthority: boolean;
  decision: HarnessTrustDecision;
  source: HarnessTrustSource;
  /** The live workspace identity anchor (an opaque hash; "" when the
   *  workspace did not resolve). The banner keys its "Not now" on it so a
   *  changed authority set re-prompts. */
  anchor: string;
}

const DECISIONS: readonly HarnessTrustDecision[] = [
  "trusted",
  "once",
  "drifted",
  "untrusted",
];
const SOURCES: readonly HarnessTrustSource[] = ["studio", "posture", "none"];

/** Reads `/status.trust`; null for external mode's `trust: null`, an older
 *  controller, or a malformed payload — the banner then stays silent. */
export function readTrustState(raw: unknown): HarnessTrustState | null {
  if (!raw || typeof raw !== "object") return null;
  const body = raw as {
    hasAuthority?: unknown;
    decision?: unknown;
    source?: unknown;
    anchor?: unknown;
  };
  const decision = DECISIONS.find((value) => value === body.decision);
  if (!decision) return null;
  return {
    hasAuthority: body.hasAuthority === true,
    decision,
    source: SOURCES.find((value) => value === body.source) ?? "none",
    anchor: typeof body.anchor === "string" ? body.anchor : "",
  };
}

/**
 * The REMEMBERED grant: the controller persists `trustProject: true` with
 * the live anchor (also how a drifted grant is re-accepted after review).
 * Bodyless. RESTARTS the daemon; in-flight runs end. Writes only the
 * controller's permissions.json — never trust.yaml.
 */
export async function trustWorkspace(): Promise<void> {
  const response = await fetch(`${CONTROL_API}/permissions/trust`, {
    method: "POST",
  });
  if (!response.ok) throw await apiError(response);
}

/**
 * The grant for this CONTROLLER process only: nothing is persisted, and the
 * grant lasts until Studio's controller exits (every daemon restart in
 * between keeps it). Bodyless. RESTARTS the daemon; in-flight runs end.
 */
export async function trustWorkspaceOnce(): Promise<void> {
  const response = await fetch(`${CONTROL_API}/permissions/trust-once`, {
    method: "POST",
  });
  if (!response.ok) throw await apiError(response);
}

/** The daemon's own view of the project soul (`GET /v1/soul`). */
export interface DaemonSoulTrust {
  /** The daemon selected (or dropped) the PROJECT soul, `<ws>/.mecatl/soul.md`. */
  projectSoul: boolean;
  /** The daemon reports that soul's provenance trusted — for a project
   *  soul that means the daemon resolved the workspace TRUSTED, from
   *  whichever source (flag, trust.yaml, trustedWorkspaces, posture). */
  trusted: boolean;
}

/** SoulProvenance.PROJECT on the wire: the SDK decodes the enum as its
 *  integer (2); an older or protojson-shaped daemon may spell the name. */
function isProjectProvenance(value: unknown): boolean {
  if (value === 2) return true;
  return typeof value === "string" && /PROJECT$/i.test(value);
}

/**
 * Reads the daemon's soul snapshot and reduces it to the trust cross-check.
 * Null when the daemon cannot answer (offline, unsupported, refused) — the
 * caller then has no daemon-side signal and falls back to the controller's
 * registry alone. Never throws.
 */
export async function fetchDaemonSoulTrust(
  signal?: AbortSignal,
): Promise<DaemonSoulTrust | null> {
  try {
    const response = await harness(() =>
      getHarnessClient().soul.get(
        { $typeName: "mecatl.v1.GetSoulRequest" },
        { signal },
      ),
    );
    const soul = response.soul;
    if (!soul) return null;
    return {
      projectSoul: isProjectProvenance(soul.provenance),
      trusted: soul.trusted === true,
    };
  } catch {
    return null;
  }
}
