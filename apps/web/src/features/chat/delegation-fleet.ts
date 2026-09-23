// SPDX-License-Identifier: Apache-2.0

import type { RunStreamEvent } from "@mecatl-studio/contracts";

const TRACE_LIMIT = 12;
const PREVIEW_LIMIT = 201;

export type DelegationState = "running" | "finished" | "unknown";

/** Only allowlisted, bounded fields from one observed child event. */
export interface DelegationTraceEntry {
  kind: string;
  text?: string;
  detail?: string;
  toolName?: string;
  isError?: boolean;
  cause?: string;
}

export interface DelegationTrace {
  entries: DelegationTraceEntry[];
  omitted: number;
}

interface ActivityBase {
  key: string;
  sessionId: string;
  runId: string;
  parentCallId: string;
  startObserved: boolean;
  historyIncomplete: boolean;
  state: DelegationState;
  stop?: string;
}

export interface SubagentActivity extends ActivityBase {
  family: "subagent";
  childId: string;
  goal?: string;
  background?: boolean;
  toolCount?: number;
  currentTool?: string;
  cause?: string;
  durationMs?: string;
  trace: DelegationTrace;
}

export interface ParallelBranchActivity {
  key: string;
  branchIndex: number;
  startObserved: boolean;
  historyIncomplete: boolean;
  state: DelegationState;
  childId?: string;
  label?: string;
  goal?: string;
  toolCount?: number;
  currentTool?: string;
  failed?: boolean;
  stop?: string;
  durationMs?: string;
  trace: DelegationTrace;
}

export interface ParallelGroupActivity extends ActivityBase {
  family: "parallel";
  join?: string;
  branchCount?: number;
  winner?: number;
  branches: ParallelBranchActivity[];
}

export interface TeamTaskActivity {
  id: string;
  description: string;
  state: string;
  assignee: string;
  deps: string[];
}

export interface TeamFindingActivity {
  member: string;
  body: string;
}

export interface TeamMemberActivity {
  key: string;
  name: string;
  role?: string;
  lead?: boolean;
  mutating?: boolean;
  state: DelegationState;
  roundState?: "running" | "completed" | "failed";
  currentTool?: string;
  cause?: string;
  disposition?: "done" | "stopped";
  reason?: "error" | "cancelled" | "budget";
  errorRounds?: number;
  trace: DelegationTrace;
}

export interface TeamActivity extends ActivityBase {
  family: "team";
  teamId: string;
  members: TeamMemberActivity[];
  tasks: TeamTaskActivity[];
  findings: TeamFindingActivity[];
  rounds?: number;
}

/** One session's in-memory activity. `seenThroughByRun` is a decimal cursor, not an SDK type. */
export interface DelegationFleet {
  sessionId: string;
  subagents: SubagentActivity[];
  parallelGroups: ParallelGroupActivity[];
  teams: TeamActivity[];
  incompleteHistory: boolean;
  seenThroughByRun: Record<string, string>;
}

export function createDelegationFleet(sessionId: string): DelegationFleet {
  return {
    sessionId,
    subagents: [],
    parallelGroups: [],
    teams: [],
    incompleteHistory: false,
    seenThroughByRun: {},
  };
}

/** Mark a stream that cannot be followed further (gap, spent budget, or premature close). */
export function markDelegationHistoryIncomplete(fleet: DelegationFleet): DelegationFleet {
  return { ...markUnknown(fleet), incompleteHistory: true };
}

/** Mark only one run after its stream closes before all delegation terminals arrive. */
export function markDelegationRunUnfollowed(
  fleet: DelegationFleet,
  runId: string,
): DelegationFleet {
  if (!runId) return fleet;
  return { ...markUnknown(fleet, runId), incompleteHistory: true };
}

/** Fold one Studio BFF delivery into a session-owned, family-specific projection. */
export function applyDelegationDelivery(
  fleet: DelegationFleet,
  delivery: RunStreamEvent,
): DelegationFleet {
  if (delivery.type === "run.started") {
    // A repeated start on reattach is a no-op. A different session owns a different fleet.
    return fleet;
  }
  if (delivery.type === "run.truncated") {
    return delivery.reason === "gap" ? markDelegationHistoryIncomplete(fleet) : fleet;
  }
  if (delivery.type === "run.error") return markDelegationHistoryIncomplete(fleet);

  const { event } = delivery;
  if (!event.runId || !isDecimal(event.seq)) return fleet;
  const last = fleet.seenThroughByRun[event.runId];
  if (last !== undefined && compareDecimal(event.seq, last) <= 0) return fleet;
  const next: DelegationFleet = {
    ...fleet,
    seenThroughByRun: { ...fleet.seenThroughByRun, [event.runId]: event.seq },
  };
  if (event.unknown) return next;
  if (event.kind === "result") return markRunOutcomeUnknown(next, event.runId);
  const payload = record(event.payload);
  if (!payload) return next;

  if (
    event.kind === "subagent.start" ||
    event.kind === "subagent.tool" ||
    event.kind === "subagent.end"
  ) {
    return applySubagent(next, event.runId, event.kind, payload);
  }
  if (
    event.kind === "parallel.start" ||
    event.kind === "parallel.branch" ||
    event.kind === "parallel.end"
  ) {
    return applyParallel(next, event.runId, event.kind, payload);
  }
  if (
    event.kind === "team.start" ||
    event.kind === "team.member" ||
    event.kind === "team.tasks" ||
    event.kind === "team.findings" ||
    event.kind === "team.end"
  ) {
    return applyTeam(next, event.runId, event.kind, payload);
  }
  return next;
}

function applySubagent(
  fleet: DelegationFleet,
  runId: string,
  kind: "subagent.start" | "subagent.tool" | "subagent.end",
  payload: Record<string, unknown>,
): DelegationFleet {
  const parentCallId = nonempty(payload.parentCallId);
  const childId = nonempty(payload.childId);
  if (!parentCallId || !childId) return fleet;
  const key = activityKey(fleet.sessionId, runId, "subagent", parentCallId, childId);
  const previous = fleet.subagents.find((entry) => entry.key === key);
  let entry: SubagentActivity = previous ?? {
    key,
    family: "subagent",
    sessionId: fleet.sessionId,
    runId,
    parentCallId,
    childId,
    startObserved: false,
    historyIncomplete: true,
    state: "running",
    trace: emptyTrace(),
  };
  if (kind === "subagent.start") {
    entry = {
      ...entry,
      startObserved: true,
      historyIncomplete: false,
      goal: nonempty(payload.goal),
      background: booleanValue(payload.background),
    };
  } else if (kind === "subagent.tool") {
    entry = {
      ...entry,
      toolCount: nonnegativeInteger(payload.toolCount) ?? entry.toolCount,
      currentTool: nonempty(payload.toolName) ?? entry.currentTool,
      trace: appendTrace(entry.trace, payload),
    };
  } else {
    entry = {
      ...entry,
      state: "finished",
      stop: nonempty(payload.stop),
      cause: nonempty(payload.cause),
      durationMs: decimalValue(payload.durationMs),
      toolCount: nonnegativeInteger(payload.toolCount) ?? entry.toolCount,
    };
  }
  return { ...fleet, subagents: upsert(fleet.subagents, entry) };
}

function applyParallel(
  fleet: DelegationFleet,
  runId: string,
  kind: "parallel.start" | "parallel.branch" | "parallel.end",
  payload: Record<string, unknown>,
): DelegationFleet {
  const parentCallId = nonempty(payload.parentCallId);
  if (!parentCallId) return fleet;
  const key = activityKey(fleet.sessionId, runId, "parallel", parentCallId);
  const previous = fleet.parallelGroups.find((entry) => entry.key === key);
  let group: ParallelGroupActivity = previous ?? {
    key,
    family: "parallel",
    sessionId: fleet.sessionId,
    runId,
    parentCallId,
    startObserved: false,
    historyIncomplete: true,
    state: "running",
    branches: [],
  };
  if (kind === "parallel.start") {
    group = {
      ...group,
      startObserved: true,
      historyIncomplete: false,
      join: nonempty(payload.join),
      branchCount: nonnegativeInteger(payload.branchCount),
    };
  } else if (kind === "parallel.end") {
    const join = nonempty(payload.join) ?? group.join;
    const branchCount = nonnegativeInteger(payload.branchCount) ?? group.branchCount;
    const declaredWinner = nonnegativeInteger(payload.winner);
    group = {
      ...group,
      state: "finished",
      stop: nonempty(payload.stop),
      join,
      branchCount,
      winner:
        join === "all" ||
        declaredWinner === undefined ||
        (branchCount !== undefined && declaredWinner >= branchCount)
          ? undefined
          : declaredWinner,
      branches: group.branches.map(unknownIfRunning),
    };
  } else {
    const branchIndex = nonnegativeInteger(payload.branchIndex);
    const branchKind = payload.kind;
    if (
      branchIndex === undefined ||
      (branchKind !== "branch_start" && branchKind !== "branch_tool" && branchKind !== "branch_end")
    )
      return fleet;
    const branchKey = activityKey(fleet.sessionId, runId, "parallel", parentCallId, branchIndex);
    const priorBranch = group.branches.find((branch) => branch.key === branchKey);
    let branch: ParallelBranchActivity = priorBranch ?? {
      key: branchKey,
      branchIndex,
      startObserved: false,
      historyIncomplete: true,
      state: "running",
      trace: emptyTrace(),
    };
    if (branchKind === "branch_start") {
      branch = {
        ...branch,
        startObserved: true,
        historyIncomplete: false,
        childId: nonempty(payload.childId),
        label: nonempty(payload.branchLabel),
        goal: nonempty(payload.goal),
      };
    } else if (branchKind === "branch_tool") {
      branch = {
        ...branch,
        toolCount: nonnegativeInteger(payload.toolCount) ?? branch.toolCount,
        currentTool: nonempty(payload.toolName) ?? branch.currentTool,
        trace: appendTrace(branch.trace, payload),
      };
    } else {
      branch = {
        ...branch,
        state: "finished",
        failed: booleanValue(payload.failed),
        stop: nonempty(payload.stop),
        durationMs: decimalValue(payload.durationMs),
        toolCount: nonnegativeInteger(payload.toolCount) ?? branch.toolCount,
      };
    }
    group = {
      ...group,
      branches: upsert(group.branches, branch).sort((a, b) => a.branchIndex - b.branchIndex),
    };
  }
  return { ...fleet, parallelGroups: upsert(fleet.parallelGroups, group) };
}

function applyTeam(
  fleet: DelegationFleet,
  runId: string,
  kind: "team.start" | "team.member" | "team.tasks" | "team.findings" | "team.end",
  payload: Record<string, unknown>,
): DelegationFleet {
  const parentCallId = nonempty(payload.parentCallId);
  const teamId = nonempty(payload.teamId);
  if (!parentCallId || !teamId) return fleet;
  const key = activityKey(fleet.sessionId, runId, "team", parentCallId, teamId);
  const previous = fleet.teams.find((entry) => entry.key === key);
  let team: TeamActivity = previous ?? {
    key,
    family: "team",
    sessionId: fleet.sessionId,
    runId,
    parentCallId,
    teamId,
    startObserved: false,
    historyIncomplete: true,
    state: "running",
    members: [],
    tasks: [],
    findings: [],
  };
  if (kind === "team.start") {
    const roster = parseRoster(payload.roster);
    if (!roster) return fleet;
    const members = roster.map((spec) => {
      const memberKey = activityKey(
        fleet.sessionId,
        runId,
        "team",
        parentCallId,
        teamId,
        spec.name,
      );
      return {
        ...emptyMember(memberKey, spec.name),
        ...team.members.find((member) => member.key === memberKey),
        role: spec.role,
        lead: spec.lead,
        mutating: spec.mutating,
      };
    });
    team = { ...team, startObserved: true, historyIncomplete: false, members };
  } else if (kind === "team.member") {
    const name = nonempty(payload.member);
    if (!name) return fleet;
    const memberKey = activityKey(fleet.sessionId, runId, "team", parentCallId, teamId, name);
    const previousMember = team.members.find((member) => member.key === memberKey);
    const innerKind = payload.innerKind;
    const isResult = innerKind === "result";
    const cause = isResult ? nonempty(payload.cause) : undefined;
    const member: TeamMemberActivity = {
      ...(previousMember ?? emptyMember(memberKey, name)),
      roundState: isResult ? (cause ? "failed" : "completed") : "running",
      currentTool: nonempty(payload.toolName) ?? previousMember?.currentTool,
      cause: cause ?? previousMember?.cause,
      trace: appendTrace(previousMember?.trace ?? emptyTrace(), payload),
    };
    team = { ...team, members: upsert(team.members, member) };
  } else if (kind === "team.tasks") {
    const tasks = parseTasks(payload.tasks);
    if (!tasks) return fleet;
    team = { ...team, tasks };
  } else if (kind === "team.findings") {
    const findings = parseFindings(payload.findings);
    if (!findings) return fleet;
    team = { ...team, findings };
  } else {
    const tasks = parseTasks(payload.tasks);
    const findings = parseFindings(payload.findings);
    const dispositions = parseDispositions(payload.dispositions);
    if (!tasks || !findings || !dispositions) return fleet;
    let members = team.members;
    for (const disposition of dispositions) {
      const memberKey = activityKey(
        fleet.sessionId,
        runId,
        "team",
        parentCallId,
        teamId,
        disposition.name,
      );
      const priorMember = members.find((member) => member.key === memberKey);
      members = upsert(members, {
        ...(priorMember ?? emptyMember(memberKey, disposition.name)),
        state: "finished",
        disposition: disposition.stopped ? "stopped" : "done",
        reason: disposition.stopped ? reasonName(disposition.reason) : undefined,
        errorRounds: disposition.errorRounds,
      });
    }
    team = {
      ...team,
      state: "finished",
      stop: nonempty(payload.stop),
      rounds: nonnegativeInteger(payload.rounds),
      tasks,
      findings,
      members: members.map((member) =>
        member.state === "running" ? { ...member, state: "unknown" } : member,
      ),
    };
  }
  return { ...fleet, teams: upsert(fleet.teams, team) };
}

function parseRoster(
  value: unknown,
): Array<{ name: string; role?: string; lead?: boolean; mutating?: boolean }> | undefined {
  if (!Array.isArray(value)) return undefined;
  const roster = value.map((item) => {
    const spec = record(item);
    const name = spec && nonempty(spec.name);
    if (!spec || !name) return undefined;
    return {
      name,
      role: nonempty(spec.role),
      lead: booleanValue(spec.lead),
      mutating: booleanValue(spec.mutating),
    };
  });
  return roster.every((item) => item !== undefined)
    ? (roster as NonNullable<(typeof roster)[number]>[])
    : undefined;
}

function parseTasks(value: unknown): TeamTaskActivity[] | undefined {
  if (!Array.isArray(value)) return undefined;
  const tasks = value.map((item) => {
    const task = record(item);
    const id = task && nonempty(task.id);
    if (
      !task ||
      !id ||
      !Array.isArray(task.deps) ||
      !task.deps.every((dep) => typeof dep === "string")
    )
      return undefined;
    return {
      id,
      description: stringValue(task.description) ?? "",
      state: stringValue(task.state) ?? "",
      assignee: stringValue(task.assignee) ?? "",
      deps: task.deps,
    };
  });
  return tasks.every((item) => item !== undefined) ? (tasks as TeamTaskActivity[]) : undefined;
}

function parseFindings(value: unknown): TeamFindingActivity[] | undefined {
  if (!Array.isArray(value)) return undefined;
  const findings = value.map((item) => {
    const finding = record(item);
    if (!finding || typeof finding.member !== "string" || typeof finding.body !== "string")
      return undefined;
    return { member: finding.member, body: finding.body };
  });
  return findings.every((item) => item !== undefined)
    ? (findings as TeamFindingActivity[])
    : undefined;
}

function parseDispositions(
  value: unknown,
): Array<{ name: string; stopped: boolean; reason: number; errorRounds: number }> | undefined {
  if (!Array.isArray(value)) return undefined;
  const dispositions = value.map((item) => {
    const disposition = record(item);
    const name = disposition && nonempty(disposition.name);
    if (
      !disposition ||
      !name ||
      typeof disposition.stopped !== "boolean" ||
      !Number.isInteger(disposition.reason) ||
      !Number.isInteger(disposition.errorRounds) ||
      (disposition.errorRounds as number) < 0
    )
      return undefined;
    return {
      name,
      stopped: disposition.stopped,
      reason: disposition.reason as number,
      errorRounds: disposition.errorRounds as number,
    };
  });
  return dispositions.every((item) => item !== undefined)
    ? (dispositions as NonNullable<(typeof dispositions)[number]>[])
    : undefined;
}

function reasonName(reason: number): TeamMemberActivity["reason"] {
  if (reason === 1) return "error";
  if (reason === 2) return "cancelled";
  if (reason === 3) return "budget";
  return undefined;
}

function emptyMember(key: string, name: string): TeamMemberActivity {
  return { key, name, state: "running", trace: emptyTrace() };
}

function emptyTrace(): DelegationTrace {
  return { entries: [], omitted: 0 };
}

function appendTrace(trace: DelegationTrace, payload: Record<string, unknown>): DelegationTrace {
  const kind = nonempty(payload.innerKind);
  if (
    kind !== "message.delta" &&
    kind !== "tool.call" &&
    kind !== "tool.result" &&
    kind !== "result"
  )
    return trace;
  const text = preview(payload.text);
  const detail = preview(payload.detail);
  const toolName = nonempty(payload.toolName);
  const isError = booleanValue(payload.isError);
  const cause = kind === "result" ? nonempty(payload.cause) : undefined;
  if (!text && !detail && !toolName && !cause && isError === undefined) return trace;
  const last = trace.entries.at(-1);
  if (kind === "message.delta" && text && last?.kind === "message.delta") {
    return {
      ...trace,
      entries: [
        ...trace.entries.slice(0, -1),
        { ...last, text: last.text?.endsWith("…") ? last.text : cap((last.text ?? "") + text) },
      ],
    };
  }
  const entries = [
    ...trace.entries,
    {
      kind,
      ...(text ? { text } : {}),
      ...(detail ? { detail } : {}),
      ...(toolName ? { toolName } : {}),
      ...(isError !== undefined ? { isError } : {}),
      ...(cause ? { cause } : {}),
    },
  ];
  if (entries.length <= TRACE_LIMIT) return { ...trace, entries };
  return {
    entries: entries.slice(-TRACE_LIMIT),
    omitted: trace.omitted + entries.length - TRACE_LIMIT,
  };
}

function markRunOutcomeUnknown(fleet: DelegationFleet, runId: string): DelegationFleet {
  return markUnknown(fleet, runId);
}

/** A gap covers every run; a result or lost follower changes only its own run. */
function markUnknown(fleet: DelegationFleet, runId?: string): DelegationFleet {
  const matches = (entry: { runId: string }) => runId === undefined || entry.runId === runId;
  return {
    ...fleet,
    subagents: fleet.subagents.map((entry) => (matches(entry) ? unknownIfRunning(entry) : entry)),
    parallelGroups: fleet.parallelGroups.map((group) =>
      matches(group)
        ? { ...unknownIfRunning(group), branches: group.branches.map(unknownIfRunning) }
        : group,
    ),
    teams: fleet.teams.map((team) =>
      matches(team)
        ? { ...unknownIfRunning(team), members: team.members.map(unknownIfRunning) }
        : team,
    ),
  };
}

function unknownIfRunning<T extends { state: DelegationState }>(entry: T): T {
  return entry.state === "running" ? { ...entry, state: "unknown" } : entry;
}

function upsert<T extends { key: string }>(items: T[], item: T): T[] {
  const index = items.findIndex((existing) => existing.key === item.key);
  return index < 0
    ? [...items, item]
    : items.map((existing, current) => (current === index ? item : existing));
}

function activityKey(...parts: Array<string | number>): string {
  return JSON.stringify(parts);
}

function record(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

function nonempty(value: unknown): string | undefined {
  return typeof value === "string" && value.length > 0 ? value : undefined;
}

function preview(value: unknown): string | undefined {
  return typeof value === "string" && value.length > 0 ? cap(value) : undefined;
}

function cap(value: string): string {
  const codePoints = Array.from(value);
  return codePoints.length > PREVIEW_LIMIT
    ? `${codePoints.slice(0, PREVIEW_LIMIT - 1).join("")}…`
    : value;
}

function stringValue(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function booleanValue(value: unknown): boolean | undefined {
  return typeof value === "boolean" ? value : undefined;
}

function nonnegativeInteger(value: unknown): number | undefined {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : undefined;
}

function decimalValue(value: unknown): string | undefined {
  return typeof value === "string" && isDecimal(value) ? value : undefined;
}

function isDecimal(value: string): boolean {
  return /^(0|[1-9][0-9]*)$/.test(value);
}

function compareDecimal(left: string, right: string): number {
  if (left.length !== right.length) return left.length - right.length;
  return left < right ? -1 : left > right ? 1 : 0;
}
