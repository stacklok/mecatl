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
