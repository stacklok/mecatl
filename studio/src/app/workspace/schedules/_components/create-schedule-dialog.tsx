"use client";

import { Plus } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import type { ScheduleSpecDraft } from "@/lib/protocol";
import { ScheduleDialog } from "./schedule-dialog";
import { draftFromForm, emptyScheduleForm } from "./schedule-form";

/**
 * Authors a full ScheduleSpecDraft and hands it to the page's own hook
 * instance, so the new row lands in the table the user is looking at.
 */
export function CreateScheduleDialog({
  createFromDraft,
}: {
  createFromDraft: (draft: ScheduleSpecDraft) => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  return (
    <ScheduleDialog
      open={open}
      onOpenChange={setOpen}
      trigger={
        <Button size="sm" variant="action" className="rounded-full">
          <Plus className="size-4" />
          <span className="max-[499px]:hidden">New scheduled task</span>
          <span className="min-[500px]:hidden">New</span>
        </Button>
      }
      title="New scheduled task"
      submitLabel="Create task"
      submittingLabel="Creating…"
      initialValue={emptyScheduleForm}
      onSubmit={async (value) => {
        await createFromDraft(draftFromForm(value));
        toast.success(`Scheduled task "${value.name.trim()}" created`);
      }}
    />
  );
}
