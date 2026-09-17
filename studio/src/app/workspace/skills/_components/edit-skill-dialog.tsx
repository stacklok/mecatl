"use client";

import { Loader2 } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";

/**
 * The SKILL.md editor both the list kebab and the detail page open: seeded
 * from the controller's body read, saved back with a PUT. Saving an ENABLED
 * skill restarts the daemon (its skills snapshot is resolved once at
 * startup), so the footer warns with the same wording the gateway and
 * model-router writes use; a refused save renders verbatim inside the dialog
 * — the controller's words are the validation.
 */
export function EditSkillDialog({
  name,
  enabled,
  fetchBody,
  saveBody,
  onClose,
}: {
  name: string;
  /** Whether the daemon currently sees this skill — an enabled save restarts it. */
  enabled: boolean;
  fetchBody: (name: string, signal?: AbortSignal) => Promise<string>;
  saveBody: (name: string, body: string) => Promise<void>;
  onClose: () => void;
}) {
  const [body, setBody] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [refusal, setRefusal] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    fetchBody(name, controller.signal)
      .then((content) => {
        if (!controller.signal.aborted) setBody(content);
      })
      .catch((caught) => {
        if (controller.signal.aborted) return;
        setLoadError(caught instanceof Error ? caught.message : String(caught));
      });
    return () => controller.abort();
  }, [name, fetchBody]);

  async function handleSave() {
    if (body === null || saving) return;
    setSaving(true);
    setRefusal(null);
    try {
      await saveBody(name, body);
      onClose();
    } catch (caught) {
      setRefusal(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="flex max-h-[90vh] flex-col max-[499px]:top-0 max-[499px]:left-0 max-[499px]:h-dvh max-[499px]:max-h-none max-[499px]:w-screen max-[499px]:max-w-none max-[499px]:translate-x-0 max-[499px]:translate-y-0 max-[499px]:rounded-none max-[499px]:border-0 sm:max-w-3xl">
        <DialogHeader className="text-left">
          <DialogTitle>Edit skill</DialogTitle>
          {!enabled && (
            <DialogDescription>
              This skill is disabled — changes take effect when it is enabled.
            </DialogDescription>
          )}
        </DialogHeader>

        {loadError ? (
          <p className="whitespace-pre-wrap rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
            {loadError}
          </p>
        ) : body === null ? (
          <div className="flex items-center gap-2 rounded-lg border border-dashed px-4 py-10 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
            Reading SKILL.md…
          </div>
        ) : (
          <Textarea
            value={body}
            onChange={(event) => setBody(event.target.value)}
            spellCheck={false}
            aria-label={`SKILL.md content for ${name}`}
            className="min-h-[45vh] flex-1 resize-none overflow-y-auto font-mono text-xs leading-relaxed"
          />
        )}

        {refusal && (
          <p className="whitespace-pre-wrap rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
            {refusal}
          </p>
        )}

        <DialogFooter className="flex-row justify-end gap-2">
          <Button
            type="button"
            variant="outline"
            className="rounded-full"
            onClick={onClose}
          >
            Cancel
          </Button>
          <Button
            type="button"
            variant="action"
            className="rounded-full"
            disabled={body === null || saving}
            onClick={() => void handleSave()}
          >
            {saving ? "Saving…" : "Save"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
