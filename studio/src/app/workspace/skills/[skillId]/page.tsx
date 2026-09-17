"use client";

import { Ellipsis } from "lucide-react";
import { useParams, useRouter } from "next/navigation";
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import { useAgentSkills } from "@/features/agent/hooks/use-agent-skills";
import { pageTitleClass } from "@/lib/typography";
import { cn } from "@/lib/utils";
import { EditSkillDialog } from "../_components/edit-skill-dialog";
import { SkillFiles } from "../_components/skill-files";

/** "pr-feedback" → "Pr Feedback"; the raw slug stays the id/route param. */
function humanizeSkillName(name: string): string {
  return name
    .split(/[-_]+/)
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ");
}

/** Why a management action is unavailable in external mode. */
const externalManagedTitle = "Managed by the external mecated deployment";

export default function SkillDetailPage() {
  const router = useRouter();
  const params = useParams<{ skillId: string }>();
  const name = decodeURIComponent(params.skillId);
  const {
    skills,
    disabled,
    manageable,
    isLoading,
    error,
    actionError,
    fetchBody,
    fetchFiles,
    fetchFile,
    saveBody,
    setEnabled,
    remove,
  } = useAgentSkills();
  const [confirmToggle, setConfirmToggle] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [editing, setEditing] = useState(false);

  // The daemon's inventory holds the enabled skills; the controller's
  // holding area supplies the disabled ones, so a parked skill still has a
  // detail page (that is where Enable lives).
  const enabledSkill = skills.find((s) => s.name === name);
  const disabledSkill = disabled.find((s) => s.name === name);
  const skill = enabledSkill ?? disabledSkill;
  const enabled = Boolean(enabledSkill);

  const back = (
    <Button
      variant="outline"
      size="sm"
      className="w-fit self-start rounded-full h-9 px-4 gap-1"
      onClick={() => router.back()}
    >
      <span aria-hidden="true">‹</span>
      Back
    </Button>
  );

  if (isLoading) {
    return (
      <div className="space-y-5 px-3 pt-6 pb-8 min-[500px]:px-4">
        {back}
        <Skeleton className="h-12 w-72 rounded-lg" />
        <Skeleton className="h-24 max-w-[465px] rounded-lg" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="space-y-5 px-3 pt-6 pb-8 min-[500px]:px-4">
        {back}
        <div className="rounded-lg border border-dashed border-destructive/40 py-12 text-center text-sm text-destructive">
          {error}
        </div>
      </div>
    );
  }

  if (!skill) {
    return (
      <div className="space-y-5 px-3 pt-6 pb-8 min-[500px]:px-4">
        {back}
        <div className="rounded-lg border border-dashed py-12 text-center text-sm text-muted-foreground">
          No skill named <code className="font-mono text-xs">{name}</code> in
          the daemon&apos;s inventory.
        </div>
      </div>
    );
  }

  const agentOwned = enabledSkill?.agentOwned === true;

  return (
    <div className="space-y-5 px-3 pt-6 pb-8 min-[500px]:px-4">
      {back}

      {/* Title + metadata pills */}
      <div className="space-y-3">
        <h1
          className={pageTitleClass(
            "text-[44px] leading-[1.05] max-[499px]:text-3xl",
          )}
        >
          {humanizeSkillName(skill.name)}
        </h1>
        <div className="flex flex-wrap items-center gap-2">
          <MetaPill className="font-mono">{skill.name}</MetaPill>
          {!enabled && <MetaPill className="border-dashed">Disabled</MetaPill>}
          {agentOwned && (
            <>
              {enabledSkill?.ownerAgent ? (
                <MetaPill>by {enabledSkill.ownerAgent}</MetaPill>
              ) : null}
              {enabledSkill?.activeVersion ? (
                <MetaPill className="font-mono">
                  {enabledSkill.activeVersion}
                </MetaPill>
              ) : null}
            </>
          )}
        </div>
      </div>

      {/* Management refusals from the controller, verbatim. */}
      {actionError && (
        <p className="max-w-4xl whitespace-pre-wrap rounded-lg border border-destructive/30 bg-destructive/5 px-4 py-3 text-sm text-destructive">
          {actionError}
        </p>
      )}

      <div className="flex flex-col gap-10 lg:flex-row lg:items-start">
        <aside className="flex w-full max-w-[465px] flex-col gap-6">
          <div className="space-y-3">
            <h2 className="text-base font-semibold">Summary</h2>
            <p className="text-base leading-relaxed text-muted-foreground">
              {skill.description}
            </p>
          </div>

          {/* The controls stack, under Summary. Every mutation goes through
              the local controller; enable/disable/delete of a visible skill
              restarts the daemon, so each destructive-ish path confirms
              first. In external mode the deployment owns its skills dir and
              the controls render disabled rather than manufacturing 409s. */}
          <div className="space-y-3">
            <h2 className="text-base font-semibold">Manage</h2>
            <div className="flex items-center gap-2">
              <Button
                variant="outline"
                className="rounded-full"
                disabled={!manageable}
                title={manageable ? undefined : externalManagedTitle}
                onClick={() => setEditing(true)}
              >
                Edit
              </Button>
              <DropdownMenu modal={false}>
                <DropdownMenuTrigger asChild>
                  <Button
                    variant="outline"
                    size="icon"
                    className="size-9 rounded-full"
                    disabled={!manageable}
                    title={manageable ? undefined : externalManagedTitle}
                    aria-label={`More actions for ${skill.name}`}
                  >
                    <Ellipsis className="size-4" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start">
                  <DropdownMenuItem onClick={() => setConfirmToggle(true)}>
                    {enabled ? "Disable" : "Enable"}
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    variant="destructive"
                    onClick={() => setConfirmDelete(true)}
                  >
                    Delete
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
            {!manageable && (
              <p className="text-xs text-muted-foreground">
                {externalManagedTitle}. Change the deployment&rsquo;s own skills
                directory instead.
              </p>
            )}
          </div>
        </aside>

        <section className="flex min-w-0 flex-1 flex-col gap-3">
          <h2 className="text-base font-semibold">Files</h2>
          {/* A skill can be a whole folder of assets, not just a SKILL.md —
              the listing (and its read-only previews) comes from the
              controller, so external mode keeps the metadata-only note. */}
          {manageable ? (
            <SkillFiles
              name={skill.name}
              fetchFiles={fetchFiles}
              fetchFile={fetchFile}
            />
          ) : (
            <div className="rounded-lg border bg-background p-6">
              <p className="text-sm leading-relaxed text-muted-foreground">
                The daemon&apos;s inventory is metadata-only; the agent reads a
                skill&apos;s body only when it loads it.
              </p>
            </div>
          )}
        </section>
      </div>

      {editing && (
        <EditSkillDialog
          name={skill.name}
          enabled={enabled}
          fetchBody={fetchBody}
          saveBody={saveBody}
          onClose={() => setEditing(false)}
        />
      )}

      <AlertDialog open={confirmToggle} onOpenChange={setConfirmToggle}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {enabled ? `Disable ${skill.name}?` : `Enable ${skill.name}?`}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {enabled
                ? "The skill moves out of the daemon's sight and the daemon restarts — in-flight runs and session ids die with it. Enable it again any time."
                : "The skill moves back into the daemon's skills directory and the daemon restarts — in-flight runs and session ids die with it."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                void setEnabled(skill.name, !enabled).catch(() => {
                  // Refusal surfaces via actionError above.
                });
                setConfirmToggle(false);
              }}
            >
              {enabled ? "Disable" : "Enable"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {skill.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              {enabled
                ? "The skill's folder (and any bundled assets) is removed from the workspace, and the daemon restarts — in-flight runs and session ids die with it."
                : "The skill's folder (and any bundled assets) is removed from the workspace. It is already disabled, so the daemon keeps running."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                setConfirmDelete(false);
                void remove(skill.name)
                  .then(() => router.push("/workspace/skills"))
                  .catch(() => {
                    // Refusal surfaces via actionError above.
                  });
              }}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function MetaPill({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full border border-border bg-background px-3 py-1 text-xs text-muted-foreground",
        className,
      )}
    >
      {children}
    </span>
  );
}
