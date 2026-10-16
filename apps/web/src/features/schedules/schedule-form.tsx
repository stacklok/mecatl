// SPDX-License-Identifier: Apache-2.0

import { useState } from "react";
import { Alert, AlertDescription } from "../../components/ui/alert";
import { Button } from "../../components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../../components/ui/dialog";
import { Input } from "../../components/ui/input";
import { Label } from "../../components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../../components/ui/select";
import { Switch } from "../../components/ui/switch";
import { Tabs, TabsList, TabsTrigger } from "../../components/ui/tabs";
import { Textarea } from "../../components/ui/textarea";
import { ordinal } from "../../lib/formatters";
import { useShortcutSuppression } from "../shortcuts/shortcut-provider";
import {
  builderToCron,
  type CronIntervalUnit,
  type CronRepeat,
  cronToBuilder,
} from "./cron-builder";
import { describeCron } from "./describe-cron";
import {
  bodyFromValue,
  displayedValue,
  type FormValue,
  isTriggerEditable,
  type PhraseOutcome,
  phraseTriggerPatch,
  type Schedule,
  type ScheduleBody,
  type TriggerPatch,
  triggerFromValue,
  valueFromSchedule,
  withTriggerPatch,
} from "./schedule-form-value";

const TRIGGER_LOCKED_NOTE =
  "The trigger of an existing task can't be changed, because Mecatl keeps its next run time when the task is saved. To change when it runs, create a new scheduled task and delete this one.";

const NO_PHRASE: PhraseOutcome = { kind: "none" };

/**
 * The write-mode options offered only to keep a stored writing schedule's mode
 * as it is: modes Studio never offers for a new schedule. Accept edits and
 * default are always offered, so they have no entry here.
 */
const KEPT_MODE_OPTIONS: Partial<Record<Schedule["mode"], string>> = {
  auto: "Keep Auto, set outside Studio",
  plan: "Plan only, changes are refused",
  serverDefault: "Keep the server's default mode",
  yolo: "Keep YOLO, set outside Studio",
};

const PHRASE_PLACEHOLDER = "every 30 minutes · daily at 9am · next monday 3pm · in 2 hours";
const PHRASE_HINT = 'Not recognised — try "every weekday at 9am" or a cron expression';

// SPEC: Editing excludes immutable name, trigger, and placement-bound tool
// profile changes. Timezone, run limits, and retry policy remain wire state but
// are not visible controls; edits round-trip their stored values unchanged.
// Create and update bodies still pass through the authenticated BFF contract.

/**
 * DECISION: A one-shot whose time has passed stays inspect-only, whatever its
 * status, because the backend rejects any update that keeps a past trigger
 * (#2146). Completed crons may still change prompt and permission posture, but
 * reopening or rescheduling belongs to #2149.
 */
export function canEditSchedule(schedule: Pick<Schedule, "status" | "trigger">, now = Date.now()) {
  if (schedule.trigger.kind !== "once") return true;
  return schedule.status !== "completed" && !(Date.parse(schedule.trigger.at) <= now);
}

/** The plain-English preview of what a phrase compiled to. */
function describePhraseOutcome(outcome: PhraseOutcome): string | null {
  if (outcome.kind === "none") return null;
  if (outcome.kind === "one-shot") return `Once at ${new Date(outcome.at).toLocaleString()}`;
  // A five-field fallback the describer cannot read is still a cron; say so
  // rather than echo the raw string bare.
  const described = describeCron(outcome.cron);
  return outcome.kind === "raw-cron" && described === outcome.cron
    ? `Cron expression ${outcome.cron}`
    : described;
}

export function ScheduleForm({
  schedule,
  onCancel,
  onSubmit,
}: {
  schedule?: Schedule;
  onCancel: () => void;
  onSubmit: (body: ScheduleBody) => Promise<void>;
}) {
  // The form is mounted only while open: global shortcuts such as `/` must not
  // act on the page behind it when focus sits on a button, tab, or switch.
  useShortcutSuppression(true);
  const [value, setValue] = useState<FormValue>(() => valueFromSchedule(schedule));
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string>();

  // The phrase is an authoring aid: it compiles INTO value.cron/oneShotAt,
  // which stay the wire truth. Its text and preview are local; a successful
  // compile bumps phraseVersion, which remounts CronFields so its Custom
  // pick re-derives from the new string instead of sticking.
  const [phrase, setPhrase] = useState("");
  const [phraseOutcome, setPhraseOutcome] = useState<PhraseOutcome>(NO_PHRASE);
  const [phraseVersion, setPhraseVersion] = useState(0);

  // An existing schedule's trigger is read-only: the daemon keeps the stored
  // next run on update, so an edited trigger would never take effect. Every
  // trigger edit goes through withTriggerPatch, which drops it when locked,
  // and the form renders displayedValue, whose trigger is always the stored
  // one for an existing schedule.
  const triggerLocked = !isTriggerEditable(schedule);
  const shown = displayedValue(value, schedule);

  /**
   * Trigger edits made through the structured controls (tabs, cron builder,
   * the one-shot date/time) consume the phrase: left in place, its text
   * would describe a trigger the form no longer holds.
   */
  function changeTrigger(patch: TriggerPatch) {
    if (triggerLocked) return;
    if (phrase) {
      setPhrase("");
      setPhraseOutcome(NO_PHRASE);
    }
    setValue((current) => withTriggerPatch(current, patch, schedule));
  }

  function handlePhrase(text: string) {
    if (triggerLocked) return;
    setPhrase(text);
    const { outcome, patch } = phraseTriggerPatch(text);
    setPhraseOutcome(outcome);
    if (patch) {
      setPhraseVersion((v) => v + 1);
      setValue((current) => withTriggerPatch(current, patch, schedule));
    }
  }

  // `plan` and the modes set outside Studio are offered only to keep a stored
  // writing schedule's mode as it is.
  const keptMode = schedule?.mutating === true ? KEPT_MODE_OPTIONS[schedule.mode] : undefined;
  const phrasePreview = describePhraseOutcome(phraseOutcome);
  const phraseNote = phrasePreview ?? (phrase.trim() ? PHRASE_HINT : null);
  // Formatted from the very trigger that will be sent, never from the inputs.
  const savedOneShot = formatOneShot(triggerFromValue(shown, schedule));

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setSubmitting(true);
    setError(undefined);
    try {
      await onSubmit(bodyFromValue(shown, schedule));
    } catch (caught) {
      setError(errorMessage(caught));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog onOpenChange={(open) => !open && onCancel()} open>
      <DialogContent className="max-h-[90dvh] overflow-y-auto">
        <form onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>{schedule ? "Edit scheduled task" : "Schedule a task"}</DialogTitle>
            <DialogDescription>
              Mecatl validates the cadence and runs the prompt unattended.
            </DialogDescription>
          </DialogHeader>

          <div className="mt-5 space-y-4">
            {/*
             * DECISION: Name stays immutable in #1852. Atomic rename or a
             * mutable displayName over a stable backend name belongs to #2145;
             * create-then-delete is not presented as rename because it cannot
             * preserve state, placement, ownership, and fire history.
             */}
            <Field htmlFor="schedule-name" label="Name">
              <Input
                id="schedule-name"
                disabled={Boolean(schedule)}
                onChange={(event) => setValue({ ...value, name: event.target.value })}
                placeholder="daily-summary"
                required
                value={value.name}
              />
            </Field>
            <Field htmlFor="schedule-prompt" label="Prompt">
              <Textarea
                id="schedule-prompt"
                onChange={(event) => setValue({ ...value, prompt: event.target.value })}
                placeholder="Summarize the latest project activity."
                required
                rows={4}
                value={value.prompt}
              />
            </Field>

            <fieldset aria-describedby={triggerLocked ? "schedule-trigger-locked" : undefined}>
              <legend className="mb-2 text-sm font-medium">Trigger</legend>
              {triggerLocked ? (
                <p className="text-xs text-muted-foreground" id="schedule-trigger-locked">
                  {TRIGGER_LOCKED_NOTE}
                </p>
              ) : (
                <Field htmlFor="schedule-phrase" label="Describe the schedule">
                  <Input
                    aria-describedby={phraseNote ? "schedule-phrase-note" : undefined}
                    autoComplete="off"
                    className="font-normal"
                    id="schedule-phrase"
                    onChange={(event) => handlePhrase(event.target.value)}
                    placeholder={PHRASE_PLACEHOLDER}
                    value={phrase}
                  />
                  {phraseNote && (
                    <p
                      className="text-xs font-normal text-muted-foreground"
                      id="schedule-phrase-note"
                    >
                      {phraseNote}
                    </p>
                  )}
                </Field>
              )}
              <Tabs
                className="mt-3"
                onValueChange={(kind) => changeTrigger({ triggerKind: kind as "cron" | "once" })}
                value={shown.triggerKind}
              >
                <TabsList className="grid w-full grid-cols-2">
                  <TabsTrigger disabled={triggerLocked} value="cron">
                    Recurring
                  </TabsTrigger>
                  <TabsTrigger disabled={triggerLocked} value="once">
                    Run once
                  </TabsTrigger>
                </TabsList>
              </Tabs>
            </fieldset>

            {shown.triggerKind === "cron" ? (
              <CronFields
                cron={shown.cron}
                disabled={triggerLocked}
                key={phraseVersion}
                onChange={(cron) => changeTrigger({ cron })}
              />
            ) : (
              <Field htmlFor="schedule-run-at" label="Run at">
                <Input
                  disabled={triggerLocked}
                  id="schedule-run-at"
                  onChange={(event) => changeTrigger({ oneShotAt: event.target.value })}
                  required
                  type="datetime-local"
                  value={shown.oneShotAt}
                />
                {savedOneShot ? (
                  <p className="mt-1 text-xs text-muted-foreground">Saves as {savedOneShot}</p>
                ) : null}
              </Field>
            )}

            <div className="rounded-lg border p-3">
              <div className="flex items-center gap-2">
                <Switch
                  checked={value.allowWrites}
                  id="schedule-allow-writes"
                  onCheckedChange={(allowWrites) => setValue({ ...value, allowWrites })}
                />
                <Label htmlFor="schedule-allow-writes">Allow this task to make changes</Label>
              </div>
              {value.allowWrites && (
                <SelectField
                  ariaLabel="Write mode"
                  className="mt-3"
                  onValueChange={(writeMode) =>
                    setValue({ ...value, writeMode: writeMode as FormValue["writeMode"] })
                  }
                  options={[
                    { label: "Accept edits automatically", value: "acceptEdits" },
                    { label: "Ask when approval is needed", value: "default" },
                    ...(keptMode && schedule ? [{ label: keptMode, value: schedule.mode }] : []),
                  ]}
                  value={value.writeMode}
                />
              )}
            </div>

            <Field htmlFor="schedule-profile" label="Tool profile">
              <SelectField
                disabled={Boolean(schedule)}
                id="schedule-profile"
                onValueChange={(profile) =>
                  setValue({ ...value, profile: profile as FormValue["profile"] })
                }
                options={[
                  { label: "All", value: "all" },
                  { label: "No filesystem", value: "noFilesystem" },
                ]}
                value={value.profile}
              />
              <p className="text-xs font-normal text-muted-foreground">
                {schedule
                  ? "The backend placement fixes the tool profile after creation."
                  : value.profile === "noFilesystem"
                    ? "No file or shell tools; other tools stay available."
                    : "The agent can use every tool, including file and shell access."}
              </p>
            </Field>
          </div>

          {error && (
            <Alert className="mt-4" variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter className="mt-5">
            <Button onClick={onCancel} type="button" variant="outline">
              Cancel
            </Button>
            <Button disabled={submitting} type="submit" variant="action">
              {submitting ? "Saving…" : schedule ? "Save changes" : "Create schedule"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function Field({
  children,
  htmlFor,
  label,
}: {
  children: React.ReactNode;
  htmlFor: string;
  label: string;
}) {
  return (
    <div className="space-y-2">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
    </div>
  );
}

/** A labelled single choice; the visible `Label` (or `ariaLabel`) names the trigger. */
function SelectField({
  ariaLabel,
  className,
  disabled,
  id,
  onValueChange,
  options,
  value,
}: {
  ariaLabel?: string;
  className?: string;
  disabled?: boolean;
  id?: string;
  onValueChange: (value: string) => void;
  options: ReadonlyArray<{ label: string; value: string }>;
  value: string;
}) {
  return (
    <Select disabled={disabled} onValueChange={onValueChange} value={value}>
      <SelectTrigger aria-label={ariaLabel} className={`w-full ${className ?? ""}`} id={id}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {options.map((option) => (
          <SelectItem key={option.value} value={option.value}>
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

const REPEAT_OPTIONS: Array<{ label: string; value: CronRepeat }> = [
  { label: "Daily", value: "daily" },
  { label: "Weekdays", value: "weekdays" },
  { label: "Weekly", value: "weekly" },
  { label: "Monthly", value: "monthly" },
  { label: "Every…", value: "interval" },
  { label: "Custom", value: "custom" },
];

const WEEKDAY_OPTIONS = [
  { label: "Monday", value: 1 },
  { label: "Tuesday", value: 2 },
  { label: "Wednesday", value: 3 },
  { label: "Thursday", value: 4 },
  { label: "Friday", value: 5 },
  { label: "Saturday", value: 6 },
  { label: "Sunday", value: 0 },
];

const MONTHDAY_OPTIONS = Array.from({ length: 28 }, (_, i) => i + 1);

const INTERVAL_UNIT_OPTIONS: Array<{ label: string; value: CronIntervalUnit }> = [
  { label: "Minutes", value: "minutes" },
  { label: "Hours", value: "hours" },
];

/** The daemon-independent step ceiling per unit (a cron field's own range). */
const INTERVAL_MAX: Record<CronIntervalUnit, number> = { hours: 23, minutes: 59 };

/**
 * The structured cron editor: a "Repeat" picker plus the controls that shape
 * implies (weekday / day of month / interval step, and a time for anything
 * but Every…), falling back to the raw cron string under "Custom". Mirrors
 * Studio's cron builder over the same `cron-builder.ts` derivation.
 */
function CronFields({
  cron,
  disabled = false,
  onChange,
}: {
  cron: string;
  disabled?: boolean;
  onChange: (cron: string) => void;
}) {
  const parsed = cronToBuilder(cron);
  const [customPicked, setCustomPicked] = useState(() => parsed.repeat === "custom");
  const repeat: CronRepeat = customPicked ? "custom" : parsed.repeat;
  // The interval step as typed, while the field has focus: a controlled
  // number input snaps back on every keystroke otherwise.
  const [everyDraft, setEveryDraft] = useState<string | null>(null);

  function rebuild(patch: {
    every?: number;
    monthday?: number;
    repeat?: Exclude<CronRepeat, "custom">;
    time?: string;
    unit?: CronIntervalUnit;
    weekday?: number;
  }) {
    const next = { ...parsed, ...patch };
    if (next.repeat === "custom") return;
    onChange(builderToCron({ ...next, repeat: next.repeat }));
  }

  const cronPreview = describeCron(cron.trim());

  return (
    <fieldset className="min-w-0 space-y-3" disabled={disabled}>
      <div className="flex flex-wrap gap-3">
        <div className="min-w-[130px] flex-1 space-y-2">
          <Label htmlFor="schedule-repeat">Repeat</Label>
          <SelectField
            id="schedule-repeat"
            onValueChange={(picked) => {
              const next = picked as CronRepeat;
              if (next === "custom") {
                setCustomPicked(true);
                return;
              }
              setCustomPicked(false);
              rebuild({ repeat: next });
            }}
            options={REPEAT_OPTIONS}
            value={repeat}
          />
        </div>

        {repeat === "weekly" && (
          <div className="min-w-[130px] flex-1 space-y-2">
            <Label htmlFor="schedule-weekday">On</Label>
            <SelectField
              id="schedule-weekday"
              onValueChange={(day) => rebuild({ weekday: Number(day) })}
              options={WEEKDAY_OPTIONS.map((day) => ({
                label: day.label,
                value: String(day.value),
              }))}
              value={String(parsed.weekday)}
            />
          </div>
        )}

        {repeat === "monthly" && (
          <div className="min-w-[130px] flex-1 space-y-2">
            <Label htmlFor="schedule-monthday">On the</Label>
            <SelectField
              id="schedule-monthday"
              onValueChange={(day) => rebuild({ monthday: Number(day) })}
              options={MONTHDAY_OPTIONS.map((day) => ({
                label: ordinal(day),
                value: String(day),
              }))}
              value={String(parsed.monthday)}
            />
          </div>
        )}

        {repeat === "interval" && (
          <>
            <div className="w-[90px] space-y-2">
              <Label htmlFor="schedule-interval-every">Every</Label>
              <Input
                id="schedule-interval-every"
                inputMode="numeric"
                max={INTERVAL_MAX[parsed.unit]}
                min={1}
                onBlur={() => setEveryDraft(null)}
                onChange={(event) => {
                  setEveryDraft(event.target.value);
                  // The builder clamps an out-of-range step; an empty field
                  // commits nothing until a number is typed.
                  if (event.target.value) rebuild({ every: Number(event.target.value) });
                }}
                step={1}
                type="number"
                value={everyDraft ?? String(parsed.every)}
              />
            </div>
            <div className="min-w-[130px] flex-1 space-y-2">
              <Label htmlFor="schedule-interval-unit">Unit</Label>
              <SelectField
                id="schedule-interval-unit"
                onValueChange={(unit) => rebuild({ unit: unit as CronIntervalUnit })}
                options={INTERVAL_UNIT_OPTIONS}
                value={parsed.unit}
              />
            </div>
          </>
        )}

        {repeat !== "custom" && repeat !== "interval" && (
          <div className="w-[148px] space-y-2">
            <Label htmlFor="schedule-time">At</Label>
            <Input
              id="schedule-time"
              onChange={(event) => {
                // Segment editing fires transient empty values; rebuilding on
                // those snaps the field back to the default mid-edit.
                if (event.target.value) rebuild({ time: event.target.value });
              }}
              type="time"
              value={parsed.time}
            />
          </div>
        )}
      </div>

      {repeat === "custom" && (
        <Field htmlFor="schedule-cron" label="Cron expression">
          <Input
            className="font-mono"
            id="schedule-cron"
            onChange={(event) => onChange(event.target.value)}
            placeholder="0 9 * * *"
            required
            value={cron}
          />
        </Field>
      )}

      {/* The builder's own controls already read as plain English; the
          preview only earns its place under a raw Custom expression. */}
      {repeat === "custom" && cronPreview !== cron.trim() && (
        <p className="text-xs font-normal text-muted-foreground">{cronPreview}</p>
      )}
    </fieldset>
  );
}

/**
 * The absolute instant the form will save, formatted with its offset so an
 * ambiguous wall clock is never saved blind.
 */
function formatOneShot(trigger: ScheduleBody["trigger"]): string {
  if (trigger.kind !== "once" || !trigger.at) return "";
  const date = new Date(trigger.at);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString(undefined, { timeZoneName: "short" });
}

function errorMessage(error: unknown) {
  if (typeof error === "object" && error !== null && "detail" in error) return String(error.detail);
  return error instanceof Error ? error.message : "The schedule could not be saved.";
}
