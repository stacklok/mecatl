"use client";

import { unzipSync } from "fflate";
import { PenLine, Plus, Upload } from "lucide-react";
import { useCallback, useId, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { validSkillName } from "@/lib/controller-security.mjs";
import type { HarnessSkillUploadFile } from "@/lib/harness/client";
import { cn } from "@/lib/utils";
import { deriveSkillName } from "./skill-name";
import {
  bytesToBase64,
  maxSkillUploadFileBytes,
  maxSkillUploadTotalBytes,
  planSkillUpload,
} from "./skill-upload";

/** The daemon's activation-name grammar, as helper text under an invalid name. */
const nameRule =
  "Lowercase letters, digits, hyphens, and underscores — max 64 characters, starting with a letter or digit.";

/** A just-enough SKILL.md: the frontmatter the inventory reads (name +
 *  description) and a stub for the instructions the model loads on demand. */
const skillTemplate = `---
name: my-skill
description: One line telling the agent when to reach for this skill.
---

# Instructions

Describe, step by step, what the agent should do when it loads this skill.
`;

/** One selectable card on the chooser step — the Create-project idiom:
 *  icon top-left, radio dot top-right, title + description below. */
function ChoiceCard({
  icon: Icon,
  title,
  description,
  selected,
  onSelect,
}: {
  icon: typeof Upload;
  title: string;
  description: string;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={selected}
      onClick={onSelect}
      className={cn(
        "flex flex-col gap-6 rounded-xl border p-4 text-left transition-colors",
        selected
          ? "border-primary ring-1 ring-primary"
          : "hover:border-muted-foreground/40",
      )}
    >
      <span className="flex items-start justify-between">
        <Icon className="size-5 text-muted-foreground" />
        <span
          aria-hidden="true"
          className={cn(
            "flex size-4 items-center justify-center rounded-full border",
            selected ? "border-primary" : "border-muted-foreground/50",
          )}
        >
          {selected && <span className="size-2 rounded-full bg-primary" />}
        </span>
      </span>
      <span className="space-y-1">
        <span className="block text-sm font-medium">{title}</span>
        <span className="block text-sm text-muted-foreground">
          {description}
        </span>
      </span>
    </button>
  );
}

/** The decoded shape both upload sources (zip entries, folder picks)
 *  normalize into before size checks and the create POST. */
interface UploadFile {
  path: string;
  bytes: Uint8Array;
}

/** Enforces the byte caps client-side so a doomed upload fails with a clear
 *  message before the POST; returns the refusal or null. */
function uploadSizeProblem(files: UploadFile[]): string | null {
  let total = 0;
  for (const file of files) {
    if (file.bytes.length > maxSkillUploadFileBytes) {
      return `"${file.path}" is larger than the ${Math.floor(maxSkillUploadFileBytes / (1024 * 1024))} MB per-file limit.`;
    }
    total += file.bytes.length;
  }
  if (total > maxSkillUploadTotalBytes) {
    return `The upload is larger than the ${Math.floor(maxSkillUploadTotalBytes / (1024 * 1024))} MB total limit.`;
  }
  return null;
}

/**
 * Authors a brand-new skill from a chooser: upload (a SKILL.md, a .zip, or a
 * whole folder — created immediately, no editor step) or create manually (the
 * name + SKILL.md editor). Cancelling the upload picker closes the dialog —
 * back to the list, never a form the user didn't ask for. Uploads land as
 * folder skills via the controller's multi-file create; a refusal (client
 * plan or controller) renders verbatim on the chooser.
 */
export function CreateSkillDialog({
  create,
  createFiles,
  onCreated,
}: {
  create: (name: string, body: string) => Promise<void>;
  createFiles: (name: string, files: HarnessSkillUploadFile[]) => Promise<void>;
  /** Called after a successful create — the page navigates to the new skill. */
  onCreated: (name: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [step, setStep] = useState<"choose" | "edit">("choose");
  const [mode, setMode] = useState<"upload" | "manual">("upload");
  const [name, setName] = useState("");
  const [body, setBody] = useState(skillTemplate);
  const [refusal, setRefusal] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  const nameFieldId = useId();

  const nameValid = validSkillName(name);
  const canSubmit = nameValid && body.trim().length > 0 && !creating;

  function handleOpenChange(next: boolean) {
    setOpen(next);
    if (next) {
      // Fresh form every open — a cancelled draft never haunts the next one.
      setStep("choose");
      setMode("upload");
      setName("");
      setBody(skillTemplate);
      setRefusal(null);
    }
  }

  // Cancelling the picker means "never mind": drop straight back to the
  // skills list instead of landing on a form the user didn't ask for. Wired
  // as ref callbacks (not an effect) because Radix portals the dialog
  // content in after the open-flip commit — an [open]-keyed effect would
  // attach to null refs and never re-run. State is read through refs so the
  // stable listener sees the live step.
  const stepRef = useRef(step);
  stepRef.current = step;
  const creatingRef = useRef(creating);
  creatingRef.current = creating;
  const attachPickerCancel = useCallback(
    (holder: React.RefObject<HTMLInputElement | null>) =>
      (node: HTMLInputElement | null) => {
        holder.current = node;
        if (!node) return;
        const onCancel = () => {
          if (stepRef.current === "choose" && !creatingRef.current)
            setOpen(false);
        };
        node.addEventListener("cancel", onCancel);
        return () => {
          node.removeEventListener("cancel", onCancel);
          holder.current = null;
        };
      },
    [],
  );

  /** The shared tail of every upload source: derive/validate the name, then
   *  create immediately and navigate. Refusals land on the chooser. */
  async function createFromUpload(sourceName: string, files: UploadFile[]) {
    const sizeProblem = uploadSizeProblem(files);
    if (sizeProblem) {
      setRefusal(sizeProblem);
      return;
    }
    const skillMd = files.find((file) => file.path === "SKILL.md");
    const content = skillMd ? new TextDecoder().decode(skillMd.bytes) : "";
    const derived = deriveSkillName(sourceName, content);
    if (!derived) {
      setRefusal(`Couldn't derive a skill name from the upload. ${nameRule}`);
      return;
    }
    setCreating(true);
    setRefusal(null);
    try {
      if (files.length === 1 && files[0].path === "SKILL.md") {
        await create(derived, content);
      } else {
        await createFiles(
          derived,
          files.map((file) => ({
            path: file.path,
            contentBase64: bytesToBase64(file.bytes),
          })),
        );
      }
      setOpen(false);
      onCreated(derived);
    } catch (caught) {
      setRefusal(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setCreating(false);
    }
  }

  async function handleFile(event: React.ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    // Same file re-chosen later must fire change again.
    event.target.value = "";
    if (!file) return;
    const lower = file.name.toLowerCase();
    try {
      if (lower.endsWith(".zip")) {
        const entries = unzipSync(new Uint8Array(await file.arrayBuffer()));
        const paths = Object.keys(entries);
        const plan = planSkillUpload(paths);
        if (plan.error) {
          setRefusal(plan.error);
          return;
        }
        await createFromUpload(
          file.name,
          plan.files.map((planned) => ({
            path: planned.path,
            bytes: entries[paths[planned.index]],
          })),
        );
      } else if (lower.endsWith(".md")) {
        await createFromUpload(file.name, [
          {
            path: "SKILL.md",
            bytes: new Uint8Array(await file.arrayBuffer()),
          },
        ]);
      } else {
        setRefusal("Upload a SKILL.md or a .zip of the skill folder.");
      }
    } catch {
      setRefusal(`Couldn't read "${file.name}" as a skill upload.`);
    }
  }

  async function handleFolder(event: React.ChangeEvent<HTMLInputElement>) {
    const picked = Array.from(event.target.files ?? []);
    event.target.value = "";
    if (picked.length === 0) return;
    // webkitRelativePath starts with the chosen folder's own name; the
    // planner strips that shared wrapper, and it also names the skill.
    const paths = picked.map((file) => file.webkitRelativePath || file.name);
    const folderName = paths[0]?.split("/")[0] ?? "";
    const plan = planSkillUpload(paths);
    if (plan.error) {
      setRefusal(plan.error);
      return;
    }
    try {
      const files = await Promise.all(
        plan.files.map(async (planned) => ({
          path: planned.path,
          bytes: new Uint8Array(await picked[planned.index].arrayBuffer()),
        })),
      );
      await createFromUpload(folderName, files);
    } catch {
      setRefusal(`Couldn't read the folder "${folderName}".`);
    }
  }

  async function handleCreate() {
    if (!canSubmit) return;
    setCreating(true);
    setRefusal(null);
    try {
      await create(name, body);
      setOpen(false);
      onCreated(name);
    } catch (caught) {
      setRefusal(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setCreating(false);
    }
  }

  return (
    <>
      <Button
        size="sm"
        variant="action"
        className="rounded-full"
        onClick={() => handleOpenChange(true)}
      >
        <Plus className="size-4" />
        <span className="max-[499px]:hidden">New skill</span>
        <span className="min-[500px]:hidden">New</span>
      </Button>

      <Dialog open={open} onOpenChange={handleOpenChange}>
        <DialogContent
          className={cn(
            "flex max-h-[90vh] flex-col max-[499px]:top-0 max-[499px]:left-0 max-[499px]:h-dvh max-[499px]:max-h-none max-[499px]:w-screen max-[499px]:max-w-none max-[499px]:translate-x-0 max-[499px]:translate-y-0 max-[499px]:rounded-none max-[499px]:border-0",
            step === "choose" ? "sm:max-w-lg" : "sm:max-w-3xl",
          )}
        >
          <DialogHeader className="text-left">
            <DialogTitle>New skill</DialogTitle>
          </DialogHeader>

          <input
            ref={attachPickerCancel(fileInput)}
            type="file"
            accept=".md,.zip,text/markdown,application/zip"
            className="sr-only"
            aria-label="Upload a SKILL.md or zip file"
            onChange={(event) => void handleFile(event)}
          />
          <input
            ref={attachPickerCancel(folderInput)}
            type="file"
            {...{ webkitdirectory: "" }}
            className="sr-only"
            aria-label="Upload a skill folder"
            onChange={(event) => void handleFolder(event)}
          />

          {step === "choose" ? (
            <>
              <div className="grid grid-cols-2 gap-3 max-[499px]:grid-cols-1">
                <ChoiceCard
                  icon={Upload}
                  title="Upload"
                  description="Import a SKILL.md, a .zip, or a whole skill folder."
                  selected={mode === "upload"}
                  onSelect={() => setMode("upload")}
                />
                <ChoiceCard
                  icon={PenLine}
                  title="Create manually"
                  description="Write the skill from scratch."
                  selected={mode === "manual"}
                  onSelect={() => setMode("manual")}
                />
              </div>

              {refusal && (
                <p className="whitespace-pre-wrap rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
                  {refusal}
                </p>
              )}
              {creating && (
                <p className="text-sm text-muted-foreground">
                  Creating the skill…
                </p>
              )}

              <DialogFooter className="flex-row justify-end gap-2">
                <Button
                  type="button"
                  variant="outline"
                  className="rounded-full"
                  disabled={creating}
                  onClick={() => handleOpenChange(false)}
                >
                  Cancel
                </Button>
                {mode === "upload" ? (
                  <>
                    <Button
                      type="button"
                      variant="outline"
                      className="rounded-full"
                      disabled={creating}
                      onClick={() => folderInput.current?.click()}
                    >
                      Upload folder
                    </Button>
                    <Button
                      type="button"
                      variant="action"
                      className="rounded-full"
                      disabled={creating}
                      onClick={() => fileInput.current?.click()}
                    >
                      Upload file
                    </Button>
                  </>
                ) : (
                  <Button
                    type="button"
                    variant="action"
                    className="rounded-full"
                    onClick={() => setStep("edit")}
                  >
                    Next
                  </Button>
                )}
              </DialogFooter>
            </>
          ) : (
            <>
              <div className="space-y-3">
                <Label htmlFor={nameFieldId}>Name</Label>
                <Input
                  id={nameFieldId}
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  placeholder="my-skill"
                  autoComplete="off"
                  spellCheck={false}
                  aria-invalid={name.length > 0 && !nameValid}
                  className="font-mono"
                />
                {!nameValid && (
                  <p className="text-xs text-muted-foreground">{nameRule}</p>
                )}
              </div>

              <Textarea
                value={body}
                onChange={(event) => setBody(event.target.value)}
                spellCheck={false}
                aria-label="SKILL.md content"
                className="min-h-[35vh] flex-1 resize-none overflow-y-auto font-mono text-xs leading-relaxed"
              />

              {refusal && (
                <p className="whitespace-pre-wrap rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
                  {refusal}
                </p>
              )}

              <DialogFooter className="flex-row gap-2">
                <Button
                  type="button"
                  variant="outline"
                  className="mr-auto rounded-full"
                  onClick={() => setStep("choose")}
                >
                  Back
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  className="rounded-full"
                  onClick={() => handleOpenChange(false)}
                >
                  Cancel
                </Button>
                <Button
                  type="button"
                  variant="action"
                  className="rounded-full"
                  disabled={!canSubmit}
                  onClick={() => void handleCreate()}
                >
                  {creating ? "Creating…" : "Create skill"}
                </Button>
              </DialogFooter>
            </>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}
