import { type SoulInfo, SoulProvenance } from "@stacklok-oss/mecatl-sdk/gen";
import { getHarnessClient, harness, isUnsupportedByDaemon } from "./sdk";

/**
 * The daemon's RESOLVED persona (soul) snapshot — `GET /v1/soul` over the
 * SDK's `client.soul.get()`: the persona fragment mecated injects as turn-0
 * context, as it stands for THIS process (after `--no-soul`, `--soul-file`,
 * `--soul-strict` and the trust fold). Shared by the chat's `/soul` built-in
 * (`soul-dialog.tsx`, the TUI's soul preview) and Settings → Agent's
 * Persona card. Read-only in both directions: the agent cannot write its
 * soul, and Studio never sends the body back — the managed-mode knobs are
 * controller spawn flags (`runtime-settings.ts`), never this route.
 *
 * `content` is the clean body that reaches the prompt (capped server-side
 * at the soul byte ceiling), of possibly project provenance: render it as
 * plain text, never as markdown. Gate a surface on `capabilities.soul`
 * (false when no soul source is wired, e.g. after `--no-soul`); an older
 * daemon without the route answers null.
 */

/** Where the selected (or dropped) soul came from. "none" = nothing was
 *  selected: none present, an untrusted project soul dropped, or
 *  `--no-soul`. */
type HarnessSoulProvenance = "none" | "user" | "project" | "driver";

export interface HarnessSoul {
  /** True when a soul fragment WILL be contributed this run. */
  present: boolean;
  /** The clean body the prompt receives; "" when none. */
  content: string;
  /** Byte length of `content`, 0 for none. */
  sizeBytes: number;
  /** Lowercase-hex SHA-256 of `content`, "" for none. */
  sha256: string;
  provenance: HarnessSoulProvenance;
  /** A user or driver soul is always trusted; a project soul only under
   *  project trust — false on a project soul means it was DROPPED. */
  trusted: boolean;
  /** The content hash differs from its recorded `<soul>.sha256` baseline.
   *  Informational: a drifted soul still loads unless strict. */
  drifted: boolean;
}

/** Nothing selected — also the shape for a response carrying no message. */
export const EMPTY_SOUL: HarnessSoul = {
  present: false,
  content: "",
  sizeBytes: 0,
  sha256: "",
  provenance: "none",
  trusted: false,
  drifted: false,
};

/** The wire enum (mecatl.v1.SoulProvenance) → the label vocabulary. */
function soulProvenanceLabel(
  provenance: SoulProvenance,
): HarnessSoulProvenance {
  switch (provenance) {
    case SoulProvenance.USER:
      return "user";
    case SoulProvenance.PROJECT:
      return "project";
    case SoulProvenance.DRIVER:
      return "driver";
    default:
      return "none";
  }
}

/** Projects the SDK's `SoulInfo` (a missing message = nothing selected). */
export function readHarnessSoul(info: SoulInfo | undefined): HarnessSoul {
  if (!info) return EMPTY_SOUL;
  return {
    present: info.present === true,
    content: info.content ?? "",
    sizeBytes: Number(info.sizeBytes ?? 0),
    sha256: info.sha256 ?? "",
    provenance: soulProvenanceLabel(info.provenance),
    trusted: info.trusted === true,
    drifted: info.drifted === true,
  };
}

/**
 * Reads the resolved soul snapshot. Null against a daemon without the route
 * (pre-soul); every other failure is rethrown as the typed
 * `HarnessApiError`.
 */
export async function fetchHarnessSoul(
  signal?: AbortSignal,
): Promise<HarnessSoul | null> {
  try {
    const response = await harness(() =>
      getHarnessClient().soul.get(
        { $typeName: "mecatl.v1.GetSoulRequest" },
        { signal },
      ),
    );
    return readHarnessSoul(response.soul);
  } catch (error) {
    if (isUnsupportedByDaemon(error)) return null;
    throw error;
  }
}
