// SPDX-License-Identifier: Apache-2.0

import { useState } from "react";
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
import { Textarea } from "../../components/ui/textarea";
import {
  builderToCron,
  type CronIntervalUnit,
  type CronRepeat,
  cronToBuilder,
  describeCron,
  ordinal,
} from "./cron-builder";
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

const PHRASE_PLACEHOLDER = "every 30 minutes · daily at 9am · next monday 3pm · in 2 hours";
const PHRASE_HINT = 'Not recognised — try "every weekday at 9am" or a cron expression';

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

  // `plan` is offered only to keep a stored writing schedule's mode as it is.
  const offerPlanMode = schedule?.mutating === true && schedule.mode === "plan";
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
            <Field label="Name">
              <Input
                disabled={Boolean(schedule)}
                onChange={(event) => setValue({ ...value, name: event.target.value })}
                placeholder="daily-summary"
                required
                value={value.name}
              />
            </Field>
            <Field label="Prompt">
              <Textarea
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
                <Field label="Describe the schedule">
                  <Input
                    aria-describedby={phraseNote ? "schedule-phrase-note" : undefined}
                    autoComplete="off"
                    className="font-normal"
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
              <div className="mt-3 grid grid-cols-2 rounded-lg bg-muted p-1">
                {(["cron", "once"] as const).map((kind) => (
                  <button
                    className={`h-8 rounded-md text-sm disabled:cursor-not-allowed ${shown.triggerKind === kind ? "bg-background font-medium shadow-sm" : "text-muted-foreground"} ${triggerLocked && shown.triggerKind !== kind ? "opacity-50" : ""}`}
                    disabled={triggerLocked}
                    key={kind}
                    onClick={() => changeTrigger({ triggerKind: kind })}
                    type="button"
                  >
                    {kind === "cron" ? "Recurring" : "Run once"}
                  </button>
                ))}
              </div>
            </fieldset>

            {shown.triggerKind === "cron" ? (
              <div className="space-y-4">
                <CronFields
                  cron={shown.cron}
                  disabled={triggerLocked}
                  key={phraseVersion}
                  onChange={(cron) => changeTrigger({ cron })}
                />
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field label="Timezone">
                    <Input
                      disabled={triggerLocked}
                      onChange={(event) => {
                        const timezone = event.target.value;
                        setValue((current) => withTriggerPatch(current, { timezone }, schedule));
                      }}
                      placeholder="UTC"
                      value={shown.timezone}
                    />
                    <p className="text-xs font-normal text-muted-foreground">
                      {shown.timezone.trim()
                        ? "An IANA time zone, such as Europe/Rome."
                        : "Empty runs the schedule in UTC."}
                    </p>
                  </Field>
                  <Field label="Maximum runs (0 = unlimited)">
                    <Input
                      min="0"
                      onChange={(event) => setValue({ ...value, maxFires: event.target.value })}
                      type="number"
                      value={value.maxFires}
                    />
                  </Field>
                </div>
              </div>
            ) : (
              <div className="space-y-3">
                <Field label="Run at">
                  <Input
                    disabled={triggerLocked}
                    onChange={(event) => changeTrigger({ oneShotAt: event.target.value })}
                    required
                    type="datetime-local"
                    value={shown.oneShotAt}
                  />
                  {savedOneShot ? (
                    <p className="mt-1 text-xs text-muted-foreground">Saves as {savedOneShot}</p>
                  ) : null}
                </Field>
                <label className="flex items-center gap-2 text-sm">
                  <input
                    checked={value.oneShotRetry}
                    onChange={(event) => setValue({ ...value, oneShotRetry: event.target.checked })}
                    type="checkbox"
                  />
                  Retry if the run fails
                </label>
                {value.oneShotRetry && (
                  <Field label="Maximum retries">
                    <Input
                      min="0"
                      onChange={(event) =>
                        setValue({ ...value, oneShotMaxRetries: event.target.value })
                      }
                      type="number"
                      value={value.oneShotMaxRetries}
                    />
                  </Field>
                )}
              </div>
            )}

            <div className="rounded-lg border p-3">
              <label className="flex items-center gap-2 text-sm font-medium">
                <input
                  checked={value.allowWrites}
                  onChange={(event) => setValue({ ...value, allowWrites: event.target.checked })}
                  type="checkbox"
                />
                Allow this task to make changes
              </label>
              {value.allowWrites && (
                <select
                  className="mt-3 h-9 w-full rounded-md border bg-background px-3 text-sm"
                  onChange={(event) =>
                    setValue({ ...value, writeMode: event.target.value as FormValue["writeMode"] })
                  }
                  value={value.writeMode}
                >
                  <option value="acceptEdits">Accept edits automatically</option>
                  <option value="default">Ask when approval is needed</option>
                  {offerPlanMode && <option value="plan">Plan only, changes are refused</option>}
                </select>
              )}
            </div>

            <Field label="Tool profile">
              <select
                className="h-9 w-full rounded-md border bg-background px-3 text-sm"
                onChange={(event) =>
                  setValue({ ...value, profile: event.target.value as FormValue["profile"] })
                }
                value={value.profile}
              >
                <option value="all">All</option>
                <option value="noFilesystem">No filesystem</option>
              </select>
              <p className="text-xs font-normal text-muted-foreground">
                {value.profile === "noFilesystem"
                  ? "No file or shell tools; other tools stay available."
                  : "The agent can use every tool, including file and shell access."}
              </p>
            </Field>
          </div>

          {error && (
            <p className="mt-4 rounded-lg bg-destructive/10 p-3 text-sm text-destructive">
              {error}
            </p>
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

function Field({ children, label }: { children: React.ReactNode; label: string }) {
  return (
    <fieldset className="block space-y-2 text-sm font-medium">
      <legend>{label}</legend>
      {children}
    </fieldset>
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

const selectClass = "h-9 w-full rounded-md border bg-background px-3 text-sm";

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
        <div className="min-w-[130px] flex-1 space-y-2 text-sm font-medium">
          <label htmlFor="schedule-repeat">Repeat</label>
          <select
            className={selectClass}
            id="schedule-repeat"
            onChange={(event) => {
              const next = event.target.value as CronRepeat;
              if (next === "custom") {
                setCustomPicked(true);
                return;
              }
              setCustomPicked(false);
              rebuild({ repeat: next });
            }}
            value={repeat}
          >
            {REPEAT_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </div>

        {repeat === "weekly" && (
          <div className="min-w-[130px] flex-1 space-y-2 text-sm font-medium">
            <label htmlFor="schedule-weekday">On</label>
            <select
              className={selectClass}
              id="schedule-weekday"
              onChange={(event) => rebuild({ weekday: Number(event.target.value) })}
              value={parsed.weekday}
            >
              {WEEKDAY_OPTIONS.map((day) => (
                <option key={day.value} value={day.value}>
                  {day.label}
                </option>
              ))}
            </select>
          </div>
        )}

        {repeat === "monthly" && (
          <div className="min-w-[130px] flex-1 space-y-2 text-sm font-medium">
            <label htmlFor="schedule-monthday">On the</label>
            <select
              className={selectClass}
              id="schedule-monthday"
              onChange={(event) => rebuild({ monthday: Number(event.target.value) })}
              value={parsed.monthday}
            >
              {MONTHDAY_OPTIONS.map((day) => (
                <option key={day} value={day}>
                  {ordinal(day)}
                </option>
              ))}
            </select>
          </div>
        )}

        {repeat === "interval" && (
          <>
            <div className="w-[90px] space-y-2 text-sm font-medium">
              <label htmlFor="schedule-interval-every">Every</label>
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
            <div className="min-w-[130px] flex-1 space-y-2 text-sm font-medium">
              <label htmlFor="schedule-interval-unit">Unit</label>
              <select
                className={selectClass}
                id="schedule-interval-unit"
                onChange={(event) => rebuild({ unit: event.target.value as CronIntervalUnit })}
                value={parsed.unit}
              >
                {INTERVAL_UNIT_OPTIONS.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
            </div>
          </>
        )}

        {repeat !== "custom" && repeat !== "interval" && (
          <div className="w-[120px] space-y-2 text-sm font-medium">
            <label htmlFor="schedule-time">At</label>
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
        <Field label="Cron expression">
          <Input
            className="font-mono"
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
