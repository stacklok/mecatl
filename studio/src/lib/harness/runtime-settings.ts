import {
  DEFAULT_RUNTIME_SETTINGS,
  LEARNING_MODES,
  LEARNING_SENSITIVITIES,
} from "@/lib/runtime-settings.mjs";
import { apiError } from "./errors";

/**
 * The managed daemon's RUNTIME SETTINGS — the knobs mecatui exposes as
 * `/learning`, `/learning-sensitivity`, `--no-steer` and the soul flags
 * (`--no-soul`, `--soul-strict`, `--soul-file`, `--approve-soul`), owned by
 * the controller (`GET|PUT /runtime-settings`, `POST /soul/approve`), never
 * a daemon route. Steer and the soul knobs become mecated spawn flags; the
 * learning pair becomes a CLI-tier settings file carrying the FULL merged
 * `learning:` block (mecated captures that section whole-block, so a
 * partial file would drop the operator's skills/automatic settings). The
 * user-global settings.yaml is never edited. Every write RESTARTS the
 * daemon (in-flight runs end) and the controller rolls back a document
 * mecated refuses to start on. External mode answers 409 for all three
 * routes: the deployment owns its own flags.
 *
 * `soul.file` is a display path on the operator's OWN machine (managed mode
 * only), constrained server-side to the mecatl config dir or the workspace;
 * file contents never travel here (the persona body is the daemon's own
 * `GET /v1/soul`).
 */

const CONTROL_API = "/api/mecatl-control";

/** "" = leave the operator's settings.yaml value (or the daemon default). */
export type HarnessLearningMode = "" | "off" | "review" | "auto";
export type HarnessLearningSensitivity =
  | ""
  | "conservative"
  | "balanced"
  | "eager";

/** mecated's closed vocabularies, shared with the controller so a select
 *  can only ever offer what the daemon parses. */
export const HARNESS_LEARNING_MODES: readonly Exclude<
  HarnessLearningMode,
  ""
>[] = LEARNING_MODES as readonly Exclude<HarnessLearningMode, "">[];
export const HARNESS_LEARNING_SENSITIVITIES: readonly Exclude<
  HarnessLearningSensitivity,
  ""
>[] = LEARNING_SENSITIVITIES as readonly Exclude<
  HarnessLearningSensitivity,
  ""
>[];

/** The controller's saved document (PUT /runtime-settings body). */
export interface HarnessRuntimeSettings {
  learning: {
    mode: HarnessLearningMode;
    sensitivity: HarnessLearningSensitivity;
  };
  /** false → `--no-steer`: every mid-run send queues instead. */
  steer: { enabled: boolean };
  soul: {
    /** false → `--no-soul`: no persona fragment at all. */
    enabled: boolean;
    /** true → `--soul-strict`: a drifted soul contributes nothing. */
    strict: boolean;
    /** "" = the conventional `<config>/mecatl/soul.md`; else `--soul-file`. */
    file: string;
  };
}

/** GET /runtime-settings: the document plus what it lands on. */
export interface HarnessRuntimeSettingsDoc {
  config: HarnessRuntimeSettings;
  /** "operator-settings" for learning: an imported operator settings file
   *  is active, the controller passes no learning file alongside it, and
   *  the PUT refuses a learning change (409). Steer and soul are flags, so
   *  they are always Studio's. */
  managedBy: {
    learning: "studio" | "operator-settings";
    steer: "studio";
    soul: "studio";
  };
  /** What the operator's settings.yaml itself says ("" / null = nothing). */
  inherited: {
    learning: { mode: string; sensitivity: string };
    steer: boolean | null;
  };
  /** The fold mecated actually runs with: Studio's override where passed,
   *  the inherited value otherwise, the daemon default last. The daemon's
   *  own `capabilities.steer` remains the live truth for steer. */
  effective: {
    learning: { mode: string; sensitivity: string };
    steer: boolean;
  };
  /** The conventional soul path (the picker's placeholder). */
  soulFileDefault: string;
  /** `*.md` files in the mecatl config dir — the picker's choices. */
  soulCandidates: { path: string; name: string }[];
}

export const EMPTY_RUNTIME_SETTINGS: HarnessRuntimeSettings = {
  learning: { mode: "", sensitivity: "" },
  steer: { enabled: true },
  soul: { enabled: true, strict: false, file: "" },
};

const asRecord = (raw: unknown): Record<string, unknown> =>
  raw && typeof raw === "object" ? (raw as Record<string, unknown>) : {};

const bool = (raw: unknown, fallback: boolean) =>
  typeof raw === "boolean" ? raw : fallback;

const text = (raw: unknown) => (typeof raw === "string" ? raw : "");

const oneOf = <T extends string>(
  raw: unknown,
  allowed: readonly T[],
): T | "" =>
  typeof raw === "string" && (allowed as readonly string[]).includes(raw)
    ? (raw as T)
    : "";

/**
 * Decodes a saved document defensively: an unknown vocabulary value or a
 * non-boolean flag falls back to the default rather than reaching a select
 * that cannot show it.
 */
export function readRuntimeSettings(raw: unknown): HarnessRuntimeSettings {
  const body = asRecord(raw);
  const learning = asRecord(body.learning);
  const steer = asRecord(body.steer);
  const soul = asRecord(body.soul);
  return {
    learning: {
      mode: oneOf(learning.mode, HARNESS_LEARNING_MODES),
      sensitivity: oneOf(learning.sensitivity, HARNESS_LEARNING_SENSITIVITIES),
    },
    steer: {
      enabled: bool(steer.enabled, DEFAULT_RUNTIME_SETTINGS.steer.enabled),
    },
    soul: {
      enabled: bool(soul.enabled, DEFAULT_RUNTIME_SETTINGS.soul.enabled),
      strict: bool(soul.strict, DEFAULT_RUNTIME_SETTINGS.soul.strict),
      file: text(soul.file),
    },
  };
}

/** Decodes GET /runtime-settings, filling anything an older controller does
 *  not report with the empty/default shape. */
export function readRuntimeSettingsDoc(
  raw: unknown,
): HarnessRuntimeSettingsDoc {
  const body = asRecord(raw);
  const managedBy = asRecord(body.managedBy);
  const inherited = asRecord(body.inherited);
  const inheritedLearning = asRecord(inherited.learning);
  const effective = asRecord(body.effective);
  const effectiveLearning = asRecord(effective.learning);
  const config = readRuntimeSettings(body.config);
  const candidates = Array.isArray(body.soulCandidates)
    ? body.soulCandidates
    : [];
  return {
    config,
    managedBy: {
      learning:
        managedBy.learning === "operator-settings"
          ? "operator-settings"
          : "studio",
      steer: "studio",
      soul: "studio",
    },
    inherited: {
      learning: {
        mode: text(inheritedLearning.mode),
        sensitivity: text(inheritedLearning.sensitivity),
      },
      steer: typeof inherited.steer === "boolean" ? inherited.steer : null,
    },
    effective: {
      learning: {
        mode: text(effectiveLearning.mode) || config.learning.mode || "off",
        sensitivity:
          text(effectiveLearning.sensitivity) ||
          config.learning.sensitivity ||
          "balanced",
      },
      steer: bool(effective.steer, config.steer.enabled),
    },
    soulFileDefault: text(body.soulFileDefault),
    soulCandidates: candidates.flatMap((entry) => {
      const row = asRecord(entry);
      const path = text(row.path);
      return path ? [{ path, name: text(row.name) || path }] : [];
    }),
  };
}

/** Reads the saved document plus the inherited and effective values. */
export async function fetchHarnessRuntimeSettings(
  signal?: AbortSignal,
): Promise<HarnessRuntimeSettingsDoc> {
  const response = await fetch(`${CONTROL_API}/runtime-settings`, {
    cache: "no-store",
    signal,
  });
  if (!response.ok) throw await apiError(response);
  return readRuntimeSettingsDoc(await response.json().catch(() => null));
}

/**
 * Replaces the document whole. RESTARTS the daemon (in-flight runs end);
 * a document mecated refuses to start on is rolled back by the controller
 * and surfaces here as the thrown error, as does a learning change while an
 * imported operator settings file is active (409) or a soul path outside
 * the allowed directories (400).
 */
export async function saveHarnessRuntimeSettings(
  config: HarnessRuntimeSettings,
): Promise<HarnessRuntimeSettings> {
  const response = await fetch(`${CONTROL_API}/runtime-settings`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(config),
  });
  if (!response.ok) throw await apiError(response);
  const body = asRecord(await response.json().catch(() => null));
  return readRuntimeSettings(body.config ?? config);
}

/**
 * Accepts the current soul as the drift baseline: ONE daemon restart with
 * `--approve-soul` (mecated rewrites `<soul>.sha256`). RESTARTS the daemon.
 * Meaningful only for a user/project soul — the daemon skips the baseline
 * for a driver-provenance soul (`--soul-source-url`) — so offer it only
 * when `GET /v1/soul` reports one of those provenances.
 */
export async function approveHarnessSoulBaseline(): Promise<void> {
  const response = await fetch(`${CONTROL_API}/soul/approve`, {
    method: "POST",
  });
  if (!response.ok) throw await apiError(response);
}
