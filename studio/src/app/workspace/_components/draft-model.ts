import type { DefaultModel } from "@/lib/model-preferences";

/**
 * Where the model in force came from — the composer picker's "current:"
 * header names it so a user can tell a deliberate pick from a default that
 * applied on its own (the TUI's provenance tag).
 *
 * - `pick`: the user chose it in this draft (or the live chat was created
 *   with an explicit model that is not their Studio default).
 * - `studio-default`: the browser-local default for new chats applied.
 * - `daemon-default`: nothing picked and no applicable Studio default — the
 *   daemon uses its own default (or the router, when enabled).
 */
export type ModelProvenance = "pick" | "studio-default" | "daemon-default";

/** The short tag rendered next to the current model in the picker header. */
export function modelProvenanceLabel(provenance: ModelProvenance): string {
  switch (provenance) {
    case "pick":
      return "your pick";
    case "studio-default":
      return "Studio default";
    default:
      return "daemon default";
  }
}

/** The subset of a composer option the resolver needs. */
export interface DraftModelOption {
  id: string;
  providerId?: string;
}

export interface ResolvedDraftModel {
  /** The model id to send ("" = let the daemon pick). */
  id: string;
  /** Present exactly when `id` is a listed model with a known provider. */
  providerId?: string;
  provenance: ModelProvenance;
  /**
   * True when a Studio default exists but the loaded inventory (non-empty)
   * does not list it — the caller warns ONCE and leaves the preference as it
   * is; the chat falls back to the daemon default.
   */
  staleDefault: boolean;
}

/** True when `option` is the stored Studio default (provider + id). */
export function isDefaultOption(
  option: DraftModelOption,
  studioDefault: DefaultModel | null,
): boolean {
  return (
    studioDefault !== null &&
    option.id !== "" &&
    option.id === studioDefault.modelId &&
    (option.providerId ?? "") === studioDefault.providerId
  );
}

/**
 * Resolves what a NEW chat's create body should carry, in one place the
 * picker (for its label and header) and the workspace (for the create call)
 * both use so they can never disagree:
 *
 * 1. An explicit pick wins — `pick` is the picked id, or "" for the auto row
 *    (the user asked for the daemon default on purpose). A picked id that has
 *    since left the inventory degrades to the daemon default rather than
 *    sending a bare model_id the daemon would reject.
 * 2. Untouched (`pick === null`): the Studio default applies when the
 *    inventory lists it (provider + id); a default the loaded, non-empty
 *    inventory lacks is reported `staleDefault` and NOT applied.
 * 3. Otherwise the daemon default.
 */
export function resolveDraftModel(
  pick: string | null,
  studioDefault: DefaultModel | null,
  options: readonly DraftModelOption[],
): ResolvedDraftModel {
  if (pick !== null) {
    if (pick === "") return { id: "", provenance: "pick", staleDefault: false };
    const option = options.find((m) => m.id === pick);
    if (option?.providerId) {
      return {
        id: pick,
        providerId: option.providerId,
        provenance: "pick",
        staleDefault: false,
      };
    }
    return { id: "", provenance: "daemon-default", staleDefault: false };
  }
  if (studioDefault) {
    const option = options.find((m) => isDefaultOption(m, studioDefault));
    if (option?.providerId) {
      return {
        id: option.id,
        providerId: option.providerId,
        provenance: "studio-default",
        staleDefault: false,
      };
    }
    // Only a LOADED inventory can vouch that the default is gone; an empty
    // list (still loading, daemon offline) is not evidence.
    const loaded = options.some((m) => m.id !== "");
    return { id: "", provenance: "daemon-default", staleDefault: loaded };
  }
  return { id: "", provenance: "daemon-default", staleDefault: false };
}

/**
 * Provenance for a LIVE chat, whose model the daemon fixed at create: "" is
 * the daemon default (an explicit auto pick is indistinguishable after the
 * fact, and "daemon default" is what is in force either way); a model equal
 * to the Studio default reads as that default; anything else was a pick.
 */
export function liveModelProvenance(
  currentId: string,
  studioDefault: DefaultModel | null,
  options: readonly DraftModelOption[],
): ModelProvenance {
  if (!currentId) return "daemon-default";
  if (!studioDefault || studioDefault.modelId !== currentId) return "pick";
  // The session row carries only the model id; match the provider through
  // the listed option when there is one, else trust the id.
  const option = options.find((m) => m.id === currentId);
  return !option || isDefaultOption(option, studioDefault)
    ? "studio-default"
    : "pick";
}
