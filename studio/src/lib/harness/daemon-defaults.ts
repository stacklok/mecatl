import {
  DAEMON_DEFAULTS_EMPTY,
  normalizeDaemonDefaults,
} from "@/lib/daemon-defaults.mjs";
import { apiError } from "./errors";

/**
 * The managed daemon's DEFAULTS — the mecated spawn flags an operator would
 * otherwise type on the command line: default and subagent model (per
 * provider), reasoning effort, context window override, prompt caching,
 * provider base-URL overrides, the ToolHive LLM gateway, model aliases and
 * slots, and the credentials-file PATH (never a key). Controller-owned
 * (`GET|PUT /daemon-defaults`, mirrored on `/status.daemonDefaults`), never
 * a settings.yaml key and never a daemon route; every save restarts the
 * daemon and the controller rolls back a document mecated refuses to start
 * on. External mode answers 409 for both verbs.
 */

const CONTROL_API = "/api/mecatl-control";

/** One provider's saved model pair; "" means "not set" (flag omitted). */
interface HarnessProviderModelDefaults {
  /** `--default-model`: every zero-selector session inherits it. */
  defaultModel: string;
  /** `--subagent-model`: the default for every def-less child engine. */
  subagentModel: string;
}

/** The normalised document, exactly as the controller keeps it. */
export interface HarnessDaemonDefaults {
  /** Keyed by provider kind — mecated validates `--default-model` against
   *  the CURRENT default provider, so a pair is only passed on its own. */
  models: Record<string, HarnessProviderModelDefaults>;
  /** "" (auto) | low | medium | high | xhigh | max. */
  reasoningEffort: string;
  /** Tokens; 0 = off. */
  contextWindowOverride: number;
  /** The two LLM stream bounds, whole seconds; 0 DISABLES a bound (it is a
   *  real value, not "unset"). mecated's own defaults (300 / 180) emit no
   *  flag. */
  llmTimeouts: {
    /** `--llm-per-attempt-timeout`: connect + first chunk only. */
    perAttemptSeconds: number;
    /** `--llm-stream-idle-timeout`: the longest mid-stream silence. */
    streamIdleSeconds: number;
  };
  promptCache: {
    /** `--no-prompt-cache`. */
    disabled: boolean;
    /** `--anthropic-cache-ttl`: "" (API default) | "5m" | "1h". */
    anthropicTtl: string;
  };
  /** `--<kind>-base-url` per built-in kind; "" = not overridden. */
  baseUrls: Record<string, string>;
  toolhive: {
    /** false passes `--toolhive-llm=false`. */
    enabled: boolean;
    /** `--toolhive-llm-base-url` (loopback only); "" = auto-detect. */
    baseUrl: string;
    /** `--toolhive-llm-mode`: auto | proxy | direct. */
    mode: string;
  };
  /** `--model-alias name=model`. */
  aliases: Record<string, string>;
  /** `--model-slot slot=selector` (the `router` slot is reserved for model
   *  routing). */
  slots: Record<string, string>;
  /** `--api-key-file`; "" = the XDG default the controller reports as
   *  `/status.authFile`. A PATH on the daemon's machine, never a key. */
  apiKeyFile: string;
  /** The durable active-provider choice (owned by POST /providers/active;
   *  read-only here). */
  activeProvider: string | null;
}

/** The empty document: every flag omitted. */
export const EMPTY_DAEMON_DEFAULTS: HarnessDaemonDefaults =
  DAEMON_DEFAULTS_EMPTY as HarnessDaemonDefaults;

/**
 * Decodes a controller document through the SAME normaliser the controller
 * uses, so a malformed or older payload reads as the empty document instead
 * of a half-typed one. Null when the payload is not an object (external
 * mode's absence, an older controller).
 */
export function readDaemonDefaults(raw: unknown): HarnessDaemonDefaults | null {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  try {
    return normalizeDaemonDefaults(raw) as HarnessDaemonDefaults;
  } catch {
    return null;
  }
}

/**
 * What a form hands to the validator / the PUT: the document minus the
 * controller-owned active provider, with the context window still allowed
 * AS TYPED (the controller's grammar parses a digit string; anything else
 * is refused with its message rather than silently coerced).
 */
export type DaemonDefaultsInput = Omit<
  HarnessDaemonDefaults,
  "activeProvider" | "contextWindowOverride" | "llmTimeouts"
> & {
  contextWindowOverride: number | string;
  /** As typed too; "" reads as mecated's own default for that flag. */
  llmTimeouts: {
    perAttemptSeconds: number | string;
    streamIdleSeconds: number | string;
  };
  activeProvider?: string | null;
};

/**
 * Validates a draft with the controller's rules, throwing the controller's
 * own user-facing message, so the form can refuse before the restart
 * confirm. The active provider is not part of what the form saves.
 */
export function validateDaemonDefaults(
  draft: DaemonDefaultsInput,
): HarnessDaemonDefaults {
  return normalizeDaemonDefaults({
    ...draft,
    activeProvider: null,
  }) as HarnessDaemonDefaults;
}

export async function fetchHarnessDaemonDefaults(
  signal?: AbortSignal,
): Promise<HarnessDaemonDefaults | null> {
  const response = await fetch(`${CONTROL_API}/daemon-defaults`, {
    signal,
    cache: "no-store",
  });
  if (!response.ok) throw await apiError(response);
  const body = (await response.json()) as { defaults?: unknown };
  return readDaemonDefaults(body.defaults);
}

/**
 * Replaces the whole document. RESTARTS the daemon; a document mecated
 * refuses to start on (an unknown `--default-model`, a bad alias) is rolled
 * back by the controller and surfaces here as the thrown error carrying
 * mecated's own refusal. `activeProvider` is stripped: the controller owns
 * it through POST /providers/active.
 */
export async function saveHarnessDaemonDefaults(
  defaults: DaemonDefaultsInput,
): Promise<HarnessDaemonDefaults | null> {
  const { activeProvider: _ignored, ...body } = defaults;
  const response = await fetch(`${CONTROL_API}/daemon-defaults`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!response.ok) throw await apiError(response);
  const answer = (await response.json().catch(() => null)) as {
    defaults?: unknown;
  } | null;
  return readDaemonDefaults(answer?.defaults);
}
