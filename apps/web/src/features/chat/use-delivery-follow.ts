// SPDX-License-Identifier: Apache-2.0

import type { ChatMessage } from "./chat-state";

interface InventoryRow {
  id: string;
  updatedAt: string;
}

export interface DeliveryCheckFacts {
  connected: boolean;
  idle: boolean;
  lastTranscriptCheckAt?: number;
  next?: InventoryRow;
  now?: number;
  previous?: InventoryRow;
  sessionId?: string;
  visible: boolean;
}

/**
 * An advancing inventory timestamp is a prompt to inspect saved content. A
 * bounded safety check also catches writes in the same timestamp second and
 * chats omitted by an incomplete inventory. Neither implies a delivery note.
 */
export function shouldRefreshTranscriptAfterInventory(facts: DeliveryCheckFacts): boolean {
  const { connected, idle, lastTranscriptCheckAt, next, now, previous, sessionId, visible } = facts;
  if (!connected || !visible || !idle || !sessionId) return false;
  if (
    now !== undefined &&
    (lastTranscriptCheckAt === undefined || now - lastTranscriptCheckAt >= 60_000)
  ) {
    return true;
  }
  if (!previous || !next || previous.id !== sessionId || next.id !== sessionId) return false;
  const before = Date.parse(previous.updatedAt);
  const after = Date.parse(next.updatedAt);
  return Number.isFinite(before) && Number.isFinite(after) && after > before;
}

function deliveryKey(message: ChatMessage): string | undefined {
  const note = message.delivery;
  return note ? JSON.stringify([note.fireId, note.kind]) : undefined;
}

function matchKey(message: ChatMessage): string {
  if (message.tools?.length) return `calls:${JSON.stringify(message.tools.map((tool) => tool.id))}`;
  const delivery = deliveryKey(message);
  if (delivery) return `delivery:${delivery}`;
  // This groups occurrence candidates, not durable identities. Render-local IDs,
  // tool output, reasoning, and image names can differ from saved projections.
  return `ordinary:${JSON.stringify([
    message.role,
    message.content,
    message.images?.map(({ mimeType }) => mimeType) ?? [],
  ])}`;
}

function matchesImageContent(live: ChatMessage, saved: ChatMessage): boolean {
  const liveImages = live.images ?? [];
  const savedImages = saved.images ?? [];
  if (liveImages.length !== savedImages.length) return false;
  return liveImages.every((image, index) => {
    const recorded = savedImages[index];
    if (!recorded || image.mimeType !== recorded.mimeType) return false;
    if (image.data !== undefined && recorded.data !== undefined) {
      return image.data === recorded.data;
    }
    if (image.url !== undefined && recorded.url !== undefined) {
      return image.url === recorded.url;
    }
    // With no common source, occurrence is safe only when neither projection
    // carries content to distinguish it from another image in the same group.
    return (
      image.data === undefined &&
      recorded.data === undefined &&
      image.url === undefined &&
      recorded.url === undefined
    );
  });
}

function keepLiveWithRecordedTools(live: ChatMessage, saved: ChatMessage): ChatMessage {
  const content = saved.tools?.length
    ? live.content.startsWith(saved.content) ||
      live.tools?.some((tool) => !saved.tools?.some((recorded) => recorded.id === tool.id))
      ? live.content
      : saved.content
    : saved.content.startsWith(live.content)
      ? saved.content
      : live.content;
  if (!saved.tools?.length) {
    return content === live.content && live.recordedOrdinal === saved.recordedOrdinal
      ? live
      : { ...live, content, recordedOrdinal: saved.recordedOrdinal };
  }
  const liveTools = live.tools ?? [];
  const recordedTools = saved.tools.map((recorded) => {
    const current = liveTools.find((tool) => tool.id === recorded.id);
    if (!current) return recorded;
    if (
      (current.output !== undefined || recorded.output === undefined) &&
      (current.isError !== undefined || recorded.isError === undefined)
    ) {
      return current;
    }
    return {
      ...recorded,
      ...current,
      isError: current.isError ?? recorded.isError,
      output: current.output ?? recorded.output,
    };
  });
  const tools = [
    ...liveTools.map((tool) => recordedTools.find((recorded) => recorded.id === tool.id) ?? tool),
    ...recordedTools.filter((tool) => !liveTools.some((current) => current.id === tool.id)),
  ];
  return tools.length === liveTools.length &&
    tools.every((tool, index) => tool === liveTools[index]) &&
    content === live.content &&
    live.recordedOrdinal === saved.recordedOrdinal
    ? live
    : { ...live, content, recordedOrdinal: saved.recordedOrdinal, tools };
}

/** Rebuild saved order, matching each repeated ordinary row only once. */
export function reconcileRecordedMessages(
  current: ChatMessage[],
  authoritative: readonly ChatMessage[],
): ChatMessage[] {
  // Calls in one saved message prove these live rows belong to the same
  // readable span. Consolidate text only, retaining every live row's metadata.
  const liveMessages = [...current];
  for (const saved of authoritative) {
    if (!saved.tools?.length) continue;
    const indexes = liveMessages.flatMap((message, index) =>
      message.tools?.some((tool) => saved.tools?.some((recorded) => recorded.id === tool.id))
        ? [index]
        : [],
    );
    if (indexes.length < 2) continue;
    const content = indexes.map((index) => liveMessages[index]?.content ?? "").join("");
    for (const [position, index] of indexes.entries()) {
      const message = liveMessages[index];
      const text = position === 0 ? content : "";
      if (message && message.content !== text) liveMessages[index] = { ...message, content: text };
    }
  }
  const liveByKey = new Map<string, { index: number; message: ChatMessage }[]>();
  for (const [index, message] of liveMessages.entries()) {
    const key = matchKey(message);
    const matches = liveByKey.get(key) ?? [];
    matches.push({ index, message });
    liveByKey.set(key, matches);
  }

  const seenDeliveries = new Set<string>();
  const consumedIndexes = new Set<number>();
  const recorded: {
    live?: { index: number; message: ChatMessage };
    saved: ChatMessage;
  }[] = [];
  let textAnchor: (typeof recorded)[number] | undefined;
  for (const saved of authoritative) {
    const priorTextAnchor = textAnchor;
    textAnchor = undefined;
    const delivery = deliveryKey(saved);
    if (delivery) {
      if (seenDeliveries.has(delivery)) continue;
      seenDeliveries.add(delivery);
    }
    const exact = liveByKey.get(matchKey(saved));
    const matchIndex = exact?.findIndex(
      ({ index, message }) => !consumedIndexes.has(index) && matchesImageContent(message, saved),
    );
    // Invocation membership is independent of assistant-message grouping. A live
    // row may cover several saved rows, and a saved row may cover several live rows.
    if (saved.tools?.length) {
      const remaining = new Map(saved.tools.map((tool) => [tool.id, tool]));
      let content = saved.content;
      for (const [index, message] of liveMessages.entries()) {
        const tools = message.tools?.flatMap((tool) => {
          const recorded = remaining.get(tool.id);
          if (!recorded) return [];
          remaining.delete(tool.id);
          return [recorded];
        });
        if (!tools?.length) continue;
        const prior = recorded.find((row) => row.live?.index === index);
        if (prior) {
          prior.saved = {
            ...prior.saved,
            content: prior.saved.content + content,
            tools: [...(prior.saved.tools ?? []), ...tools],
          };
        } else {
          recorded.push({
            live: { index, message },
            saved: { ...saved, tools, content },
          });
        }
        textAnchor ??= prior ?? recorded.at(-1);
        content = "";
        consumedIndexes.add(index);
      }
      if (remaining.size !== saved.tools.length) {
        if (remaining.size) {
          recorded.push({ saved: { ...saved, tools: [...remaining.values()], content: "" } });
          textAnchor = undefined;
        }
        continue;
      }
    }
    // A canonical tool anchor can also cover the following saved text-only
    // segment. Consume it only when the live row actually spans that boundary;
    // adjacency or equal text alone must not join unrelated assistant messages.
    const prefix = priorTextAnchor?.saved.content;
    const liveContent = priorTextAnchor?.live?.message.content;
    if (
      priorTextAnchor &&
      prefix !== undefined &&
      liveContent?.startsWith(prefix) &&
      liveContent.length > prefix.length &&
      saved.role === "assistant" &&
      saved.content &&
      !saved.tools?.length &&
      !delivery &&
      !saved.images?.length
    ) {
      const suffix = liveContent.slice(prefix.length);
      if (suffix.startsWith(saved.content) || saved.content.startsWith(suffix)) {
        priorTextAnchor.saved = {
          ...priorTextAnchor.saved,
          content: prefix + saved.content,
        };
        textAnchor = priorTextAnchor;
        continue;
      }
    }
    let live =
      matchIndex !== undefined && matchIndex >= 0 ? exact?.splice(matchIndex, 1)[0] : undefined;
    if (!live && !saved.tools?.length && saved.role === "assistant" && saved.content) {
      const prior = recorded.at(-1)?.live;
      const candidate = prior?.message.role === "user" ? liveMessages[prior.index + 1] : undefined;
      if (
        prior &&
        candidate?.role === "assistant" &&
        !candidate.tools?.length &&
        candidate.content &&
        saved.content.startsWith(candidate.content) &&
        !candidate.stopReason &&
        !candidate.failure &&
        !consumedIndexes.has(prior.index + 1)
      ) {
        live = { index: prior.index + 1, message: candidate };
      }
    }
    if (live) consumedIndexes.add(live.index);
    recorded.push({ live, saved });
  }
  // Transcript indexes are render-local IDs. Inserting a saved turn can reuse
  // an older row's index, so reserve live anchors before adding saved rows.
  const usedIds = new Set(current.map(({ id }) => id));
  const matchedIndexes = new Set(recorded.flatMap(({ live }) => (live ? [live.index] : [])));
  const savedRows = recorded.map(({ live, saved }, index) => {
    if (live) return keepLiveWithRecordedTools(live.message, saved);
    let id = saved.id;
    if (usedIds.has(id)) {
      id = `${saved.id}-recorded-${index}`;
      for (let suffix = 1; usedIds.has(id); suffix += 1) {
        id = `${saved.id}-recorded-${index}-${suffix}`;
      }
    }
    usedIds.add(id);
    return id === saved.id ? saved : { ...saved, id };
  });
  const merged: ChatMessage[] = [];
  let lastLiveIndex = -1;
  const appendUnmatched = (from: number, until: number) => {
    for (let index = from; index < until; index += 1) {
      if (matchedIndexes.has(index)) continue;
      const message = liveMessages[index];
      if (!message) continue;
      const delivery = deliveryKey(message);
      if (delivery) {
        if (seenDeliveries.has(delivery)) continue;
        seenDeliveries.add(delivery);
      }
      merged.push(message);
    }
  };
  for (const [index, row] of recorded.entries()) {
    if (row.live && row.live.index > lastLiveIndex) {
      appendUnmatched(lastLiveIndex + 1, row.live.index);
      lastLiveIndex = row.live.index;
    }
    const saved = savedRows[index];
    if (saved) merged.push(saved);
  }
  appendUnmatched(lastLiveIndex + 1, current.length);
  return merged.length === current.length &&
    merged.every((message, index) => message === current[index])
    ? current
    : merged;
}
