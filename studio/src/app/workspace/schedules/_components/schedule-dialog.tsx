"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import {
  ScheduleFormFields,
  type ScheduleFormValue,
  scheduleFormProblem,
} from "./schedule-form";

/**
 * The one dialog shell the create and edit flows share: form state, the
 * submit gate, and the daemon-refusal rendering. Callers own what a submit
 * means (build the draft, call the hook, toast); a thrown refusal renders
 * verbatim inside the dialog — the daemon's words are the validation.
 */
export function ScheduleDialog({
  open,
  onOpenChange,
  trigger,
  title,
  description,
  submitLabel,
  submittingLabel,
  initialValue,
  noteForValue,
  onSubmit,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Rendered as the DialogTrigger (the create button); edit opens controlled. */
  trigger?: React.ReactNode;
  title: string;
  description?: string;
  submitLabel: string;
  submittingLabel: string;
  initialValue: () => ScheduleFormValue;
  /** Caller-owned advisory line under the form (e.g. the rename warning). */
  noteForValue?: (value: ScheduleFormValue) => string | null;
  onSubmit: (value: ScheduleFormValue) => Promise<void>;
}) {
  const [value, setValue] = useState<ScheduleFormValue>(initialValue);
  const [refusal, setRefusal] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const problem = scheduleFormProblem(value);
  const note = noteForValue?.(value) ?? null;

  function handleOpenChange(next: boolean) {
    onOpenChange(next);
    if (!next) {
      setValue(initialValue());
      setRefusal(null);
    }
  }

  async function handleSubmit(event: React.FormEvent) {
    event.preventDefault();
    if (problem || submitting) return;
    setSubmitting(true);
    setRefusal(null);
    try {
      await onSubmit(value);
      handleOpenChange(false);
    } catch (caught) {
      setRefusal(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      {trigger && <DialogTrigger asChild>{trigger}</DialogTrigger>}
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            {description && (
              <DialogDescription>{description}</DialogDescription>
            )}
          </DialogHeader>

          <div className="space-y-3 py-4">
            <ScheduleFormFields
              value={value}
              onChange={(patch) => setValue((v) => ({ ...v, ...patch }))}
            />
            {note && <p className="text-xs text-muted-foreground">{note}</p>}
          </div>

          {refusal && (
            <p className="mb-3 whitespace-pre-wrap rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
              {refusal}
            </p>
          )}

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              className="rounded-full"
              onClick={() => handleOpenChange(false)}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              variant="action"
              className="rounded-full"
              disabled={problem !== null || submitting}
            >
              {submitting ? submittingLabel : submitLabel}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
