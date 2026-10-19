// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import {
  bodyFromValue,
  displayedValue,
  type FormValue,
  isTriggerEditable,
  oneShotDatePatch,
  oneShotInputs,
  oneShotTimePatch,
  phraseTriggerPatch,
  type Schedule,
  scheduleFormProblem,
  TRIGGER_FIELDS,
  type TriggerPatch,
  triggerFromValue,
  valueFromSchedule,
  withTriggerPatch,
} from "./schedule-form-value";
import { oneShotInstant, toLocalDateTimeInput } from "./schedule-phrase";

function schedule(overrides: Partial<Schedule> = {}): Schedule {
  return {
    enabled: true,
    fireCount: 0,
    lastFireAt: "",
    lastFireSessionId: "",
    maxFires: 0,
    mode: "plan",
    modelId: "",
    mutating: false,
    name: "daily-summary",
    nextFireAt: "2027-01-15T08:00:00.000Z",
    oneShotMaxRetries: 0,
    oneShotRetry: false,
    owner: "",
    profile: "all",
    prompt: "Summarize the day",
    providerId: "",
    status: "scheduled",
    trigger: { expression: "0 9 * * *", kind: "cron", timezone: "Europe/Rome" },
    ...overrides,
  };
}

describe("schedule form value mapping", () => {
  it("saving an unchanged form sends exactly the stored mode and write flag", () => {
    const stored = [
      { mode: "plan", mutating: false },
      { mode: "plan", mutating: true },
      { mode: "default", mutating: true },
      { mode: "acceptEdits", mutating: true },
    ] as const;
    for (const permission of stored) {
      const existing = schedule(permission);
      const body = bodyFromValue(valueFromSchedule(existing), existing);
      expect({ mode: body.mode, mutating: body.mutating }, JSON.stringify(permission)).toEqual(
        permission,
      );
    }
  });

  it("sends an existing schedule's trigger exactly as stored", () => {
    for (const trigger of [
      { expression: "0 9 * * *", kind: "cron", timezone: "" },
      { expression: "*/15 * * * *", kind: "cron", timezone: "Europe/Rome" },
      { at: "2027-01-15T08:00:00.000Z", kind: "once" },
    ] as const) {
      const existing = schedule({ trigger });
      const value = valueFromSchedule(existing);
      // Even a stale field value cannot move the trigger of a saved schedule.
      const body = bodyFromValue({ ...value, cron: "0 10 * * *", timezone: "UTC" }, existing);
      expect(body.trigger).toEqual(trigger);
    }
  });

  it("maps the write toggle of a new schedule to its permission mode", () => {
    const value = valueFromSchedule();
    expect(bodyFromValue(value)).toMatchObject({ mode: "plan", mutating: false });
    expect(bodyFromValue({ ...value, allowWrites: true })).toMatchObject({
      mode: "acceptEdits",
      mutating: true,
    });
    expect(bodyFromValue({ ...value, allowWrites: true, writeMode: "default" })).toMatchObject({
      mode: "default",
      mutating: true,
    });
  });

  it("turning writes off on a writing schedule returns it to read-only plan mode", () => {
    const existing = schedule({ mode: "acceptEdits", mutating: true });
    const body = bodyFromValue({ ...valueFromSchedule(existing), allowWrites: false }, existing);
    expect(body).toMatchObject({ mode: "plan", mutating: false });
  });
});

const STORED_TRIGGERS = [
  { expression: "*/15 * * * *", kind: "cron", timezone: "Europe/Rome" },
  { at: "2027-01-15T08:00:00.000Z", kind: "once" },
] as const;

// A Wednesday, 10:00 local: anchors the one-shot phrases.
const phraseNow = new Date(2026, 8, 16, 10, 0);

/**
 * Every edit a trigger control can make: the tabs, the cron builder and
 * expression, the timezone, the one-shot date/time, and the phrase field.
 */
function triggerEdits(): TriggerPatch[] {
  const phrased = ["in 2 hours", "tomorrow at 8am", "daily at 9am", "*/10 * * * *"].map(
    (text) => phraseTriggerPatch(text, phraseNow).patch,
  );
  return [
    { triggerKind: "cron" },
    { triggerKind: "once" },
    { cron: "0 10 * * 1-5" },
    { timezone: "UTC" },
    { timezone: "" },
    { oneShotAt: "2030-06-01T12:00" },
    { oneShotAt: toLocalDateTimeInput(Date.now()) },
    { oneShotOriginal: "" },
    ...phrased.filter((patch): patch is TriggerPatch => patch !== null),
  ];
}

describe("schedule trigger lock", () => {
  it("locks every trigger control of an existing schedule", () => {
    const edits = triggerEdits();
    // The edits reach every field the server compares as the trigger.
    const touched = new Set(edits.flatMap((patch) => Object.keys(patch)));
    expect([...touched].sort()).toEqual([...TRIGGER_FIELDS].sort());

    for (const trigger of STORED_TRIGGERS) {
      const existing = schedule({ trigger });
      expect(isTriggerEditable(existing)).toBe(false);
      const value = valueFromSchedule(existing);
      for (const patch of edits) {
        const edited = withTriggerPatch(value, patch, existing);
        expect(edited, JSON.stringify(patch)).toBe(value);
        expect(bodyFromValue(displayedValue(edited, existing), existing).trigger).toEqual(trigger);
      }
    }
  });

  it("displays exactly the trigger an existing schedule sends", () => {
    for (const trigger of STORED_TRIGGERS) {
      const existing = schedule({ trigger });
      const stored = valueFromSchedule(existing);
      // A form state whose trigger fields drifted, however that happened.
      const drifted = {
        ...stored,
        cron: "0 10 * * *",
        oneShotAt: "2030-06-01T12:00",
        oneShotOriginal: "",
        oneShotTime: "23:59",
        prompt: "Edited prompt",
        timezone: "UTC",
        triggerKind: trigger.kind === "cron" ? "once" : "cron",
      } as const;
      const shown = displayedValue(drifted, existing);
      for (const field of TRIGGER_FIELDS) {
        expect(shown[field], field).toBe(stored[field]);
      }
      // Non-trigger edits still come through.
      expect(shown.prompt).toBe("Edited prompt");
      expect(triggerFromValue(shown, existing)).toEqual(trigger);
      if (trigger.kind === "once") {
        // The date/time input renders the stored instant, not another one.
        expect(oneShotInstant(shown.oneShotAt, shown.oneShotOriginal)).toBe(trigger.at);
      } else {
        expect({ expression: shown.cron, timezone: shown.timezone }).toEqual({
          expression: trigger.expression,
          timezone: trigger.timezone,
        });
      }
    }
  });

  it("round-trips hidden limits and retry policy unchanged", () => {
    const cron = schedule({ maxFires: 7 });
    expect(bodyFromValue(valueFromSchedule(cron), cron)).toMatchObject({
      maxFires: 7,
      oneShotMaxRetries: 0,
      oneShotRetry: false,
      trigger: cron.trigger,
    });

    const once = schedule({
      oneShotMaxRetries: 5,
      oneShotRetry: true,
      trigger: STORED_TRIGGERS[1],
    });
    expect(bodyFromValue(valueFromSchedule(once), once)).toMatchObject({
      maxFires: 0,
      oneShotMaxRetries: 5,
      oneShotRetry: true,
      trigger: STORED_TRIGGERS[1],
    });
  });

  it("keeps trigger controls editable for a new schedule", () => {
    expect(isTriggerEditable()).toBe(true);
    const value = valueFromSchedule();
    expect(displayedValue(value)).toBe(value);

    const cron = withTriggerPatch(value, { cron: " 0 10 * * 1-5 ", timezone: " UTC " });
    expect(displayedValue(cron)).toBe(cron);
    expect(bodyFromValue(cron).trigger).toEqual({
      expression: "0 10 * * 1-5",
      kind: "cron",
      timezone: "UTC",
    });

    const phrased = phraseTriggerPatch("tomorrow at 8am", phraseNow).patch;
    expect(phrased).not.toBeNull();
    const once = withTriggerPatch(value, phrased ?? {});
    expect(once.triggerKind).toBe("once");
    // The wall clock is canonicalised to the absolute instant it names.
    expect(bodyFromValue(once).trigger).toEqual({
      at: new Date(2026, 8, 17, 8, 0).toISOString(),
      kind: "once",
    });

    const at = new Date(2030, 5, 1, 12, 0).getTime();
    const typed = withTriggerPatch(value, {
      oneShotAt: toLocalDateTimeInput(at),
      triggerKind: "once",
    });
    expect(bodyFromValue(typed).trigger).toEqual({ at: new Date(at).toISOString(), kind: "once" });
  });
});

describe("scheduleFormProblem", () => {
  const ready = { ...valueFromSchedule(), name: "nightly", prompt: "Summarize" };

  it("names the first missing field and clears once the form could be sent", () => {
    expect(scheduleFormProblem(valueFromSchedule())).toBe("Name is required.");
    expect(scheduleFormProblem({ ...ready, prompt: "  " })).toBe("Prompt is required.");
    expect(scheduleFormProblem({ ...ready, cron: " " })).toBe("Cron expression is required.");
    expect(scheduleFormProblem(ready)).toBeNull();
  });

  it("requires a valid one-shot run time", () => {
    const once = { ...ready, triggerKind: "once" as const };
    expect(scheduleFormProblem({ ...once, oneShotAt: "" })).toBe("Run time is required.");
    expect(scheduleFormProblem({ ...once, oneShotAt: "2030-13-45T25:00" })).toBe(
      "Run time is not a valid date.",
    );
    expect(scheduleFormProblem({ ...once, oneShotAt: "2030-01-02T15:30" })).toBeNull();
  });

  it("checks an existing schedule against its stored, locked trigger", () => {
    const stored = schedule({ trigger: { at: "2030-01-02T08:00:00.000Z", kind: "once" } });
    const value = { ...valueFromSchedule(stored), cron: "", oneShotAt: "" };
    expect(scheduleFormProblem(displayedValue(value, stored), stored)).toBeNull();
    expect(scheduleFormProblem({ ...value, name: "" }, stored)).toBe("Name is required.");
  });
});

describe("one-shot date and time inputs", () => {
  const blank = { ...valueFromSchedule(), triggerKind: "once" as const };
  const apply = (value: FormValue, patch: TriggerPatch) => withTriggerPatch(value, patch);

  it("runs a date at 9:00 only when no time was ever chosen", () => {
    const dated = apply(blank, oneShotDatePatch(blank, "2030-01-02"));
    expect(dated.oneShotAt).toBe("2030-01-02T09:00");
    expect(oneShotInputs(dated)).toEqual({ date: "2030-01-02", time: "09:00" });
  });

  it("keeps a time chosen before the date and joins it to the date", () => {
    const timed = apply(blank, oneShotTimePatch(blank, "15:30"));
    expect(timed.oneShotAt).toBe("");
    expect(oneShotInputs(timed)).toEqual({ date: "", time: "15:30" });
    const dated = apply(timed, oneShotDatePatch(timed, "2030-01-02"));
    expect(dated.oneShotAt).toBe("2030-01-02T15:30");
  });

  it("keeps the time when the date is cleared or changed", () => {
    const dated = apply(blank, oneShotDatePatch(blank, "2030-01-02"));
    const set = apply(dated, oneShotTimePatch(dated, "15:30"));
    expect(set.oneShotAt).toBe("2030-01-02T15:30");
    const cleared = apply(set, oneShotDatePatch(set, ""));
    expect(cleared.oneShotAt).toBe("");
    expect(oneShotInputs(cleared)).toEqual({ date: "", time: "15:30" });
    expect(scheduleFormProblem({ ...cleared, name: "n", prompt: "p" })).toBe(
      "Run time is required.",
    );
    const changed = apply(set, oneShotDatePatch(set, "2030-02-03"));
    expect(changed.oneShotAt).toBe("2030-02-03T15:30");
  });

  it("ignores a transient empty time", () => {
    expect(oneShotTimePatch({ ...blank, oneShotTime: "15:30" }, "")).toEqual({});
  });

  it("reads a stored one-shot's date and time", () => {
    const stored = schedule({ trigger: { at: "2030-01-02T08:00:00.000Z", kind: "once" } });
    const value = valueFromSchedule(stored);
    expect(oneShotInputs(value)).toEqual({
      date: value.oneShotAt.slice(0, 10),
      time: value.oneShotAt.slice(11, 16),
    });
    expect(value.oneShotTime).not.toBe("");
  });

  it("sets the time from a one-shot phrase", () => {
    const patch = phraseTriggerPatch("tomorrow at 8am", phraseNow).patch;
    expect(patch).toMatchObject({ oneShotAt: "2026-09-17T08:00", oneShotTime: "08:00" });
  });
});
