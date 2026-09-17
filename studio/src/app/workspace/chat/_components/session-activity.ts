/**
 * The sidebar row's activity indicator for a session, from the inventory
 * poll's daemon lifecycle `state` plus the locally-known streaming flag.
 *
 * "awaiting" (the daemon is parked on a permission ask, ADR 0027 Phase 2)
 * wins over "running": a non-selected chat waiting on the operator must be
 * findable from the list, and the selected chat's own streaming flag folds a
 * pending ask into "streaming" — the state is the more specific fact.
 */
export type SessionActivity =
  | { kind: "running"; label: "Running" }
  | { kind: "awaiting"; label: "Awaiting approval" };

export function sessionActivity(session: {
  isStreaming?: boolean;
  state?: string;
}): SessionActivity | null {
  if (session.state === "awaiting") {
    return { kind: "awaiting", label: "Awaiting approval" };
  }
  if (session.isStreaming || session.state === "running") {
    return { kind: "running", label: "Running" };
  }
  return null;
}
