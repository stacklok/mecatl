"use client";

/**
 * The composer's memory indicator — READ-ONLY, derived from the daemon.
 *
 * Memory is a daemon setting (mecated's `--memory-dir` / `--no-user-model`),
 * never a per-chat switch: the daemon registers Remember/Recall and wires the
 * user-model store at spawn, and no session API turns either off. The pill
 * therefore REPORTS what the daemon advertises in its compatibility document
 * (`serverCapabilities.memory` — Remember/Recall registered, the TUI's
 * "memory is on" welcome note — and `serverCapabilities.user_model`) and
 * points at Settings → Memory, where the stores live. It replaced a local
 * On/Off toggle that never reached the daemon and so implied a switch that
 * did not exist.
 */

/** The settings page that owns the memory stores. */

/** The two daemon-reported memory stores, as advertised on the capability
 *  document; `null` when the daemon reported nothing (an older daemon with no
 *  compatibility endpoint, or capabilities not loaded yet). */
export interface MemoryStores {
  /** `capabilities.memory`: the Remember/Recall tools are registered — the
   *  per-project cross-session store. */
  project: boolean | null;
  /** `capabilities.user_model`: the cross-project user-model store is
   *  wired (the facts Settings → Memory lists). */
  userModel: boolean | null;
}

/** Reads the two store flags off the wire-keyed (snake_case) capabilities. */
export function readMemoryStores(
  capabilities: Record<string, unknown>,
): MemoryStores {
  return {
    project: flag(capabilities.memory),
    userModel: flag(capabilities.user_model),
  };
}

function flag(value: unknown): boolean | null {
  return typeof value === "boolean" ? value : null;
}

/** "on" when EITHER store is on (the agent has some long-term memory), "off"
 *  only when the daemon reported both off, "unknown" when it reported
 *  neither. */
