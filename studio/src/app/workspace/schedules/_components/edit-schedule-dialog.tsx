"use client";

import { useState } from "react";
import { toast } from "sonner";
import {
  type ScheduleCarriedSpec,
  type ScheduleRow,
  type ScheduleSpecDraft,
  scheduleDraftFromRow,
} from "@/lib/protocol";
import { ScheduleDialog } from "./schedule-dialog";
import { draftFromForm, formFromDraft } from "./schedule-form";

/**
 * Edits an existing schedule. The form is seeded from the stored spec and the
 * save carries the row's `carried` fields verbatim: PUT replaces the whole
 * spec, so anything not re-sent (a CLI-set model selector, misfire policy,
 * fire timeout) would be silently deleted.
 *
 * A changed name cannot ride the PUT (the daemon force-stamps the path name
 * onto the body — there is no rename on the wire), so a new-name save goes
 * through the hook's re-create + delete path instead, with a note in the
 * dialog that run history stays behind.
 */
export function EditScheduleDialog({
  row,
  updateFromDraft,
  renameAndUpdateFromDraft,
  onRenamed,
  onClose,
}: {
  row: ScheduleRow;
  updateFromDraft: (
    draft: ScheduleSpecDraft,
    carried: ScheduleCarriedSpec,
  ) => Promise<void>;
  renameAndUpdateFromDraft: (
    draft: ScheduleSpecDraft,
    previous: ScheduleRow,
  ) => Promise<void>;
  /** The old detail route 404s after a rename; the caller navigates. */
  onRenamed: (name: string) => void;
  onClose: () => void;
}) {
  // The stored draft is captured once on mount: it seeds the form and supplies
  // the fields the form has no controls for (limits).
  const [storedDraft] = useState(() => scheduleDraftFromRow(row));
  return (
    <ScheduleDialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      title="Edit Schedule"
      submitLabel="Save changes"
      submittingLabel="Saving…"
      initialValue={() => formFromDraft(storedDraft)}
      noteForValue={(value) =>
        value.name.trim() && value.name.trim() !== row.name
          ? "Renaming re-creates the schedule under the new name — run history does not carry over."
          : null
      }
      onSubmit={async (value) => {
        const draft = draftFromForm(value, storedDraft);
        if (draft.name === row.name) {
          await updateFromDraft(draft, row.carried);
          toast.success(`Scheduled task "${row.name}" updated`);
          return;
        }
        await renameAndUpdateFromDraft(draft, row);
        toast.success(`Scheduled task renamed to "${draft.name}"`);
        onRenamed(draft.name);
      }}
    />
  );
}
