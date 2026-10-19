// SPDX-License-Identifier: Apache-2.0

/**
 * The inventory's kind and reason vocabularies in plain words, ported from
 * the prototype's `lib/session-kinds.ts`. The daemon stamps every row with a
 * closed `kind` and closed capability-reason codes; this module only labels
 * them. It never re-derives eligibility: the daemon's own capabilities stay
 * the rule.
 */

/** Human label for a row's kind. An unlisted kind is humanized, never hidden. */
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
 * Plain wording for the daemon's closed capability-reason codes. An unlisted
 * code is humanized rather than swallowed, so a newer daemon's reason still
 * reaches the reader; an empty reason yields "".
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

function humanize(code: string): string {
  const spaced = code.replace(/[_-]+/g, " ").trim();
  return spaced ? spaced.charAt(0).toUpperCase() + spaced.slice(1) : "";
}
