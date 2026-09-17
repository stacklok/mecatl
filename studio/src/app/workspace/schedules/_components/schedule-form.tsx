"use client";

import { useState } from "react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import {
  builderToCron,
  type CronIntervalUnit,
  type CronRepeat,
  cronToBuilder,
} from "@/lib/cron-builder";
import { describeCron, ordinal } from "@/lib/formatters";
import { PERMISSION_MODES, type ScheduleSpecDraft } from "@/lib/protocol";
import {
  compileSchedulePhrase,
  looksLikeCron,
  type SchedulePhraseResult,
  toLocalDateTimeInput,
} from "@/lib/schedule-phrase";
import {
  normalizeToolProfile,
  type SessionToolProfile,
  TOOL_PROFILE_OPTIONS,
} from "@/lib/tool-profile";

/**
 * Form state for authoring a schedule.
 *
 * Write access is a single opt-in: `allowWrites` couples `mutating: true` with
 * a write-capable permission mode, and `writeMode` only exists under that
 * opt-in. The default posture (`allowWrites: false`) is always
 * `mutating: false` + plan mode — the invalid pairing (mutating in plan mode,
 * or writes without the opt-in) cannot be expressed by this state at all.
 * Both fields are user-editable: `ScheduleFormFields` renders the
 * "Allow file and shell writes" switch and, under it, the permission-mode
 * picker (the TUI form's y/n mutating toggle).
 *
 * `profile` is the fire session's TOOL PROFILE (the spec's `profile`, the
 * same field a chat's create carries): "" = all tools, "no-fs" = the
 * file-less catalog (no Shell/Read/Edit/Write…; web tools remain). It is
 * independent of the write opt-in — a no-fs schedule with writes on simply
 * has nothing file-shaped left to write with.
 */
export interface ScheduleFormValue {
  name: string;
  prompt: string;
  triggerKind: "cron" | "one-shot";
  cron: string;
  timezone: string;
  /** Numeric text; empty or "0" means unlimited (the wire's meaning of 0). */
  maxFires: string;
  /** `datetime-local` value, interpreted in the browser's local time. */
  oneShotAt: string;
  oneShotRetry: boolean;
  oneShotMaxRetries: string;
  allowWrites: boolean;
  writeMode: "default" | "accept_edits";
  profile: SessionToolProfile;
}

export function emptyScheduleForm(): ScheduleFormValue {
  return {
    name: "",
    prompt: "",
    triggerKind: "cron",
    cron: "0 9 * * *",
    timezone: browserTimezone(),
    maxFires: "",
    oneShotAt: "",
    oneShotRetry: false,
    oneShotMaxRetries: "3",
    allowWrites: false,
    writeMode: "accept_edits",
    profile: "",
  };
}

function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone ?? "";
  } catch {
    return "";
  }
}

/**
 * Seed the form from a stored draft (the edit path). A spec whose
 * mode/mutating pairing this form cannot express (e.g. a CLI-authored
 * `mutating: false` + default mode) is coerced to the nearest expressible
 * posture — the form's invariant wins over round-tripping an odd pairing.
 */
export function formFromDraft(draft: ScheduleSpecDraft): ScheduleFormValue {
  const base = emptyScheduleForm();
  return {
    ...base,
    name: draft.name,
    prompt: draft.prompt,
    triggerKind: draft.trigger.kind,
    cron: draft.trigger.kind === "cron" ? draft.trigger.cron : base.cron,
    timezone:
      draft.trigger.kind === "cron" && draft.trigger.timezone
        ? draft.trigger.timezone
        : base.timezone,
    maxFires: draft.maxFires > 0 ? String(draft.maxFires) : "",
    oneShotAt:
      draft.trigger.kind === "one-shot"
        ? toLocalDateTimeInput(draft.trigger.at)
        : "",
    oneShotRetry: draft.oneShotRetry,
    oneShotMaxRetries:
      draft.oneShotMaxRetries > 0 ? String(draft.oneShotMaxRetries) : "3",
    allowWrites: draft.mutating,
    writeMode:
      draft.mode === PERMISSION_MODES.PERMISSION_MODE_DEFAULT
        ? "default"
        : "accept_edits",
    profile: normalizeToolProfile(draft.profile),
  };
}

/**
 * Build the wire draft. `base` is the stored draft on an edit — it supplies
 * the spec fields this form has no controls for (limits) so they survive
 * the PUT-replaces-everything contract.
 */
export function draftFromForm(
  value: ScheduleFormValue,
  base?: ScheduleSpecDraft,
): ScheduleSpecDraft {
  const mode = value.allowWrites
    ? value.writeMode === "default"
      ? PERMISSION_MODES.PERMISSION_MODE_DEFAULT
      : PERMISSION_MODES.PERMISSION_MODE_ACCEPT_EDITS
    : PERMISSION_MODES.PERMISSION_MODE_PLAN;
  return {
    name: value.name.trim(),
    prompt: value.prompt.trim(),
    trigger:
      value.triggerKind === "cron"
        ? {
            kind: "cron",
            cron: value.cron.trim(),
            timezone: value.timezone.trim(),
          }
        : { kind: "one-shot", at: new Date(value.oneShotAt).getTime() },
    profile: value.profile,
    mode,
    mutating: value.allowWrites,
    maxFires: Math.max(0, Number(value.maxFires) || 0),
    limits: base?.limits ?? {
      maxTurns: 0,
      maxToolCalls: 0,
      maxConsecutiveFailures: 0,
    },
    oneShotRetry: value.oneShotRetry,
    oneShotMaxRetries: value.oneShotRetry
      ? Math.max(0, Number(value.oneShotMaxRetries) || 0)
      : 0,
  };
}

/**
 * Client-side gate for the submit button only — shape checks a request could
 * never survive. Everything else (frequency floors, cron grammar, name rules)
 * is the daemon's call and its refusal is shown verbatim.
 */
export function scheduleFormProblem(value: ScheduleFormValue): string | null {
  if (!value.name.trim()) return "Name is required.";
  if (!value.prompt.trim()) return "Prompt is required.";
  if (value.triggerKind === "cron") {
    if (!value.cron.trim()) return "Cron expression is required.";
  } else {
    if (!value.oneShotAt) return "Run time is required.";
    if (Number.isNaN(new Date(value.oneShotAt).getTime()))
      return "Run time is not a valid date.";
  }
  return null;
}

/** Badge label for a wire permission mode; list and detail share the wording. */
export function permissionModeLabel(mode: number): string {
  switch (mode) {
    case PERMISSION_MODES.PERMISSION_MODE_DEFAULT:
      return "default";
    case PERMISSION_MODES.PERMISSION_MODE_PLAN:
      return "plan";
    case PERMISSION_MODES.PERMISSION_MODE_ACCEPT_EDITS:
      return "accept edits";
    default:
      return "unspecified";
  }
}

/** "YYYY-MM-DDTHH:MM" → its date and time halves ("" when absent). */
function oneShotParts(at: string): { date: string; time: string } {
  return { date: at.slice(0, 10), time: at.slice(11, 16) };
}

/** Local (not UTC) YYYY-MM-DD / HH:MM for a Date — presets and defaults. */
function localDate(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** Compose the datetime-local string, defaulting the missing half sensibly. */
function joinOneShot(date: string, time: string): string {
  if (!date && !time) return "";
  return `${date || localDate(new Date())}T${time || "09:00"}`;
}

/** What the phrase input last produced — drives the line under it. */
type PhraseOutcome =
  | SchedulePhraseResult
  | { kind: "raw-cron"; cron: string }
  | { kind: "none" };

const NO_PHRASE: PhraseOutcome = { kind: "none" };

const PHRASE_PLACEHOLDER =
  "every 30 minutes · daily at 9am · next monday 3pm · in 2 hours";
export const PHRASE_HINT =
  'Not recognised — try "every weekday at 9am" or a cron expression';

/** The plain-English preview of what a phrase compiled to. */
export function describePhraseOutcome(outcome: PhraseOutcome): string | null {
  switch (outcome.kind) {
    case "cron":
      return describeCron(outcome.cron);
    case "raw-cron": {
      // A five-field fallback the describer cannot read is still a cron;
      // say so rather than echo the raw string bare.
      const described = describeCron(outcome.cron);
      return described === outcome.cron
        ? `Cron expression ${outcome.cron}`
        : described;
    }
    case "one-shot":
      return `Once at ${new Date(outcome.at).toLocaleString()}`;
    case "none":
      return null;
  }
}

export function ScheduleFormFields({
  value,
  onChange,
}: {
  value: ScheduleFormValue;
  onChange: (patch: Partial<ScheduleFormValue>) => void;
}) {
  // The phrase is an authoring aid (the TUI's natural-language trigger): it
  // compiles INTO `value.cron` / `value.oneShotAt`, which stay the wire truth.
  // Its text and preview are local; a successful compile bumps
  // `phraseVersion`, which remounts the cron builder so its Custom pick
  // re-derives from the new string instead of sticking.
  const [phrase, setPhrase] = useState("");
  const [phraseOutcome, setPhraseOutcome] = useState<PhraseOutcome>(NO_PHRASE);
  const [phraseVersion, setPhraseVersion] = useState(0);

  /**
   * Trigger edits made through the structured controls (tabs, builder, the
   * one-shot date/time) consume the phrase: left in place, its text would
   * describe a trigger the form no longer holds.
   */
  const changeTrigger = (patch: Partial<ScheduleFormValue>) => {
    if (phrase) {
      setPhrase("");
      setPhraseOutcome(NO_PHRASE);
    }
    onChange(patch);
  };

  const handlePhrase = (text: string) => {
    setPhrase(text);
    const compiled = compileSchedulePhrase(text);
    let outcome: PhraseOutcome = NO_PHRASE;
    let patch: Partial<ScheduleFormValue> | null = null;
    if (compiled?.kind === "cron") {
      outcome = compiled;
      patch = { triggerKind: "cron", cron: compiled.cron };
    } else if (compiled?.kind === "one-shot") {
      outcome = compiled;
      patch = {
        triggerKind: "one-shot",
        oneShotAt: toLocalDateTimeInput(compiled.at),
      };
    } else if (looksLikeCron(text)) {
      // The TUI fallback: unmatched five-field input IS the cron, verbatim;
      // the builder lands on Custom (or the shape it happens to parse as).
      const cron = text.trim();
      outcome = { kind: "raw-cron", cron };
      patch = { triggerKind: "cron", cron };
    }
    setPhraseOutcome(outcome);
    if (patch) {
      setPhraseVersion((v) => v + 1);
      onChange(patch);
    }
  };

  const phrasePreview = describePhraseOutcome(phraseOutcome);
  const phraseNote = phrasePreview ?? (phrase.trim() ? PHRASE_HINT : null);

  return (
    <div className="space-y-4">
      <div className="space-y-3">
        <Label htmlFor="schedule-name">Name</Label>
        <Input
          id="schedule-name"
          value={value.name}
          onChange={(e) => onChange({ name: e.target.value })}
          placeholder="daily-standup-summary"
          required
        />
      </div>

      <div className="space-y-3">
        <Label htmlFor="schedule-prompt">Prompt</Label>
        <Textarea
          id="schedule-prompt"
          value={value.prompt}
          onChange={(e) => onChange({ prompt: e.target.value })}
          placeholder="Summarise yesterday's activity and post it to the team channel."
          rows={3}
          required
        />
      </div>

      <div className="space-y-3">
        <Label>Trigger</Label>
        <div className="space-y-2">
          <Label
            htmlFor="schedule-phrase"
            className="text-xs font-normal text-muted-foreground"
          >
            Describe the schedule
          </Label>
          <Input
            id="schedule-phrase"
            value={phrase}
            onChange={(e) => handlePhrase(e.target.value)}
            placeholder={PHRASE_PLACEHOLDER}
            autoComplete="off"
            aria-describedby={phraseNote ? "schedule-phrase-note" : undefined}
          />
          {phraseNote && (
            <p
              id="schedule-phrase-note"
              className="text-xs text-muted-foreground"
            >
              {phraseNote}
            </p>
          )}
        </div>
        <Tabs
          value={value.triggerKind}
          onValueChange={(v) =>
            changeTrigger({
              triggerKind: v as ScheduleFormValue["triggerKind"],
            })
          }
        >
          <TabsList className="grid w-full grid-cols-2 rounded-full bg-muted p-1">
            <TabsTrigger
              value="cron"
              className="rounded-full data-[state=active]:bg-background data-[state=active]:shadow-sm"
            >
              Recurring
            </TabsTrigger>
            <TabsTrigger
              value="one-shot"
              className="rounded-full data-[state=active]:bg-background data-[state=active]:shadow-sm"
            >
              Run once
            </TabsTrigger>
          </TabsList>
        </Tabs>

        {value.triggerKind === "cron" ? (
          <div className="space-y-3">
            <CronBuilderFields
              key={phraseVersion}
              cron={value.cron}
              onCronChange={(cron) => changeTrigger({ cron })}
            />
          </div>
        ) : (
          <div className="space-y-3">
            <div className="space-y-3">
              <Label htmlFor="schedule-one-shot-date">Run at</Label>
              {/* Split date + time beats the native datetime-local widget, and
                  the presets cover the common cases in one click. */}
              <div className="flex flex-wrap items-center gap-2">
                <Input
                  id="schedule-one-shot-date"
                  aria-label="Date"
                  type="date"
                  value={oneShotParts(value.oneShotAt).date}
                  onChange={(e) =>
                    changeTrigger({
                      oneShotAt: joinOneShot(
                        e.target.value,
                        oneShotParts(value.oneShotAt).time,
                      ),
                    })
                  }
                  className="w-fit"
                  required
                />
                <Input
                  aria-label="Time"
                  type="time"
                  value={oneShotParts(value.oneShotAt).time}
                  onChange={(e) => {
                    if (!e.target.value) return;
                    changeTrigger({
                      oneShotAt: joinOneShot(
                        oneShotParts(value.oneShotAt).date,
                        e.target.value,
                      ),
                    });
                  }}
                  className="w-fit"
                  required
                />
              </div>
            </div>
          </div>
        )}
      </div>

      {/* Write access is the one posture choice the form owns: off is plan
          mode + mutating:false, on is mutating:true with a write-capable mode.
          The pairing the daemon rejects (mutating:false outside plan mode)
          cannot be built here. */}
      <div className="space-y-3 rounded-lg border px-3 py-2.5">
        <div className="flex items-center justify-between gap-3">
          <Label htmlFor="schedule-allow-writes">
            Allow file and shell writes
          </Label>
          <Switch
            id="schedule-allow-writes"
            checked={value.allowWrites}
            onCheckedChange={(checked) => onChange({ allowWrites: checked })}
          />
        </div>
        <p className="text-xs text-muted-foreground">
          {value.allowWrites ? WRITE_ACCESS_ON_NOTE : WRITE_ACCESS_OFF_NOTE}
        </p>
        {value.allowWrites && (
          <div className="space-y-2">
            <Label htmlFor="schedule-write-mode">Permission mode</Label>
            <Select
              value={value.writeMode}
              onValueChange={(v) =>
                onChange({ writeMode: v as ScheduleFormValue["writeMode"] })
              }
            >
              <SelectTrigger id="schedule-write-mode" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="accept_edits">
                  Accept edits — file edits proceed without approval
                </SelectItem>
                <SelectItem value="default">
                  Default — standard permission rules apply
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
        )}
      </div>

      {/* The fire session's tool profile — the same `profile` a chat's create
          carries. Independent of the write opt-in above. */}
      <div className="space-y-2">
        <Label htmlFor="schedule-profile">Tool profile</Label>
        <Select
          value={value.profile || ALL_TOOLS_VALUE}
          onValueChange={(v) =>
            onChange({ profile: v === ALL_TOOLS_VALUE ? "" : "no-fs" })
          }
        >
          <SelectTrigger id="schedule-profile" className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {TOOL_PROFILE_OPTIONS.map((option) => (
              <SelectItem
                key={option.id || ALL_TOOLS_VALUE}
                value={option.id || ALL_TOOLS_VALUE}
              >
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p id="schedule-profile-note" className="text-xs text-muted-foreground">
          {toolProfileDescription(value.profile)}
        </p>
      </div>
    </div>
  );
}

/** Radix Select refuses an empty item value; the default profile ("") rides
 *  this sentinel inside the control only and never reaches the draft. */
const ALL_TOOLS_VALUE = "all";

/** The one-line consequence under the tool-profile picker, per profile. */
export function toolProfileDescription(profile: SessionToolProfile): string {
  return (
    TOOL_PROFILE_OPTIONS.find((option) => option.id === profile)?.description ??
    TOOL_PROFILE_OPTIONS[0].description
  );
}

/** The one-line consequence under the write-access switch, per state. */
export const WRITE_ACCESS_OFF_NOTE =
  "Read-only: the task runs in plan mode and cannot edit files or run mutating commands.";
export const WRITE_ACCESS_ON_NOTE =
  "The task can edit files and run commands unattended; anything that would need a human approval is denied while it runs headless.";

const REPEAT_OPTIONS: { value: CronRepeat; label: string }[] = [
  { value: "daily", label: "Daily" },
  { value: "weekdays", label: "Weekdays" },
  { value: "weekly", label: "Weekly" },
  { value: "monthly", label: "Monthly" },
  { value: "interval", label: "Every…" },
  { value: "custom", label: "Custom" },
];

const INTERVAL_UNIT_OPTIONS: { value: CronIntervalUnit; label: string }[] = [
  { value: "minutes", label: "Minutes" },
  { value: "hours", label: "Hours" },
];

/** The daemon-independent step ceiling per unit (a cron field's own range). */
const INTERVAL_MAX: Record<CronIntervalUnit, number> = {
  minutes: 59,
  hours: 23,
};

/** Cron day-of-week values, Monday-first for display (cron's 0 is Sunday). */
const WEEKDAY_OPTIONS = [
  { value: 1, label: "Monday" },
  { value: 2, label: "Tuesday" },
  { value: 3, label: "Wednesday" },
  { value: 4, label: "Thursday" },
  { value: 5, label: "Friday" },
  { value: 6, label: "Saturday" },
  { value: 0, label: "Sunday" },
];

const MONTHDAY_OPTIONS = Array.from({ length: 28 }, (_, i) => i + 1);

/**
 * The structured cron editor. The form value keeps carrying the cron STRING —
 * this is purely a nicer editor for it: the builder state is derived from the
 * string via `cronToBuilder` on every render (so an edited schedule
 * pre-populates, and a schedule built here round-trips losslessly), and every
 * control change writes the derived string back via `builderToCron`. A cron
 * the builder cannot express lands on Custom with the raw string intact; the
 * Custom pick is the one piece of local state, so choosing it sticks even
 * while the string still parses as a builder shape.
 */
function CronBuilderFields({
  cron,
  onCronChange,
}: {
  cron: string;
  onCronChange: (cron: string) => void;
}) {
  const parsed = cronToBuilder(cron);
  const [customPicked, setCustomPicked] = useState(
    () => parsed.repeat === "custom",
  );
  const repeat: CronRepeat = customPicked ? "custom" : parsed.repeat;
  // The interval step as typed, while the field has focus: a controlled
  // number input snaps back on every keystroke otherwise (clearing "30" to
  // type "5" would read "305"). Blur drops the draft and shows the committed,
  // clamped value.
  const [everyDraft, setEveryDraft] = useState<string | null>(null);

  /** Re-derive the cron string from the builder with one control changed. */
  const rebuild = (patch: {
    repeat?: Exclude<CronRepeat, "custom">;
    time?: string;
    weekday?: number;
    monthday?: number;
    every?: number;
    unit?: CronIntervalUnit;
  }) => {
    const next = { ...parsed, ...patch };
    if (next.repeat === "custom") return;
    onCronChange(builderToCron({ ...next, repeat: next.repeat }));
  };

  const cronPreview = describeCron(cron.trim());
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap gap-3">
        <div className="min-w-[130px] flex-1 space-y-3">
          <Label htmlFor="schedule-repeat">Repeat</Label>
          <Select
            value={repeat}
            onValueChange={(v) => {
              const next = v as CronRepeat;
              if (next === "custom") {
                setCustomPicked(true);
                return;
              }
              setCustomPicked(false);
              rebuild({ repeat: next });
            }}
          >
            <SelectTrigger id="schedule-repeat" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {REPEAT_OPTIONS.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        {repeat === "weekly" && (
          <div className="min-w-[130px] flex-1 space-y-3">
            <Label htmlFor="schedule-weekday">On</Label>
            <Select
              value={String(parsed.weekday)}
              onValueChange={(v) => rebuild({ weekday: Number(v) })}
            >
              <SelectTrigger id="schedule-weekday" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {WEEKDAY_OPTIONS.map((day) => (
                  <SelectItem key={day.value} value={String(day.value)}>
                    {day.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}

        {repeat === "monthly" && (
          <div className="min-w-[130px] flex-1 space-y-3">
            <Label htmlFor="schedule-monthday">On the</Label>
            <Select
              value={String(parsed.monthday)}
              onValueChange={(v) => rebuild({ monthday: Number(v) })}
            >
              <SelectTrigger id="schedule-monthday" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {MONTHDAY_OPTIONS.map((day) => (
                  <SelectItem key={day} value={String(day)}>
                    {ordinal(day)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}

        {repeat === "interval" && (
          <>
            <div className="w-[90px] space-y-3">
              <Label htmlFor="schedule-interval-every">Every</Label>
              <Input
                id="schedule-interval-every"
                type="number"
                inputMode="numeric"
                min={1}
                max={INTERVAL_MAX[parsed.unit]}
                step={1}
                value={everyDraft ?? String(parsed.every)}
                onChange={(e) => {
                  setEveryDraft(e.target.value);
                  // The builder clamps an out-of-range step; an empty field
                  // commits nothing until a number is typed.
                  if (e.target.value)
                    rebuild({ every: Number(e.target.value) });
                }}
                onBlur={() => setEveryDraft(null)}
              />
            </div>
            <div className="min-w-[130px] flex-1 space-y-3">
              <Label htmlFor="schedule-interval-unit">Unit</Label>
              <Select
                value={parsed.unit}
                onValueChange={(v) => rebuild({ unit: v as CronIntervalUnit })}
              >
                <SelectTrigger id="schedule-interval-unit" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {INTERVAL_UNIT_OPTIONS.map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {option.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </>
        )}

        {repeat !== "custom" && repeat !== "interval" && (
          <div className="w-[120px] space-y-3">
            <Label htmlFor="schedule-time">At</Label>
            <Input
              id="schedule-time"
              type="time"
              value={parsed.time}
              onChange={(e) => {
                // Segment editing fires transient empty values; rebuilding on
                // those snaps the field back to the default mid-edit.
                if (e.target.value) rebuild({ time: e.target.value });
              }}
            />
          </div>
        )}
      </div>

      {repeat === "custom" && (
        <div className="space-y-3">
          <Label htmlFor="schedule-cron">Cron expression</Label>
          <Input
            id="schedule-cron"
            value={cron}
            onChange={(e) => onCronChange(e.target.value)}
            placeholder="0 9 * * *"
            className="font-mono"
            required
          />
        </div>
      )}

      {/* The builder's own controls already read as plain English; the preview
          only earns its place under a raw Custom expression. */}
      {repeat === "custom" && cronPreview !== cron.trim() && (
        <p className="text-xs text-muted-foreground">{cronPreview}</p>
      )}
    </div>
  );
}
