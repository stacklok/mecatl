"use client";

import { useState } from "react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { useRuntimeSettings } from "@/features/agent/hooks/use-runtime-settings";
import type {
  HarnessLearningMode,
  HarnessLearningSensitivity,
  HarnessRuntimeSettingsDoc,
} from "@/lib/harness/runtime-settings";
import { OptionField, type OptionItem } from "./option-field";
import {
  ExternalManagedNote,
  Note,
  OfflineNote,
  RESTART_SENTENCE,
  SettingsCard,
  SettingsRow,
} from "./settings-card";

/**
 * Settings → Learning → Learning: two plain choices (mode and sensitivity)
 * over the agent's closed vocabularies, a summary of the pending change, and
 * one "Save and restart" that writes both values through the controller and
 * restarts the agent.
 *
 * The values shown as CURRENT are the controller's `effective` fold — what
 * the agent actually runs with (Studio's override, else the operator's
 * settings file, else the default) — never just Studio's own file.
 *
 * Managed mode only. An imported operator settings file owns learning
 * (`managedBy.learning === "operator-settings"`): the values render
 * read-only. External mode renders the managed note; offline the offline
 * note.
 */

type LearningMode = Exclude<HarnessLearningMode, "">;
type LearningSensitivity = Exclude<HarnessLearningSensitivity, "">;

interface LearningDraft {
  mode: LearningMode;
  sensitivity: LearningSensitivity;
}

const MODE_OPTIONS: readonly (OptionItem & { value: LearningMode })[] = [
  {
    value: "off",
    label: "Off",
    description: "The agent does not learn from chats.",
  },
  {
    value: "review",
    label: "Review",
    description: "The agent suggests things to remember; you approve each one.",
  },
  {
    value: "auto",
    label: "Auto",
    description:
      "The agent suggests things and remembers clear-cut facts on its own.",
  },
];

const SENSITIVITY_OPTIONS: readonly (OptionItem & {
  value: LearningSensitivity;
})[] = [
  {
    value: "conservative",
    label: "Conservative",
    description: "Fewer suggestions, only when the evidence is strong.",
  },
  {
    value: "balanced",
    label: "Balanced",
    description: "The standard amount of suggestions.",
  },
  {
    value: "eager",
    label: "Eager",
    description: "More suggestions, with less evidence needed.",
  },
];

const isMode = (value: string): value is LearningMode =>
  MODE_OPTIONS.some((option) => option.value === value);

const isSensitivity = (value: string): value is LearningSensitivity =>
  SENSITIVITY_OPTIONS.some((option) => option.value === value);

/** The agent's defaults stand in for a value the controller could not
 *  report (an older controller, an unparseable settings file). */
const asLearningMode = (value: string): LearningMode =>
  isMode(value) ? value : "off";

const asLearningSensitivity = (value: string): LearningSensitivity =>
  isSensitivity(value) ? value : "balanced";

const learningModeLabel = (value: string): string =>
  MODE_OPTIONS.find((option) => option.value === value)?.label ??
  learningModeLabel(asLearningMode(value));

const learningSensitivityLabel = (value: string): string =>
  SENSITIVITY_OPTIONS.find((option) => option.value === value)?.label ??
  learningSensitivityLabel(asLearningSensitivity(value));

/**
 * The pending change in plain words, naming only what changed:
 * `Learning mode: Off → Review · Sensitivity: Balanced → Eager`.
 * Empty when nothing changed.
 */
export function learningChangeReport(
  from: LearningDraft,
  to: LearningDraft,
): string {
  const parts: string[] = [];
  if (from.mode !== to.mode) {
    parts.push(
      `Learning mode: ${learningModeLabel(from.mode)} → ${learningModeLabel(to.mode)}`,
    );
  }
  if (from.sensitivity !== to.sensitivity) {
    parts.push(
      `Sensitivity: ${learningSensitivityLabel(from.sensitivity)} → ${learningSensitivityLabel(to.sensitivity)}`,
    );
  }
  return parts.join(" · ");
}

/** The effective pair the agent runs with, as the two choices show it. */
export function currentLearning(doc: HarnessRuntimeSettingsDoc): LearningDraft {
  return {
    mode: asLearningMode(doc.effective.learning.mode),
    sensitivity: asLearningSensitivity(doc.effective.learning.sensitivity),
  };
}

const MODE_DESCRIPTION = "What the agent does with a finished chat.";
const SENSITIVITY_DESCRIPTION =
  "How sure the agent must be before it suggests something.";

export function LearningModeSection() {
  const { live, manageable, doc, isLoading, busy, error, notice, save } =
    useRuntimeSettings();
  // The unsaved pair; null = showing the effective values untouched.
  const [draft, setDraft] = useState<LearningDraft | null>(null);
  const [confirming, setConfirming] = useState(false);

  let body: React.ReactNode;
  if (!live) {
    body = <OfflineNote />;
  } else if (!manageable) {
    body = <ExternalManagedNote />;
  } else if (!doc) {
    body = (
      <Note>
        {isLoading
          ? "Loading…"
          : (error ?? "Learning settings could not be loaded right now.")}
      </Note>
    );
  } else if (doc.managedBy.learning === "operator-settings") {
    const current = currentLearning(doc);
    body = (
      <>
        <div className="divide-y divide-border/60">
          <SettingsRow label="Learning mode" description={MODE_DESCRIPTION}>
            <span className="text-sm text-muted-foreground">
              {learningModeLabel(current.mode)}
            </span>
          </SettingsRow>
          <SettingsRow
            label="Sensitivity"
            description={SENSITIVITY_DESCRIPTION}
          >
            <span className="text-sm text-muted-foreground">
              {learningSensitivityLabel(current.sensitivity)}
            </span>
          </SettingsRow>
        </div>
        <div className="mt-3">
          <Note>
            Learning is set where the agent runs and can&rsquo;t be changed
            here.
          </Note>
        </div>
      </>
    );
  } else {
    const current = currentLearning(doc);
    const shown = draft ?? current;
    const dirty =
      shown.mode !== current.mode || shown.sensitivity !== current.sensitivity;
    const report = learningChangeReport(current, shown);
    const submit = async () => {
      setConfirming(false);
      const ok = await save({
        learning: { mode: shown.mode, sensitivity: shown.sensitivity },
      });
      if (ok) setDraft(null);
    };

    body = (
      <>
        <div className="divide-y divide-border/60">
          <SettingsRow label="Learning mode" description={MODE_DESCRIPTION}>
            <OptionField
              label="Learning mode"
              value={shown.mode}
              options={MODE_OPTIONS}
              onChange={(value) =>
                setDraft({ ...shown, mode: asLearningMode(value) })
              }
            />
          </SettingsRow>
          <SettingsRow
            label="Sensitivity"
            description={SENSITIVITY_DESCRIPTION}
          >
            <OptionField
              label="Sensitivity"
              value={shown.sensitivity}
              options={SENSITIVITY_OPTIONS}
              onChange={(value) =>
                setDraft({
                  ...shown,
                  sensitivity: asLearningSensitivity(value),
                })
              }
            />
          </SettingsRow>
        </div>
        {dirty ? (
          <div className="mt-3 space-y-1" data-testid="learning-pending">
            <p className="text-sm">{report}</p>
            <p className="text-xs text-muted-foreground">{RESTART_SENTENCE}</p>
          </div>
        ) : null}
        <div className="mt-4 flex flex-wrap items-center gap-2">
          <Button
            type="button"
            size="sm"
            disabled={!dirty || busy !== ""}
            onClick={() => setConfirming(true)}
          >
            Save and restart
          </Button>
          {dirty ? (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              disabled={busy !== ""}
              onClick={() => setDraft(null)}
            >
              Discard
            </Button>
          ) : null}
        </div>
        {error ? (
          <p role="alert" className="mt-3 text-sm text-destructive">
            {error}
          </p>
        ) : null}
        {notice ? (
          <p role="status" className="mt-3 text-sm text-muted-foreground">
            {notice}
          </p>
        ) : null}
        <AlertDialog open={confirming} onOpenChange={setConfirming}>
          {confirming && (
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>Save and restart the agent?</AlertDialogTitle>
                <AlertDialogDescription>
                  {report}. {RESTART_SENTENCE}
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>Cancel</AlertDialogCancel>
                <AlertDialogAction onClick={() => void submit()}>
                  Save and restart
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          )}
        </AlertDialog>
      </>
    );
  }

  return (
    <SettingsCard
      title="Learning"
      description="Let the agent remember useful things from finished chats."
    >
      {body}
    </SettingsCard>
  );
}
