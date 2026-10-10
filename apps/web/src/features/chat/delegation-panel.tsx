// SPDX-License-Identifier: Apache-2.0

import { AlertCircle, Check, CircleHelp } from "lucide-react";
import { type Ref, useEffect, useId, useRef, useState } from "react";
import { cn } from "@/lib/utils";
import { branchLabel, type DelegationFocus, delegationStateLabel } from "./delegation-card";
import type {
  DelegationFleet,
  DelegationTrace,
  ParallelBranchActivity,
  ParallelGroupActivity,
  SubagentActivity,
  TeamActivity,
  TeamMemberActivity,
} from "./delegation-fleet";

export type { DelegationFocus } from "./delegation-card";

type Family = DelegationFocus["family"];
const families: Family[] = ["subagent", "parallel", "team"];
const familyLabels: Record<Family, string> = {
  subagent: "Subagents",
  parallel: "Parallel",
  team: "Teams",
};

function entries(fleet: DelegationFleet, family: Family) {
  if (family === "subagent") return fleet.subagents;
  if (family === "parallel") return fleet.parallelGroups;
  return fleet.teams;
}

function initialFamily(fleet: DelegationFleet): Family {
  return (
    families.find((family) => entries(fleet, family).some((item) => item.state === "running")) ??
    families.find((family) => entries(fleet, family).length > 0) ??
    "subagent"
  );
}

/** Read-only delegated activity; the surrounding side-panel frame remains independently owned. */
export function SessionActivityContent({
  family: requestedFamily,
  fleet,
  focus,
  onFocusChange,
}: {
  /** The family to open on, as the header's fleet chip asks; else the busiest one. */
  family?: Family;
  fleet: DelegationFleet;
  focus?: DelegationFocus;
  onFocusChange: (focus?: DelegationFocus) => void;
}) {
  const id = useId();
  const [roster, setRoster] = useState(() => ({
    family: requestedFamily ?? initialFamily(fleet),
    sessionId: fleet.sessionId,
  }));
  const headingRef = useRef<HTMLHeadingElement>(null);
  const focusIdentity = focus ? JSON.stringify(focus) : undefined;
  useEffect(() => {
    if (focusIdentity) headingRef.current?.focus();
  }, [focusIdentity]);
  const tabRefs = useRef<Record<Family, HTMLButtonElement | null>>({
    subagent: null,
    parallel: null,
    team: null,
  });
  const family =
    focus?.family ?? (roster.sessionId === fleet.sessionId ? roster.family : initialFamily(fleet));
  const selected = focus && entries(fleet, focus.family).find((item) => item.key === focus.key);

  function chooseFamily(next: Family, moveFocus: boolean) {
    setRoster({ family: next, sessionId: fleet.sessionId });
    onFocusChange(undefined);
    if (moveFocus) tabRefs.current[next]?.focus();
  }

  function handleTabKey(event: React.KeyboardEvent<HTMLButtonElement>, current: Family) {
    const index = families.indexOf(current);
    let next: Family | undefined;
    if (event.key === "ArrowRight") next = families[(index + 1) % families.length];
    else if (event.key === "ArrowLeft")
      next = families[(index + families.length - 1) % families.length];
    else if (event.key === "Home") next = families[0];
    else if (event.key === "End") next = families.at(-1);
    if (!next) return;
    event.preventDefault();
    chooseFamily(next, true);
  }

  return (
    <div className="flex min-h-0 min-w-0 max-w-full flex-col gap-3 px-4 py-3 text-sm [overflow-wrap:anywhere]">
      {fleet.incompleteHistory && (
        <p className="rounded-md border border-warning/50 bg-warning/10 px-3 py-2 text-xs">
          Activity history incomplete. Only observed events are shown; an unfinished outcome may be
          unknown.
        </p>
      )}
      {/* The Tabs primitive's look (TabsList/TabsTrigger), kept on this
          tablist so its family focus and keyboard rules stay as they are. */}
      <div
        aria-label="Activity families"
        className="grid min-h-9 min-w-0 grid-cols-3 items-stretch rounded-lg bg-muted p-[3px] text-muted-foreground"
        role="tablist"
      >
        {families.map((item) => (
          <button
            aria-label={`${familyLabels[item]} (${entries(fleet, item).length})`}
            aria-controls={`${id}-panel`}
            aria-selected={family === item}
            className="flex min-w-0 flex-wrap items-center justify-center gap-x-1 rounded-md px-1 py-1 text-xs font-medium leading-4 text-foreground transition-[color,box-shadow] focus-visible:outline-1 focus-visible:outline-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 data-[active=true]:bg-background data-[active=true]:shadow-sm dark:text-muted-foreground dark:data-[active=true]:bg-card dark:data-[active=true]:text-foreground"
            data-active={family === item}
            id={`${id}-${item}`}
            key={item}
            onClick={() => chooseFamily(item, false)}
            onKeyDown={(event) => handleTabKey(event, item)}
            ref={(node) => {
              tabRefs.current[item] = node;
            }}
            role="tab"
            tabIndex={family === item ? 0 : -1}
            type="button"
          >
            <span className="whitespace-nowrap">{familyLabels[item]}</span>
            <span className="tabular-nums text-muted-foreground">
              ({entries(fleet, item).length})
            </span>
          </button>
        ))}
      </div>
      <div
        aria-labelledby={`${id}-${family}`}
        className="flex min-w-0 flex-col gap-4"
        id={`${id}-panel`}
        role="tabpanel"
      >
        <section className="min-w-0">
          <h3 className="mb-1.5 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
            Roster
          </h3>
          {entries(fleet, family).length === 0 ? (
            <p className="py-4 text-sm text-muted-foreground">
              No observed {familyLabels[family].toLowerCase()} activity.
            </p>
          ) : (
            <div className="flex min-w-0 flex-col">
              {family === "subagent" &&
                fleet.subagents.map((item) => (
                  <RosterButton
                    key={item.key}
                    onClick={() => onFocusChange({ family: "subagent", key: item.key })}
                    state={delegationStateLabel(item.state, item)}
                    selected={selected?.key === item.key}
                  >
                    Subagent {item.childId} · {delegationStateLabel(item.state, item)}
                  </RosterButton>
                ))}
              {family === "parallel" &&
                fleet.parallelGroups.map((item) => (
                  <RosterButton
                    key={item.key}
                    onClick={() => onFocusChange({ family: "parallel", key: item.key })}
                    state={delegationStateLabel(item.state, item)}
                    selected={selected?.key === item.key}
                  >
                    Parallel group {item.parentCallId} · {delegationStateLabel(item.state, item)}
                  </RosterButton>
                ))}
              {family === "team" &&
                fleet.teams.map((item) => (
                  <RosterButton
                    key={item.key}
                    onClick={() => onFocusChange({ family: "team", key: item.key })}
                    state={delegationStateLabel(item.state, item)}
                    selected={selected?.key === item.key}
                  >
                    Team {item.teamId} · {delegationStateLabel(item.state, item)}
                  </RosterButton>
                ))}
            </div>
          )}
        </section>
        {selected ? (
          selected.family === "subagent" ? (
            <SubagentDetails headingRef={headingRef} item={selected} />
          ) : selected.family === "parallel" ? (
            <ParallelDetails
              headingRef={headingRef}
              focus={focus?.family === "parallel" ? focus : undefined}
              item={selected}
              onFocusChange={onFocusChange}
            />
          ) : (
            <TeamDetails
              headingRef={headingRef}
              focus={focus?.family === "team" ? focus : undefined}
              item={selected}
              onFocusChange={onFocusChange}
            />
          )
        ) : (
          <p className="text-sm text-muted-foreground">Choose an activity to see its details.</p>
        )}
      </div>
    </div>
  );
}

/** The prototype's roster glyph for one observed outcome label. */
function RosterGlyph({ state }: { state: string }) {
  if (state === "Running") {
    return (
      <span
        aria-hidden="true"
        className="mt-1 size-2 shrink-0 animate-pulse rounded-full bg-brand"
      />
    );
  }
  if (state === "Failed" || state.startsWith("Stopped")) {
    return <AlertCircle aria-hidden="true" className="mt-0.5 size-3.5 shrink-0 text-destructive" />;
  }
  if (state === "Outcome unknown") {
    return (
      <CircleHelp aria-hidden="true" className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
    );
  }
  return <Check aria-hidden="true" className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />;
}

function RosterButton({
  children,
  onClick,
  selected,
  state,
}: {
  children: React.ReactNode;
  onClick: () => void;
  selected: boolean;
  /** The row's observed outcome label, shown as the prototype's state glyph. */
  state: string;
}) {
  return (
    <button
      aria-pressed={selected}
      className={cn(
        "flex w-full min-w-0 max-w-full items-start gap-2 rounded-md px-2 py-1.5 text-left text-xs text-foreground/90 transition-colors hover:bg-secondary/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring data-[selected=true]:bg-secondary",
        (state === "Failed" || state.startsWith("Stopped")) && "text-destructive",
      )}
      data-selected={selected}
      onClick={onClick}
      type="button"
    >
      <RosterGlyph state={state} />
      <span className="min-w-0 flex-1">{children}</span>
    </button>
  );
}

function HistoryNotice({ item }: { item: { historyIncomplete: boolean; startObserved: boolean } }) {
  if (!item.historyIncomplete && item.startObserved) return null;
  return (
    <p className="rounded-md border border-warning/50 bg-warning/10 px-3 py-2 text-xs">
      History incomplete{!item.startObserved ? ": start event not observed" : ""}. Only observed
      facts are shown.
    </p>
  );
}

function ObservedStatus({
  cause,
  failed,
  state,
  stop,
}: {
  cause?: string;
  failed?: boolean;
  state: SubagentActivity["state"];
  stop?: string;
}) {
  return (
    <p aria-atomic="true" aria-live="polite" className="text-xs font-medium">
      State: {delegationStateLabel(state, { cause, failed, stop })}
      {stop && <span> · Stop: {stop}</span>}
    </p>
  );
}

function ToolFacts({ currentTool, toolCount }: { currentTool?: string; toolCount?: number }) {
  if (currentTool === undefined && toolCount === undefined) return null;
  return (
    <p className="text-xs text-muted-foreground">
      {toolCount !== undefined && `Tools: ${toolCount}`}
      {toolCount !== undefined && currentTool && " · "}
      {currentTool && `Current tool: ${currentTool}`}
    </p>
  );
}

function SubagentDetails({
  headingRef,
  item,
}: {
  headingRef: Ref<HTMLHeadingElement>;
  item: SubagentActivity;
}) {
  return (
    <section className="min-w-0 space-y-2 border-t pt-3 text-xs">
      <h3 className="text-sm font-medium" ref={headingRef} tabIndex={-1}>
        Subagent {item.childId}
      </h3>
      <HistoryNotice item={item} />
      <ObservedStatus cause={item.cause} state={item.state} stop={item.stop} />
      {item.goal && <p>Goal: {item.goal}</p>}
      {item.background !== undefined && <p>Background: {item.background ? "yes" : "no"}</p>}
      <ToolFacts currentTool={item.currentTool} toolCount={item.toolCount} />
      {item.cause && <p>Cause: {item.cause}</p>}
      {item.durationMs && <p>Duration: {item.durationMs} ms</p>}
      <Trace trace={item.trace} />
    </section>
  );
}

function ParallelDetails({
  headingRef,
  focus,
  item,
  onFocusChange,
}: {
  focus?: Extract<DelegationFocus, { family: "parallel" }>;
  headingRef: Ref<HTMLHeadingElement>;
  item: ParallelGroupActivity;
  onFocusChange: (focus?: DelegationFocus) => void;
}) {
  const branch = item.branches.find((entry) => entry.key === focus?.branchKey);
  return (
    <section className="min-w-0 space-y-2 border-t pt-3 text-xs">
      <h3 className="text-sm font-medium" ref={branch ? undefined : headingRef} tabIndex={-1}>
        Parallel group {item.parentCallId}
      </h3>
      <HistoryNotice item={item} />
      <ObservedStatus state={item.state} stop={item.stop} />
      <p className="flex min-w-0 flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
        {item.join && <span>Join: {item.join}</span>}
        {item.branchCount !== undefined && <span>Branches: {item.branchCount}</span>}
        {item.winner !== undefined && <span>Winner: Branch {item.winner + 1}</span>}
      </p>
      <div className="min-w-0 space-y-2">
        <h4 className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
          Branch states
        </h4>
        {item.branches.length === 0 && <p>No branch events observed.</p>}
        {item.branches.map((entry) => (
          <RosterButton
            key={entry.key}
            onClick={() =>
              onFocusChange({ family: "parallel", key: item.key, branchKey: entry.key })
            }
            state={delegationStateLabel(entry.state, entry)}
            selected={branch?.key === entry.key}
          >
            <span className="block font-medium">
              {branchLabel(entry)}
              {entry.label && ` · ${entry.label}`}
              {item.winner === entry.branchIndex && " · Winner"}
            </span>
            <span className="block text-xs text-muted-foreground">
              {delegationStateLabel(entry.state, entry)}
              {entry.stop && ` · Stop: ${entry.stop}`}
              {entry.currentTool && ` · Current tool: ${entry.currentTool}`}
              {entry.toolCount !== undefined && ` · Tools: ${entry.toolCount}`}
            </span>
          </RosterButton>
        ))}
      </div>
      {branch ? (
        <BranchDetails branch={branch} headingRef={headingRef} />
      ) : (
        <p className="text-xs text-muted-foreground">Choose a branch to see its trace.</p>
      )}
    </section>
  );
}

function BranchDetails({
  branch,
  headingRef,
}: {
  branch: ParallelBranchActivity;
  headingRef: Ref<HTMLHeadingElement>;
}) {
  return (
    <section className="min-w-0 space-y-2 border-t pt-3 text-xs">
      <h4 className="font-medium" ref={headingRef} tabIndex={-1}>
        {branchLabel(branch)}
      </h4>
      <HistoryNotice item={branch} />
      <ObservedStatus failed={branch.failed} state={branch.state} stop={branch.stop} />
      {branch.goal && <p>Goal: {branch.goal}</p>}
      <ToolFacts currentTool={branch.currentTool} toolCount={branch.toolCount} />
      {branch.durationMs && <p>Duration: {branch.durationMs} ms</p>}
      <Trace trace={branch.trace} />
    </section>
  );
}

function TeamDetails({
  headingRef,
  focus,
  item,
  onFocusChange,
}: {
  focus?: Extract<DelegationFocus, { family: "team" }>;
  headingRef: Ref<HTMLHeadingElement>;
  item: TeamActivity;
  onFocusChange: (focus?: DelegationFocus) => void;
}) {
  const selectedMember = item.members.find((member) => member.key === focus?.memberKey);
  return (
    <section className="min-w-0 space-y-3 border-t pt-3 text-xs">
      <h3
        className="text-sm font-medium"
        ref={selectedMember ? undefined : headingRef}
        tabIndex={-1}
      >
        Team {item.teamId}
      </h3>
      <HistoryNotice item={item} />
      <ObservedStatus state={item.state} stop={item.stop} />
      {item.rounds !== undefined && <p>Rounds: {item.rounds}</p>}
      {selectedMember ? (
        <MemberDetails headingRef={headingRef} member={selectedMember} />
      ) : (
        <p className="text-xs text-muted-foreground">Choose a member to see its trace.</p>
      )}
      <section className="min-w-0 space-y-2">
        <h4 className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
          Roster
        </h4>
        {item.members.length === 0 && <p>No members observed.</p>}
        {item.members.map((member) => (
          <RosterButton
            key={member.key}
            onClick={() => onFocusChange({ family: "team", key: item.key, memberKey: member.key })}
            state={memberOutcome(member)}
            selected={selectedMember?.key === member.key}
          >
            <span className="block font-medium">
              {member.name}
              {member.lead && " · Lead"}
              {member.role && ` · ${member.role}`}
            </span>
            <span className="block text-xs text-muted-foreground">{memberOutcome(member)}</span>
          </RosterButton>
        ))}
      </section>
      <section className="min-w-0 space-y-2">
        <h4 className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
          Tasks
        </h4>
        {item.tasks.length === 0 && <p>No current tasks observed.</p>}
        <ul className="min-w-0 space-y-2">
          {item.tasks.map((task) => (
            <li className="min-w-0 rounded-md border border-border px-2 py-1.5" key={task.id}>
              <p className="font-medium">
                Task {task.id} · {task.state}
              </p>
              <p>{task.description}</p>
              {task.assignee && <p>Assignee: {task.assignee}</p>}
              <p>Dependencies: {task.deps.length > 0 ? task.deps.join(", ") : "none"}</p>
            </li>
          ))}
        </ul>
      </section>
      <section className="min-w-0 space-y-2">
        <h4 className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
          Findings
        </h4>
        {item.findings.length === 0 && <p>No findings observed.</p>}
        <ul className="min-w-0 space-y-2">
          {withOccurrenceKeys(item.findings).map(({ item: finding, key }) => (
            <li className="min-w-0 rounded-md border border-border px-2 py-1.5" key={key}>
              <span className="font-medium">{finding.member}: </span>
              {finding.body}
            </li>
          ))}
        </ul>
      </section>
    </section>
  );
}

function memberOutcome(member: TeamMemberActivity): string {
  if (member.disposition === "stopped") return `Stopped: ${member.reason ?? "reason unknown"}`;
  if (member.disposition === "done") return "Done";
  return delegationStateLabel(member.state, { cause: member.cause });
}

function MemberDetails({
  headingRef,
  member,
}: {
  headingRef: Ref<HTMLHeadingElement>;
  member: TeamMemberActivity;
}) {
  return (
    <section className="min-w-0 space-y-2 border-t pt-3 text-xs">
      <h4 className="font-medium" ref={headingRef} tabIndex={-1}>
        Member {member.name}
      </h4>
      <p aria-atomic="true" aria-live="polite">
        State: {memberOutcome(member)}
      </p>
      {member.roundState && <p>Last round: {member.roundState}</p>}
      {member.currentTool && <p>Current tool: {member.currentTool}</p>}
      {member.cause && <p>Cause: {member.cause}</p>}
      {member.errorRounds !== undefined && <p>Error rounds: {member.errorRounds}</p>}
      <Trace trace={member.trace} />
    </section>
  );
}

function Trace({ trace }: { trace: DelegationTrace }) {
  return (
    <section aria-label="Recent trace" aria-live="off" className="min-w-0 space-y-2">
      <h4 className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
        Recent trace
      </h4>
      {trace.omitted > 0 && (
        <p className="text-xs text-muted-foreground">Older entries omitted: {trace.omitted}</p>
      )}
      {trace.entries.length === 0 ? (
        <p className="text-sm text-muted-foreground">No trace entries observed.</p>
      ) : (
        <ol className="min-w-0 space-y-1">
          {withOccurrenceKeys(trace.entries).map(({ item: entry, key }) => (
            <li
              className={cn(
                "min-w-0 rounded-md border border-border bg-muted/30 px-2 py-1 font-mono text-[11px]",
                entry.isError && "border-destructive/40 text-destructive/90",
              )}
              key={key}
            >
              <p className="font-medium">
                {entry.kind}
                {entry.isError && " · Error"}
              </p>
              {entry.toolName && <p>Tool: {entry.toolName}</p>}
              {entry.text && <p className="whitespace-pre-wrap break-words">{entry.text}</p>}
              {entry.detail && (
                <p className="whitespace-pre-wrap break-words">Detail: {entry.detail}</p>
              )}
              {entry.cause && <p>Cause: {entry.cause}</p>}
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

/** Snapshot entries have no server IDs; repeated identical rows need distinct React keys. */
function withOccurrenceKeys<T>(items: T[]): Array<{ item: T; key: string }> {
  const seen = new Map<string, number>();
  return items.map((item) => {
    const signature = JSON.stringify(item);
    const occurrence = seen.get(signature) ?? 0;
    seen.set(signature, occurrence + 1);
    return { item, key: `${signature}:${occurrence}` };
  });
}
