"use client";

import { Check, ChevronDown } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useOptionalRuntimeStatus } from "@/features/agent/runtime-status";
import {
  type SessionToolProfile,
  SHELL_DISABLED_NOTE,
  shellDisabledOnDaemon,
  TOOL_PROFILE_OPTIONS,
  toolProfileLabel,
} from "@/lib/tool-profile";
import { cn } from "@/lib/utils";

/**
 * The composer's TOOLS choice — the daemon's per-session `profile` (ADR
 * 0291): "All tools" or "No filesystem" (the file-less catalog: no Shell,
 * Read, Edit, Write…; web tools remain). On desktop it is its OWN pill next
 * to Mode (`ToolProfileSelector`); the mobile mode sheet keeps the rows.
 *
 * Two shapes, one prop contract:
 * - `onProfileChange` present = a DRAFT: the rows pick the profile the first
 *   send mints with.
 * - absent = a LIVE chat: the profile is fixed at create and the daemon
 *   never reports it back, so a KNOWN `profile` (Studio minted the chat)
 *   renders as a muted read-only line and an unknown one (undefined — the
 *   chat came from the TUI or a schedule) renders nothing at all.
 *
 * When the daemon reports its Shell tool OFF (`capabilities.bash === false`,
 * the operator's `--no-shell`) a muted note says so under the rows: "All
 * tools" then honestly means "all but Shell". Rendered outside the runtime
 * provider (a unit test, a display-only composer) the note simply stays
 * hidden.
 */

/** Read-only wording for a live chat's remembered profile. */
export function toolProfileReadOnlyLine(profile: SessionToolProfile): string {
  return `Tools: ${toolProfileLabel(profile)} — set when this chat was created`;
}

function useShellDisabledNote(): string | null {
  const runtime = useOptionalRuntimeStatus();
  return shellDisabledOnDaemon(runtime?.serverCapabilities)
    ? SHELL_DISABLED_NOTE
    : null;
}

/** The mobile mode sheet's Tools rows (the SheetOptionRow idiom). */
export function ToolProfileSheetRows({
  profile,
  onProfileChange,
}: {
  profile?: SessionToolProfile;
  onProfileChange?: (profile: SessionToolProfile) => void;
}) {
  const shellNote = useShellDisabledNote();
  if (!onProfileChange && profile === undefined) return null;
  return (
    <div className="border-t">
      <p className="px-4 pt-3 pb-1 text-xs font-medium text-muted-foreground">
        Tools
      </p>
      {onProfileChange ? (
        TOOL_PROFILE_OPTIONS.map((option) => (
          <button
            key={option.id || "all"}
            type="button"
            onClick={() => onProfileChange(option.id)}
            className="flex w-full items-center gap-3 px-4 py-3 text-sm transition-colors hover:bg-muted/50"
          >
            <span className="flex min-w-0 flex-1 flex-col text-left">
              <span className="truncate font-medium">{option.label}</span>
              <span className="text-xs text-muted-foreground">
                {option.description}
              </span>
            </span>
            <Check
              className={cn(
                "size-4 shrink-0",
                (profile ?? "") === option.id
                  ? "text-foreground"
                  : "text-transparent",
              )}
            />
          </button>
        ))
      ) : (
        <p role="note" className="px-4 py-2 text-xs text-muted-foreground">
          {toolProfileReadOnlyLine(profile ?? "")}
        </p>
      )}
      {shellNote && (
        <p role="note" className="px-4 pt-1 pb-3 text-xs text-muted-foreground">
          {shellNote}
        </p>
      )}
    </div>
  );
}

/**
 * The desktop composer's Tools pill: a dropdown of the two profiles on a
 * draft, a read-only pill on a live chat whose profile Studio remembers,
 * nothing when the profile is unknown.
 */
export function ToolProfileSelector({
  profile,
  onProfileChange,
  disabled,
  className,
}: {
  profile?: SessionToolProfile;
  onProfileChange?: (profile: SessionToolProfile) => void;
  disabled?: boolean;
  className?: string;
}) {
  const shellNote = useShellDisabledNote();
  if (!onProfileChange && profile === undefined) return null;
  const label = toolProfileLabel(profile ?? "");
  if (!onProfileChange) {
    return (
      <span
        className={cn(className, "cursor-default")}
        title={toolProfileReadOnlyLine(profile ?? "")}
        data-testid="tool-profile-pill"
      >
        <span className="max-w-40 truncate">Tools: {label}</span>
      </span>
    );
  }
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          size="sm"
          className={className}
          disabled={disabled}
          title={`Tools: ${label}`}
          data-testid="tool-profile-pill"
        >
          <span className="max-w-40 truncate @max-md:hidden">
            Tools: {label}
          </span>
          <span className="hidden @max-md:inline">Tools</span>
          <ChevronDown className="size-3.5 text-muted-foreground" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        onCloseAutoFocus={(e) => e.preventDefault()}
        align="start"
        className="w-72"
      >
        {TOOL_PROFILE_OPTIONS.map((option) => (
          <DropdownMenuItem
            key={option.id || "all"}
            className="items-start gap-2"
            onClick={() => onProfileChange(option.id)}
          >
            <Check
              className={cn(
                "mt-0.5 size-4 shrink-0",
                (profile ?? "") === option.id
                  ? "text-foreground"
                  : "text-transparent",
              )}
            />
            <span className="flex min-w-0 flex-col">
              <span>{option.label}</span>
              <span className="text-xs text-muted-foreground">
                {option.description}
              </span>
            </span>
          </DropdownMenuItem>
        ))}
        {shellNote && (
          <p role="note" className="px-2 py-1.5 text-xs text-muted-foreground">
            {shellNote}
          </p>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
