import type { SessionRelationshipInfo } from "@/lib/protocol/sessions";

/**
 * The session inventory's kind taxonomy, as the sidebar tabs and the global
 * search read it. The daemon stamps every row with a closed `kind`
 * (engine/session/kind.go: main | subagent | parallel_branch | team_member |
 * scheduled | debug | unknown) and, when it advertises the
 * `session_activity_inventory` feature, a content-free `activity_state`
 * ("draft" for a chat whose history holds no exchange yet, "active"
 * otherwise). This module only maps those vocabularies onto UI groupings and
 * labels; it never re-derives eligibility (Studio rule 9).
 */

/** The inventory kinds a row can belong to (the TUI's /sessions tabs). */
export type SessionTab = "chats" | "runs" | "scheduled" | "drafts" | "other";

/** The fields a row needs for tab placement (both `SessionSummary` and
 *  `AgentSession` satisfy it). */
export interface SessionTabInput {
  kind?: string;
  /** Omitted reads as a chat: only the decoder ever sets false. */
  isChat?: boolean;
  activityState?: string;
}

const RUN_KINDS = new Set(["subagent", "parallel_branch", "team_member"]);

/**
 * Which tab a row belongs to. A chat (incl. an AI-debug session) is a Chat
 * unless the daemon classified its history as a draft AND the Drafts tab is
 * offered (`draftsEnabled`, the `session_activity_inventory` feature gate);
 * the three child families are Runs; scheduler fires are Scheduled; anything
 * else the daemon refuses as a public chat — an unknown kind, a row an older
 * daemon left kind-less — is Other, so no stored session is ever hidden.
 */
export function sessionTabFor(
  row: SessionTabInput,
  draftsEnabled = true,
): SessionTab {
  const isChat = row.isChat ?? true;
  if (isChat) {
    return draftsEnabled && row.activityState === "draft" ? "drafts" : "chats";
  }
  const kind = row.kind ?? "";
  if (RUN_KINDS.has(kind)) return "runs";
  if (kind === "scheduled") return "scheduled";
  return "other";
}

/** Human label for a row's kind. */
export function sessionKindLabel(kind: string | undefined): string {
  switch (kind) {
    case "main":
      return "Chat";
    case "subagent":
      return "Subagent";
    case "parallel_branch":
      return "Parallel branch";
    case "team_member":
      return "Team member";
    case "scheduled":
      return "Scheduled run";
    case "debug":
      return "Debug session";
    case "":
    case undefined:
      return "Unknown kind";
    default:
      return humanize(kind);
  }
}

/**
 * Plain wording for the daemon's closed capability-reason codes
 * (internal/adapter/server/service.go CapabilityReason*). An unlisted code is
 * shown humanized rather than swallowed, so a newer daemon's reason still
 * reaches the operator; an empty reason yields "".
 */
export function capabilityReasonLabel(reason: string | undefined): string {
  switch (reason) {
    case undefined:
    case "":
      return "";
    case "inspect_only_kind":
      return "Read-only run";
    case "awaiting_approval":
      return "Waiting for approval";
    case "active_elsewhere":
      return "Running in another client";
    case "transcript_unavailable":
      return "Transcript unavailable";
    case "storage_unsupported":
      return "Store cannot delete";
    default:
      return humanize(reason);
  }
}

/**
 * One line naming what a related session IS, from the relationship the
 * daemon validated for its kind: a subagent names its parent and call, a
 * parallel branch its index and parent, a team member its team and roster
 * name, a scheduler fire its schedule, a carryover its origin. "" when the
 * row carries no relationship (a plain chat).
 */
export function describeRelationship(
  rel: SessionRelationshipInfo | undefined,
): string {
  if (!rel) return "";
  if (rel.scheduleName) return `Fire of schedule ${rel.scheduleName}`;
  if (rel.teamId || rel.memberName) {
    const parts = [
      rel.teamId ? `Team ${rel.teamId}` : "Team",
      rel.memberName ? `member ${rel.memberName}` : "",
    ].filter(Boolean);
    return parts.join(" · ");
  }
  if (rel.branchIndex !== null && rel.branchIndex !== undefined) {
    return rel.parentSessionId
      ? `Branch #${rel.branchIndex} of ${rel.parentSessionId}`
      : `Branch #${rel.branchIndex}`;
  }
  if (rel.parentSessionId) {
    return rel.callId
      ? `Subagent of ${rel.parentSessionId} · call ${rel.callId}`
      : `Subagent of ${rel.parentSessionId}`;
  }
  if (rel.originSessionId) return `Forked from ${rel.originSessionId}`;
  return "";
}

/** The row's display label: its title, else what it is, else a plain floor. */
export function inspectRowTitle(row: {
  title?: string;
  relationship?: SessionRelationshipInfo;
}): string {
  return row.title || describeRelationship(row.relationship) || "Untitled run";
}

/**
 * The deep link the global search hands out for a run: the parent chat with
 * `?inspect=<id>` (the chat page opens the read-only transcript on mount);
 * a run with no parent (a scheduler fire) opens on the draft route.
 */
export function runInspectHref(row: {
  id: string;
  relationship?: SessionRelationshipInfo;
}): string {
  const parent = row.relationship?.parentSessionId ?? "";
  const base = parent
    ? `/workspace/chat/${encodeURIComponent(parent)}`
    : "/workspace/chat";
  return `${base}?inspect=${encodeURIComponent(row.id)}`;
}

/** The relationship's ids and names, for search keywords. */
export function relationshipTerms(
  rel: SessionRelationshipInfo | undefined,
): string[] {
  if (!rel) return [];
  return [
    rel.parentSessionId,
    rel.callId,
    rel.scheduleName,
    rel.originSessionId,
    rel.teamId,
    rel.memberName,
  ].filter((term): term is string => Boolean(term));
}

function humanize(code: string): string {
  const spaced = code.replace(/[_-]+/g, " ").trim();
  return spaced ? spaced[0].toUpperCase() + spaced.slice(1) : "";
}
