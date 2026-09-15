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
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import {
  builderToCron,
  type CronRepeat,
  cronToBuilder,
} from "@/lib/cron-builder";
import { describeCron, ordinal } from "@/lib/formatters";
import { PERMISSION_MODES, type ScheduleSpecDraft } from "@/lib/protocol";

/**
 * Form state for authoring a schedule.
 *
 * Write access is a single opt-in: `allowWrites` couples `mutating: true` with
 * a write-capable permission mode, and `writeMode` only exists under that
 * opt-in. The default posture (`allowWrites: false`) is always
 * `mutating: false` + plan mode — the invalid pairing (mutating in plan mode,
 * or writes without the opt-in) cannot be expressed by this state at all.
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
  };
}

function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone ?? "";
  } catch {
    return "";
  }
}

function toLocalDateTimeInput(ms: number): string {
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
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
  };
}

/**
 * Build the wire draft. `base` is the stored draft on an edit — it supplies
 * the spec fields this form has no controls for (profile, limits)
 * so they survive the PUT-replaces-everything contract.
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
    profile: base?.profile ?? "",
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

export function ScheduleFormFields({
  value,
  onChange,
}: {
  value: ScheduleFormValue;
  onChange: (patch: Partial<ScheduleFormValue>) => void;
}) {
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
        <Tabs
          value={value.triggerKind}
          onValueChange={(v) =>
            onChange({ triggerKind: v as ScheduleFormValue["triggerKind"] })
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
              cron={value.cron}
              onCronChange={(cron) => onChange({ cron })}
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
                    onChange({
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
                    onChange({
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
    </div>
  );
}

const REPEAT_OPTIONS: { value: CronRepeat; label: string }[] = [
  { value: "daily", label: "Daily" },
  { value: "weekdays", label: "Weekdays" },
  { value: "weekly", label: "Weekly" },
  { value: "monthly", label: "Monthly" },
  { value: "custom", label: "Custom" },
];

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

  /** Re-derive the cron string from the builder with one control changed. */
  const rebuild = (patch: {
    repeat?: Exclude<CronRepeat, "custom">;
    time?: string;
    weekday?: number;
    monthday?: number;
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

        {repeat !== "custom" && (
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
