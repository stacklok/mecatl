// SPDX-License-Identifier: Apache-2.0

import { Bot, GitFork, Users } from "lucide-react";
import type {
  DelegationState,
  ParallelBranchActivity,
  ParallelGroupActivity,
  SubagentActivity,
  TeamActivity,
  TeamMemberActivity,
} from "./delegation-fleet";

export type DelegationActivity = SubagentActivity | ParallelGroupActivity | TeamActivity;

/** A selection is scoped to a fleet entry, never to an unqualified child ID. */
export type DelegationFocus =
  | { family: "subagent"; key: string }
  | { family: "parallel"; key: string; branchKey?: string }
  | { family: "team"; key: string; memberKey?: string };

export function delegationStateLabel(
  state: DelegationState,
  options: { cause?: string; failed?: boolean; stop?: string } = {},
): string {
  if (state === "unknown") return "Outcome unknown";
  if (state === "running") return "Running";
  if (options.failed || options.cause || options.stop === "error") return "Failed";
  return "Finished";
}

export function branchLabel(branch: ParallelBranchActivity): string {
  return `Branch ${branch.branchIndex + 1}`;
}

function ToolSummary({ currentTool, toolCount }: { currentTool?: string; toolCount?: number }) {
  if (toolCount === undefined && !currentTool) return null;
  return (
    <span className="min-w-0 break-words [overflow-wrap:anywhere]">
      {toolCount !== undefined && `Tools: ${toolCount}`}
      {toolCount !== undefined && currentTool && " · "}
      {currentTool && `Current tool: ${currentTool}`}
    </span>
  );
}

const cardClass =
  "flex min-w-0 max-w-full flex-col gap-1 rounded-lg border border-border/70 bg-muted/20 px-3 py-2 text-left text-xs leading-5 transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 [overflow-wrap:anywhere]";

export function DelegationCard({
  activity,
  member,
  onOpen,
}: {
  activity: DelegationActivity;
  member?: TeamMemberActivity;
  onOpen: (focus: DelegationFocus, opener: HTMLButtonElement) => void;
}) {
  if (activity.family === "subagent") {
    return (
      <button
        className={cardClass}
        onClick={(event) => onOpen({ family: "subagent", key: activity.key }, event.currentTarget)}
        type="button"
      >
        <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 font-medium">
          <Bot aria-hidden="true" className="size-4 shrink-0 text-brand" />
          <span>Subagent {activity.childId}</span>
          <span aria-atomic="true" aria-live="polite">
            {delegationStateLabel(activity.state, activity)}
          </span>
        </span>
        {activity.goal && <span className="min-w-0 break-words">{activity.goal}</span>}
        <span className="flex min-w-0 flex-wrap gap-x-2 text-muted-foreground">
          <ToolSummary currentTool={activity.currentTool} toolCount={activity.toolCount} />
          {activity.stop && <span>Stop: {activity.stop}</span>}
          {activity.historyIncomplete && <span>History incomplete</span>}
        </span>
      </button>
    );
  }

  if (activity.family === "parallel") {
    return (
      <button
        className={cardClass}
        onClick={(event) => onOpen({ family: "parallel", key: activity.key }, event.currentTarget)}
        type="button"
      >
        <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 font-medium">
          <GitFork aria-hidden="true" className="size-4 shrink-0 text-brand" />
          <span>Parallel group</span>
          <span aria-atomic="true" aria-live="polite">
            {delegationStateLabel(activity.state, activity)}
          </span>
        </span>
        <span className="flex min-w-0 flex-wrap gap-x-2 text-muted-foreground">
          {activity.join && <span>Join: {activity.join}</span>}
          {activity.branchCount !== undefined && <span>Branches: {activity.branchCount}</span>}
          {activity.winner !== undefined && <span>Winner: Branch {activity.winner + 1}</span>}
          {activity.stop && <span>Stop: {activity.stop}</span>}
          {activity.historyIncomplete && <span>History incomplete</span>}
        </span>
        {activity.branches.length > 0 && (
          <span className="flex min-w-0 flex-wrap gap-x-2 gap-y-0.5">
            {activity.branches.map((branch) => (
              <span className="min-w-0 break-words" key={branch.key}>
                {branchLabel(branch)}
                {branch.label && ` · ${branch.label}`}
                {` · ${delegationStateLabel(branch.state, branch)}`}
                {branch.stop && ` · Stop: ${branch.stop}`}
                {branch.toolCount !== undefined && ` · Tools: ${branch.toolCount}`}
                {branch.currentTool && ` · Current tool: ${branch.currentTool}`}
              </span>
            ))}
          </span>
        )}
      </button>
    );
  }

  return (
    <button
      className={cardClass}
      onClick={(event) =>
        onOpen({ family: "team", key: activity.key, memberKey: member?.key }, event.currentTarget)
      }
      type="button"
    >
      <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 font-medium">
        <Users aria-hidden="true" className="size-4 shrink-0 text-brand" />
        <span>Team {activity.teamId}</span>
        {member && <span>Member {member.name}</span>}
        {member?.lead && <span>Lead</span>}
        <span aria-atomic="true" aria-live="polite">
          {member
            ? member.disposition === "stopped"
              ? `Stopped: ${member.reason ?? "reason unknown"}`
              : member.disposition === "done"
                ? "Done"
                : delegationStateLabel(member.state, { cause: member.cause })
            : delegationStateLabel(activity.state, activity)}
        </span>
      </span>
      {member?.role && <span className="min-w-0 break-words">{member.role}</span>}
      <span className="flex min-w-0 flex-wrap gap-x-2 text-muted-foreground">
        {member?.currentTool && <span>Current tool: {member.currentTool}</span>}
        {activity.stop && <span>Stop: {activity.stop}</span>}
        {activity.historyIncomplete && <span>History incomplete</span>}
      </span>
    </button>
  );
}

/** Place one compact family card per observed child, group, or team member. */
export function DelegationCardRow({
  activities,
  onOpen,
}: {
  activities: DelegationActivity[];
  onOpen: (focus: DelegationFocus, opener: HTMLButtonElement) => void;
}) {
  if (activities.length === 0) return null;
  return (
    <fieldset className="mt-2 flex min-w-0 max-w-full flex-wrap gap-2">
      <legend className="sr-only">Delegated activity</legend>
      {activities.flatMap((activity) =>
        activity.family === "team" && activity.members.length > 0
          ? activity.members.map((member) => (
              <DelegationCard
                activity={activity}
                key={member.key}
                member={member}
                onOpen={onOpen}
              />
            ))
          : [<DelegationCard activity={activity} key={activity.key} onOpen={onOpen} />],
      )}
    </fieldset>
  );
}
