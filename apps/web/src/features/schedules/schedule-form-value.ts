// SPDX-License-Identifier: Apache-2.0

import type { CreateScheduleData, ListSchedulesResponse } from "@mecatl-studio/contracts/generated";
import {
  compileSchedulePhrase,
  looksLikeCron,
  oneShotInstant,
  type SchedulePhraseResult,
  toLocalDateTimeInput,
} from "./schedule-phrase";

export type Schedule = ListSchedulesResponse["items"][number];
export type ScheduleBody = CreateScheduleData["body"];
/** A mode Studio can send: `scheduleModeSchema` in `contracts/src/schemas/schedules.ts`. */
export type RequestableScheduleMode = NonNullable<ScheduleBody["mode"]>;

/**
 * Whether Studio can send this mode. `auto` and `yolo` (recorded by an in-chat
 * schedule) and `serverDefault` (no mode set) come from outside Studio; an
 * edit keeps them by omitting the mode rather than replacing them.
 */
export function isRequestableScheduleMode(mode: Schedule["mode"]): mode is RequestableScheduleMode {
  return mode === "default" || mode === "plan" || mode === "acceptEdits";
}

/** The label for a schedule's mode, naming the ones set outside Studio as such. */
export function scheduleModeLabel(mode: Schedule["mode"]): string {
  switch (mode) {
    case "acceptEdits":
      return "Accept edits";
    case "plan":
      return "Plan";
    case "auto":
      return "Auto (set outside Studio)";
    case "yolo":
      return "YOLO (set outside Studio)";
    case "serverDefault":
      return "Server default";
    default:
      return "Default";
  }
}

export interface FormValue {
  allowWrites: boolean;
  cron: string;
  maxFires: string;
  name: string;
  /** The one-shot wall clock, "YYYY-MM-DDTHH:MM", or "" while there is no date. */
  oneShotAt: string;
  /**
   * The one-shot time as last chosen ("HH:MM"), or "" until one is. It is kept
   * apart from {@link oneShotAt} so that the Date and Time inputs stay
   * independent: a time chosen before the date, or kept while the date is
   * cleared, is not lost.
   */
  oneShotTime: string;
  /** The schedule's original one-shot instant, re-sent unchanged while the wall clock is unedited. */
  oneShotOriginal: string;
  oneShotMaxRetries: string;
  oneShotRetry: boolean;
  profile: "all" | "noFilesystem";
  prompt: string;
  timezone: string;
  triggerKind: "cron" | "once";
  /**
   * The permission mode a writing schedule runs in. `plan` and the modes set
   * outside Studio are only ever loaded from a stored writing schedule, so
   * saving it unchanged keeps them.
   */
  writeMode: Schedule["mode"];
}

export function valueFromSchedule(schedule?: Schedule): FormValue {
  const timezone =
    schedule?.trigger.kind === "cron" ? schedule.trigger.timezone : browserTimezone();
  const oneShotAt = schedule?.trigger.kind === "once" ? toLocalInput(schedule.trigger.at) : "";
  return {
    allowWrites: schedule?.mutating ?? false,
    cron: schedule?.trigger.kind === "cron" ? schedule.trigger.expression : "0 9 * * *",
    maxFires: String(schedule?.maxFires ?? 0),
    name: schedule?.name ?? "",
    oneShotAt,
    oneShotOriginal: schedule?.trigger.kind === "once" ? schedule.trigger.at : "",
    oneShotMaxRetries: String(schedule?.oneShotMaxRetries || 3),
    oneShotRetry: schedule?.oneShotRetry ?? false,
    oneShotTime: oneShotAt.slice(11, 16),
    profile: schedule?.profile ?? "all",
    prompt: schedule?.prompt ?? "",
    timezone,
    triggerKind: schedule?.trigger.kind ?? "cron",
    // A read-only schedule is always `plan`; the mode offered once writes are
    // switched on is the new-schedule default.
    writeMode: schedule?.mutating ? schedule.mode : "acceptEdits",
  };
}

/**
 * The form fields that make up a schedule's trigger: exactly what the server
 * compares when it refuses to change the trigger of an existing schedule
 * (the kind, the cron expression and timezone, or the one-shot instant). The
 * run limits (`maxFires`, the one-shot retry) are not part of it.
 */
export const TRIGGER_FIELDS = [
  "triggerKind",
  "cron",
  "timezone",
  "oneShotAt",
  "oneShotOriginal",
  "oneShotTime",
] as const satisfies ReadonlyArray<keyof FormValue>;

export type TriggerPatch = Partial<Pick<FormValue, (typeof TRIGGER_FIELDS)[number]>>;

/**
 * Whether the form may change the trigger. Only a new schedule's can be
 * edited: the daemon keeps an existing schedule's stored next run on update,
 * so changing when it runs means creating a new schedule.
 */
export function isTriggerEditable(schedule?: Schedule): boolean {
  return schedule === undefined;
}

/**
 * Applies a trigger edit (kind switch, cron builder, timezone, one-shot date
 * and time, or a compiled phrase). An existing schedule's trigger is locked,
 * so the edit is dropped and the value is returned unchanged.
 */
export function withTriggerPatch(
  value: FormValue,
  patch: TriggerPatch,
  schedule?: Schedule,
): FormValue {
  return isTriggerEditable(schedule) ? { ...value, ...patch } : value;
}

/**
 * The value the form renders. For an existing schedule the trigger fields are
 * always re-derived from the stored trigger, whatever the form state holds, so
 * the displayed trigger can never differ from the one that is sent.
 */
export function displayedValue(value: FormValue, schedule?: Schedule): FormValue {
  if (isTriggerEditable(schedule)) return value;
  const stored = valueFromSchedule(schedule);
  return {
    ...value,
    cron: stored.cron,
    oneShotAt: stored.oneShotAt,
    oneShotOriginal: stored.oneShotOriginal,
    oneShotTime: stored.oneShotTime,
    timezone: stored.timezone,
    triggerKind: stored.triggerKind,
  };
}

/**
 * The trigger the form sends. An existing schedule's trigger cannot be edited
 * (the daemon keeps its stored next run), so it is sent exactly as stored.
 */
export function triggerFromValue(value: FormValue, schedule?: Schedule): ScheduleBody["trigger"] {
  if (schedule) return schedule.trigger;
  return value.triggerKind === "cron"
    ? { expression: value.cron.trim(), kind: "cron", timezone: value.timezone.trim() }
    : { at: oneShotInstant(value.oneShotAt, value.oneShotOriginal) ?? "", kind: "once" };
}

/** The request body for the form. */
export function bodyFromValue(value: FormValue, schedule?: Schedule): ScheduleBody {
  const trigger = triggerFromValue(value, schedule);
  return {
    maxFires: Math.max(0, Number(value.maxFires) || 0),
    // A mode Studio cannot send is kept by omitting it (the BFF keeps the
    // stored mode), never replaced with one Studio can.
    mode: !value.allowWrites
      ? "plan"
      : isRequestableScheduleMode(value.writeMode)
        ? value.writeMode
        : undefined,
    mutating: value.allowWrites,
    name: value.name.trim(),
    oneShotMaxRetries: value.oneShotRetry ? Math.max(0, Number(value.oneShotMaxRetries) || 0) : 0,
    oneShotRetry: trigger.kind === "once" && value.oneShotRetry,
    profile: value.profile,
    prompt: value.prompt.trim(),
    trigger,
  };
}

/**
 * Why the form cannot be submitted yet, or null when it can. Only shape checks
 * that no request could survive: the four the fields already mark `required`.
 * Cadence floors, cron grammar and name rules stay the daemon's call, and its
 * refusal is shown in the dialog. Mirrors the prototype's `scheduleFormProblem`.
 */
export function scheduleFormProblem(value: FormValue, schedule?: Schedule): string | null {
  if (!value.name.trim()) return "Name is required.";
  if (!value.prompt.trim()) return "Prompt is required.";
  const trigger = triggerFromValue(value, schedule);
  if (trigger.kind === "cron") {
    if (!trigger.expression) return "Cron expression is required.";
  } else {
    if (!value.oneShotAt && !schedule) return "Run time is required.";
    if (!trigger.at) return "Run time is not a valid date.";
  }
  return null;
}

/** The time a one-shot date runs at when no time has been chosen, as in the prototype. */
const DEFAULT_ONE_SHOT_TIME = "09:00";

/** What the one-shot Date and Time inputs show. */
export function oneShotInputs(value: FormValue): { date: string; time: string } {
  return { date: value.oneShotAt.slice(0, 10), time: value.oneShotTime };
}

/**
 * The trigger edit for a new Date input value: the run time becomes that date
 * at the chosen time, or at 9:00 if no time was ever chosen. An empty value (a
 * cleared date, or a segment mid-edit) leaves no run time, so the form cannot
 * be sent, but it never touches the time.
 */
export function oneShotDatePatch(value: FormValue, date: string): TriggerPatch {
  if (!date) return { oneShotAt: "" };
  const time = value.oneShotTime || DEFAULT_ONE_SHOT_TIME;
  return { oneShotAt: `${date}T${time}`, oneShotTime: time };
}

/**
 * The trigger edit for a new Time input value. The time is kept even without
 * a date, and joins the date once there is one. An empty value is a segment
 * mid-edit: the last time stays.
 */
export function oneShotTimePatch(value: FormValue, time: string): TriggerPatch {
  if (!time) return {};
  const date = value.oneShotAt.slice(0, 10);
  return { oneShotAt: date ? `${date}T${time}` : "", oneShotTime: time };
}

/** What the phrase input last produced — drives the line under it. */
export type PhraseOutcome =
  | SchedulePhraseResult
  | { cron: string; kind: "raw-cron" }
  | { kind: "none" };

/**
 * Compiles the phrase input into the trigger edit it implies, if any. The
 * patch still goes through {@link withTriggerPatch}, so a phrase cannot move
 * a locked trigger either.
 */
export function phraseTriggerPatch(
  text: string,
  now?: Date,
): { outcome: PhraseOutcome; patch: TriggerPatch | null } {
  const compiled = compileSchedulePhrase(text, now);
  if (compiled?.kind === "cron") {
    return { outcome: compiled, patch: { cron: compiled.cron, triggerKind: "cron" } };
  }
  if (compiled?.kind === "one-shot") {
    const oneShotAt = toLocalDateTimeInput(compiled.at);
    return {
      outcome: compiled,
      patch: { oneShotAt, oneShotTime: oneShotAt.slice(11, 16), triggerKind: "once" },
    };
  }
  if (looksLikeCron(text)) {
    // The unmatched five-field fallback: it IS the cron, verbatim; the
    // builder lands on Custom (or the shape it happens to parse as).
    const cron = text.trim();
    return { outcome: { cron, kind: "raw-cron" }, patch: { cron, triggerKind: "cron" } };
  }
  return { outcome: { kind: "none" }, patch: null };
}

function browserTimezone() {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

function toLocalInput(value: string) {
  const date = new Date(value);
  const offset = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
}
