"use client";

import { Eye, EyeOff } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { AgentSession } from "@/features/agent";
import { formatRelativeTime } from "@/lib/formatters";
import {
  capabilityReasonLabel,
  describeRelationship,
  inspectRowTitle,
  sessionKindLabel,
} from "@/lib/session-kinds";
import { cn } from "@/lib/utils";
import { sessionActivity } from "./session-activity";
import { SidebarGroup } from "./session-sidebar";

/** Opens a run's read-only transcript (or explains why it cannot). */
export type InspectSession = (id: string) => void;

/** The DOM id the keyboard navigation focuses for a run row. */
export function inspectRowDomId(sessionId: string): string {
  return `inspect-row-${encodeURIComponent(sessionId)}`;
}

/**
 * One inspect-only inventory row (a subagent, parallel branch, team member,
 * scheduled fire, or unknown kind): the title — or, untitled, what the run
 * IS — a kind chip, a Read-only badge whose tooltip carries the daemon's
 * reason, and the relative time. Clicking never rebinds the live chat; it
 * hands the id to `onInspect`, which opens the read-only transcript. A row
 * the daemon will not show a transcript for stays listed (the inventory is
 * honest about what is stored) and says so in its label and tooltip.
 */
export function InspectSessionRow({
  session,
  onInspect,
}: {
  session: AgentSession;
  onInspect: InspectSession;
}) {
  const title = inspectRowTitle(session);
  const relationship = describeRelationship(session.relationship);
  const readOnlyReason =
    capabilityReasonLabel(session.publicChatReason) || "Read-only run";
  const transcriptDenied = session.canViewTranscript === false;
  const deniedReason =
    capabilityReasonLabel(session.viewTranscriptReason) ||
    "Transcript unavailable";
  const activity = sessionActivity(session);

  return (
    <div className="group flex items-center border-l-[3px] border-transparent py-2 pr-3 pl-3 transition-colors hover:bg-accent lg:pr-2">
      <button
        type="button"
        id={inspectRowDomId(session.id)}
        onClick={() => onInspect(session.id)}
        aria-label={`Inspect run: ${title}${
          transcriptDenied ? ` (${deniedReason.toLowerCase()})` : ""
        }`}
        title={
          transcriptDenied ? deniedReason : "Open the read-only transcript"
        }
        className="min-w-0 flex-1 select-none text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <span className="flex min-w-0 items-center gap-1.5">
          {transcriptDenied ? (
            <EyeOff
              className="size-3 shrink-0 text-muted-foreground/60"
              aria-hidden="true"
            />
          ) : (
            <Eye
              className="size-3 shrink-0 text-muted-foreground/60"
              aria-hidden="true"
            />
          )}
          <span className="block truncate text-[0.85rem] font-medium text-muted-foreground group-hover:text-foreground">
            {title}
          </span>
        </span>
        <span className="mt-0.5 flex min-w-0 items-center gap-1.5 pl-[18px]">
          <Badge
            variant="outline"
            className="h-4 shrink-0 px-1.5 text-[10px] font-medium uppercase tracking-wide text-muted-foreground"
          >
            {sessionKindLabel(session.kind)}
          </Badge>
          <Badge
            variant="outline"
            title={readOnlyReason}
            className="h-4 shrink-0 px-1.5 text-[10px] font-medium uppercase tracking-wide text-muted-foreground"
          >
            Read-only
          </Badge>
          {session.title && relationship && (
            <span
              className="truncate text-[10px] text-muted-foreground/70"
              title={relationship}
            >
              {relationship}
            </span>
          )}
        </span>
      </button>
      <div className="ml-2 flex w-8 shrink-0 items-center justify-center">
        {activity ? (
          <span
            role="img"
            aria-label={activity.label}
            className={cn(
              "size-2 rounded-full",
              activity.kind === "awaiting"
                ? "bg-warning"
                : "bg-brand animate-pulse",
            )}
          />
        ) : (
          <span
            suppressHydrationWarning
            className="text-xs tabular-nums text-muted-foreground/50"
          >
            {formatRelativeTime(session.updatedAt)}
          </span>
        )}
      </div>
    </div>
  );
}

/** Recency-grouped inspect rows, in the same group grammar as the chat list. */
export function InspectSessionGroups({
  groups,
  onInspect,
}: {
  groups: { label: string; sessions: AgentSession[] }[];
  onInspect: InspectSession;
}) {
  return (
    <div className="flex flex-col gap-3">
      {groups.map((group) => (
        <SidebarGroup key={group.label} label={group.label}>
          <div className="flex flex-col">
            {group.sessions.map((session) => (
              <InspectSessionRow
                key={session.id}
                session={session}
                onInspect={onInspect}
              />
            ))}
          </div>
        </SidebarGroup>
      ))}
    </div>
  );
}
