// SPDX-License-Identifier: Apache-2.0

import type { ChatMessage } from "./chat-state";
import type { DelegationActivity } from "./delegation-card";
import type { DelegationFleet } from "./delegation-fleet";

/** The observed assistant turn is trusted only while its local message ID exists. */
export interface DelegationAnchor {
  assistantId: string;
}

export function placeDelegationCards(
  messages: ChatMessage[],
  fleet: DelegationFleet,
  anchors: Record<string, DelegationAnchor>,
): { byMessageId: Record<string, DelegationActivity[]>; unanchored: DelegationActivity[] } {
  const byMessageId: Record<string, DelegationActivity[]> = {};
  const unanchored: DelegationActivity[] = [];
  const assistants = messages.filter((message) => message.role === "assistant");
  const activities: DelegationActivity[] = [
    ...fleet.subagents,
    ...fleet.parallelGroups,
    ...fleet.teams,
  ];
  for (const activity of activities) {
    const observedAnchors = [
      anchors[activity.key],
      anchors[
        JSON.stringify([activity.sessionId, activity.runId, activity.family, activity.parentCallId])
      ],
    ];
    const observed = observedAnchors
      .map((anchor) => assistants.find((candidate) => candidate.id === anchor?.assistantId))
      .find((candidate) => candidate !== undefined);
    const toolMatches = assistants.filter((candidate) =>
      candidate.tools?.some((tool) => tool.id === activity.parentCallId),
    );
    const message = observed ?? (toolMatches.length === 1 ? toolMatches[0] : undefined);
    if (!message) {
      unanchored.push(activity);
      continue;
    }
    const row = byMessageId[message.id] ?? [];
    row.push(activity);
    byMessageId[message.id] = row;
  }
  return { byMessageId, unanchored };
}
