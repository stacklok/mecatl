// SPDX-License-Identifier: Apache-2.0

import { AlertCircle, GitBranch, Pencil } from "lucide-react";
import { cn } from "@/lib/utils";
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

function toolFacts(toolCount?: number, currentTool?: string): string[] {
  const facts: string[] = [];
  if (toolCount !== undefined) facts.push(`Tools: ${toolCount}`);
  if (currentTool) facts.push(`Current tool: ${currentTool}`);
  return facts;
}

type ChipTone = "running" | "failed" | "settled";

function chipTone(state: DelegationState, failed: boolean): ChipTone {
  if (state === "running") return "running";
  return failed ? "failed" : "settled";
}

/** The prototype's state glyph: a pulsing dot while running, an alert once failed, else a branch. */
function StateGlyph({ tone }: { tone: ChipTone }) {
  if (tone === "running") {
    return (
      <span aria-hidden="true" className="size-2 shrink-0 animate-pulse rounded-full bg-brand" />
    );
  }
  if (tone === "failed") return <AlertCircle aria-hidden="true" className="size-3 shrink-0" />;
  return <GitBranch aria-hidden="true" className="size-3 shrink-0" />;
}

const chipClass =
  "inline-flex min-w-0 max-w-full items-center gap-1.5 rounded-full bg-secondary px-2.5 py-1 text-left text-xs leading-4 text-muted-foreground transition-colors hover:bg-secondary/80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2";
const failedChipClass = "bg-destructive/10 text-destructive hover:bg-destructive/15";

/**
 * One line of a card: the identity, then the observed facts, joined with
 * " · ". The state rides in its own live region so a screen reader hears a
 * child settle; the whole line is the hover title, since the chip truncates.
 */
function ChipLine({
  facts,
  identity,
  state,
}: {
  facts: string[];
  identity: string;
  state: string;
}) {
  return (
    <span className="min-w-0 truncate" title={[identity, state, ...facts].join(" · ")}>
      {identity}
      {" · "}
      <span aria-atomic="true" aria-live="polite">
        {state}
      </span>
      {facts.map((fact) => ` · ${fact}`).join("")}
    </span>
  );
}

function CauseLine({ cause }: { cause?: string }) {
  if (!cause) return null;
  return (
    <p className="mt-0.5 truncate pl-1 text-xs text-destructive/90" title={cause}>
      {cause}
    </p>
  );
}

function teamMemberState(member: TeamMemberActivity): string {
  if (member.disposition === "stopped") return `Stopped: ${member.reason ?? "reason unknown"}`;
  if (member.disposition === "done") return "Done";
  return delegationStateLabel(member.state, { cause: member.cause });
}

/**
 * One delegated child inline in the transcript, in the prototype's chip: a
 * state glyph and one line of Studio's observed facts. A subagent and a team
 * member each get a chip; a parallel group is one chip that lists its
 * branches as inner pills. Read-only: a click opens the child in the
 * activity panel.
 */
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
    const failed = delegationStateLabel(activity.state, activity) === "Failed";
    const focus: DelegationFocus = { family: "subagent", key: activity.key };
    return (
      <div className="flex min-w-0 max-w-full flex-col">
        <button
          className={cn(chipClass, failed && failedChipClass)}
          data-delegation-focus={JSON.stringify(focus)}
          onClick={(event) => onOpen(focus, event.currentTarget)}
          type="button"
        >
          <StateGlyph tone={chipTone(activity.state, failed)} />
          <ChipLine
            facts={[
              ...toolFacts(activity.toolCount, activity.currentTool),
              ...(activity.stop ? [`Stop: ${activity.stop}`] : []),
              ...(activity.historyIncomplete ? ["History incomplete"] : []),
            ]}
            identity={`Subagent ${activity.childId}${activity.goal ? `: ${activity.goal}` : ""}`}
            state={delegationStateLabel(activity.state, activity)}
          />
        </button>
        {failed && <CauseLine cause={activity.cause} />}
      </div>
    );
  }

  if (activity.family === "parallel") {
    const failed = delegationStateLabel(activity.state, activity) === "Failed";
    const focus: DelegationFocus = { family: "parallel", key: activity.key };
    return (
      <button
        className={cn(
          chipClass,
          "flex-wrap rounded-2xl",
          activity.branches.length > 0 && "py-1.5",
          failed && failedChipClass,
        )}
        data-delegation-focus={JSON.stringify(focus)}
        onClick={(event) => onOpen(focus, event.currentTarget)}
        type="button"
      >
        <span className="inline-flex min-w-0 max-w-full items-center gap-1.5">
          <StateGlyph tone={chipTone(activity.state, failed)} />
          <ChipLine
            facts={[
              ...(activity.join ? [`Join: ${activity.join}`] : []),
              ...(activity.branchCount !== undefined ? [`Branches: ${activity.branchCount}`] : []),
              ...(activity.winner !== undefined ? [`Winner: Branch ${activity.winner + 1}`] : []),
              ...(activity.stop ? [`Stop: ${activity.stop}`] : []),
              ...(activity.historyIncomplete ? ["History incomplete"] : []),
            ]}
            identity="Parallel group"
            state={delegationStateLabel(activity.state, activity)}
          />
        </span>
        {activity.branches.map((branch) => {
          const state = delegationStateLabel(branch.state, branch);
          return (
            <span
              className={cn(
                "inline-flex min-w-0 max-w-full items-center gap-1.5 rounded-full bg-background/70 px-2 py-0.5",
                state === "Failed" && "text-destructive",
              )}
              key={branch.key}
            >
              <StateGlyph tone={chipTone(branch.state, state === "Failed")} />
              <ChipLine
                facts={[
                  ...(branch.stop ? [`Stop: ${branch.stop}`] : []),
                  ...toolFacts(branch.toolCount, branch.currentTool),
                ]}
                identity={`${branchLabel(branch)}${branch.label ? ` · ${branch.label}` : ""}`}
                state={state}
              />
            </span>
          );
        })}
      </button>
    );
  }

  const state = member ? teamMemberState(member) : delegationStateLabel(activity.state, activity);
  const failed = member
    ? member.disposition === "stopped" || state === "Failed"
    : state === "Failed";
  const focus: DelegationFocus = { family: "team", key: activity.key, memberKey: member?.key };
  const identity = [
    `Team ${activity.teamId}`,
    ...(member ? [`Member ${member.name}`] : []),
    ...(member?.lead ? ["Lead"] : []),
    ...(member?.role ? [member.role] : []),
  ].join(" · ");
  return (
    <div className="flex min-w-0 max-w-full flex-col">
      <button
        className={cn(chipClass, failed && failedChipClass)}
        data-delegation-focus={JSON.stringify(focus)}
        onClick={(event) => onOpen(focus, event.currentTarget)}
        type="button"
      >
        <StateGlyph
          tone={
            member && (member.disposition === "done" || member.disposition === "stopped")
              ? failed
                ? "failed"
                : "settled"
              : chipTone(member?.state ?? activity.state, failed)
          }
        />
        {member?.mutating && (
          <Pencil aria-label="Read-write" className="size-3 shrink-0" role="img" />
        )}
        <ChipLine
          facts={[
            ...(member?.currentTool ? [`Current tool: ${member.currentTool}`] : []),
            ...(activity.stop ? [`Stop: ${activity.stop}`] : []),
            ...(activity.historyIncomplete ? ["History incomplete"] : []),
          ]}
          identity={identity}
          state={state}
        />
      </button>
      {failed && <CauseLine cause={member?.cause} />}
    </div>
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
    <fieldset className="mt-1.5 flex min-w-0 max-w-full flex-wrap gap-1.5">
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
