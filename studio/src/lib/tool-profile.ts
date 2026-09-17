/**
 * The per-session TOOL PROFILE — the daemon's `CreateSessionRequest.profile`
 * (ADR 0291): `""` is the deployment default (every tool the daemon
 * configures), `"no-fs"` attenuates the session to a file-less catalog
 * (Shell, Read, ListDir, Edit, Write, Copy, Move, Remove, Grep, Glob,
 * Parallel and SkillDraft stay out; WebSearch/WebFetch remain). The schedule
 * spec carries the same field, so a scheduled run can be attenuated too.
 *
 * The profile is FIXED at create: the daemon exposes no per-session read of
 * it and no switch, so a live chat only ever DISPLAYS what Studio remembers
 * choosing (`session-profile-memory.ts`). One vocabulary here keeps the
 * composer's Tools rows, the schedule form's picker and the detail badges
 * from drifting.
 */

import type { ScheduleSpecDraft } from "@/lib/protocol/schedules";

/** `"" | "no-fs"` — the same closed set the schedule spec carries. */
export type SessionToolProfile = ScheduleSpecDraft["profile"];

/** The Tools rows, in menu order: the everyday posture first. */
export const TOOL_PROFILE_OPTIONS = [
  {
    id: "",
    label: "All tools",
    description: "Shell and the file tools as this daemon configures them",
  },
  {
    id: "no-fs",
    label: "No filesystem",
    description:
      "Shell, Read, Edit, Write and the other file tools are left out; web tools remain",
  },
] as const satisfies readonly {
  id: SessionToolProfile;
  label: string;
  description: string;
}[];

/** Display label for a tool profile ("All tools" for the default). */
export function toolProfileLabel(profile: SessionToolProfile): string {
  return (
    TOOL_PROFILE_OPTIONS.find((option) => option.id === profile)?.label ??
    "All tools"
  );
}

/** Narrows an untrusted value (storage, an older row) to the closed set. */
export function normalizeToolProfile(value: unknown): SessionToolProfile {
  return value === "no-fs" ? "no-fs" : "";
}

/** The short pill suffix for a non-default profile ("" for the default). */
export function toolProfilePillSuffix(profile: SessionToolProfile): string {
  return profile === "no-fs" ? " · No FS" : "";
}

/** The muted note when the daemon itself runs without a Shell tool. */
export const SHELL_DISABLED_NOTE = "Shell is disabled on this daemon";

/**
 * True when the daemon reports its Shell tool OFF (`capabilities.bash ===
 * false` — the operator's `--no-shell`, Settings → Permissions → Shell
 * tool). An absent key is an older daemon that does not report it: no note.
 */
export function shellDisabledOnDaemon(
  capabilities: Record<string, unknown> | null | undefined,
): boolean {
  return capabilities?.bash === false;
}
