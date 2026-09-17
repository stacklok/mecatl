/**
 * The controller's permissions state — operator posture, project trust and
 * shell-less mode — normalised and turned into mecated spawn flags. Shared by
 * the controller (`scripts/local-controller.mjs`) and its vitest suite, the
 * same dual-import pattern as controller-security.mjs, so the flag grammar
 * cannot drift between the process that spawns mecated and the tests that
 * pin what it passes.
 *
 * Nothing here is a credential and nothing here writes settings.yaml: the
 * controller expresses the whole decision as CLI flags on the spawn.
 */

/** mecated's operator posture ladder, lowest tier first (`--posture`). */
export const POSTURES = Object.freeze(["strict", "trusted", "auto", "yolo"]);

/** The tier at or above which mecated raises the project-trust floor on an
 *  interactive root (internal/app/posture.go applyPosture). The controller
 *  never passes --headless, so for the Studio-managed daemon this floor
 *  always applies — pinned by controller-permissions.test.ts. */
const TRUST_IMPLYING_POSTURE = "trusted";

/** The tiers that waive the built-in mutate-ask floor (allow-all); mecated
 *  refuses them as root outside MECATL_SANDBOX. */
export const ALLOW_ALL_POSTURES = Object.freeze(["auto", "yolo"]);

/** Longest trust anchor the state file keeps (an opaque label another
 *  controller feature may attach to a remembered trust decision). */
const maxTrustAnchorLength = 512;

/**
 * Rank of a posture on the ladder (0 = strict … 3 = yolo); -1 for an unknown
 * token so comparisons against it always read "below strict".
 * @param {string} posture
 */
export function postureRank(posture) {
  return POSTURES.indexOf(String(posture ?? "").toLowerCase());
}

/**
 * Whether the tier itself raises project trust on an interactive daemon
 * (trusted and above): the UI shows the trust switch checked and disabled
 * there because the flag would be redundant.
 * @param {string} posture
 */
export function postureImpliesTrust(posture) {
  return postureRank(posture) >= postureRank(TRUST_IMPLYING_POSTURE);
}

/**
 * Validates and fills a permissions document (a saved state file or a
 * POST /permissions body). Defaults are mecated's own: strict, untrusted,
 * shell on. An unknown posture THROWS rather than falling closed — the UI
 * only ever offers the four tiers, so a fifth is a bug worth surfacing, not
 * a preference to quietly downgrade.
 *
 * @param {unknown} input
 * @returns {{posture: string, trustProject: boolean, noShell: boolean, trustAnchor: string}}
 */
export function normalizePermissions(input) {
  const source =
    input && typeof input === "object" && !Array.isArray(input)
      ? /** @type {Record<string, unknown>} */ (input)
      : {};
  const rawPosture =
    typeof source.posture === "string"
      ? source.posture.trim().toLowerCase()
      : "";
  const posture = rawPosture || "strict";
  if (!POSTURES.includes(posture)) {
    throw Object.assign(
      new Error(
        `Unknown posture "${rawPosture}" — expected one of ${POSTURES.join(", ")}`,
      ),
      { statusCode: 400 },
    );
  }
  return {
    posture,
    trustProject: source.trustProject === true,
    noShell: source.noShell === true,
    trustAnchor:
      typeof source.trustAnchor === "string"
        ? source.trustAnchor.slice(0, maxTrustAnchorLength)
        : "",
  };
}

/**
 * The mecated flags for a permissions document.
 *
 * `--posture` is ALWAYS emitted, strict included: an explicit flag out-ranks
 * the `posture:` key of an imported operator settings.yaml
 * (internal/app/posture.go foldOperatorPosture returns early when the flag is
 * set), so Studio's saved tier is authoritative in BOTH directions — a
 * deliberately chosen Strict is never silently overridden by an imported
 * `posture: yolo`. `--trust-project` rides the saved switch OR the in-memory
 * trust-once grant; `--no-shell` drops the Shell tool from the catalog.
 *
 * `trustDrifted` is the controller's drift verdict for THIS spawn (the saved
 * trust anchor no longer matches the workspace's live one): a drifted saved
 * switch passes NO trust flag — the same fail-safe arm as the daemon's own
 * resolveTrust (internal/app/trust.go: a remembered-but-drifted entry is
 * untrusted until re-granted). Trust-once is a fresh, explicit answer to the
 * live prompt, so it still grants.
 *
 * @param {{posture: string, trustProject: boolean, noShell: boolean}} permissions
 * @param {{trustOnce?: boolean, trustDrifted?: boolean}} [options]
 * @returns {string[]}
 */
export function permissionArgs(
  permissions,
  { trustOnce = false, trustDrifted = false } = {},
) {
  const args = ["--posture", permissions.posture];
  if ((permissions.trustProject && !trustDrifted) || trustOnce)
    args.push("--trust-project");
  if (permissions.noShell) args.push("--no-shell");
  return args;
}
