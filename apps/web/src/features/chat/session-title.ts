// SPDX-License-Identifier: Apache-2.0

export interface SessionTitleRevision {
  readonly id: string;
  readonly title: string;
  readonly titleProvenance: string;
  readonly titleRevision: string;
}

export function adoptSessionTitle(
  current: SessionTitleRevision | undefined,
  candidate: SessionTitleRevision,
): SessionTitleRevision | undefined {
  if (!candidate.title.trim() || !/^(0|[1-9]\d*)$/.test(candidate.titleRevision)) {
    return current;
  }
  if (current === undefined) return candidate;
  if (current.id !== candidate.id) return current;
  if (BigInt(candidate.titleRevision) > BigInt(current.titleRevision)) return candidate;
  return current;
}

/** The BFF forwards the JSON-safe SDK title payload within the existing run.event. */
export function sessionTitleFromEvent(
  sessionId: string,
  payload: unknown,
): SessionTitleRevision | undefined {
  if (typeof payload !== "object" || payload === null) return undefined;
  if (!("title" in payload) || !("revision" in payload)) return undefined;
  if (typeof payload.title !== "string" || typeof payload.revision !== "string") {
    return undefined;
  }
  return {
    id: sessionId,
    title: payload.title,
    titleProvenance:
      "provenance" in payload && typeof payload.provenance === "string" ? payload.provenance : "",
    titleRevision: payload.revision,
  };
}
