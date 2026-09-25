// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type { SessionTranscriptResponse } from "@mecatl-studio/contracts";
import { afterEach, describe, expect, it } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import { messagesFromTranscript } from "./chat-state";
import {
  parseThreadMap,
  readThreadAssociations,
  recordedRootForMessage,
  registerThreadSession,
  relinkLegacyThread,
  threadKeyForMessage,
  threadKeyForRoot,
  threadTitleFromRoot,
} from "./thread-map";

afterEach(() => {
  clearUserScopedStorage();
  window.localStorage.clear();
});

function transcript(texts: string[]): SessionTranscriptResponse {
  return {
    complete: true,
    messages: texts.map((text, index) => ({
      images: [],
      role: index % 2 === 0 ? "user" : "assistant",
      text,
      toolCalls: [],
    })),
    sessionId: "chat-a",
  };
}

async function rootAt(ordinal: number, saved: SessionTranscriptResponse) {
  const row = saved.messages[ordinal];
  if (!row) throw new Error("Missing saved row");
  const root = await recordedRootForMessage(
    {
      content: row.text,
      id: `transcript-${ordinal}`,
      recordedOrdinal: ordinal,
      role: row.role,
    },
    saved,
  );
  if (!root) throw new Error("Missing recorded root");
  return root;
}

describe("thread associations", () => {
  it("keeps equal text messages in distinct forked threads", async () => {
    const saved = transcript(["same", "answer", "same", "later turn"]);
    const first = await rootAt(0, saved);
    const second = await rootAt(2, saved);
    expect(first.digest).toBe("09acbfdfe88c23c9357b0a33a6d12e21cca122026ed0b0d709304d49e1be7ddc");
    expect(threadKeyForRoot(first)).not.toBe(threadKeyForRoot(second));
    registerThreadSession("chat-a", threadKeyForRoot(first), "fork-a");
    registerThreadSession("chat-a", threadKeyForRoot(second), "fork-b");
    expect(parseThreadMap(window.localStorage.getItem("studio.chat.threads.chat-a"))).toEqual({
      version: 2,
      entries: [
        { ...first, sessionId: "fork-a" },
        { ...second, sessionId: "fork-b" },
      ],
    });
    const reloaded = await readThreadAssociations("chat-a", saved);
    expect(reloaded.byKey[threadKeyForRoot(first)]?.sessionId).toBe("fork-a");
    expect(reloaded.byKey[threadKeyForRoot(second)]?.sessionId).toBe("fork-b");
    const changed = transcript(["same", "answer", "changed", "later turn"]);
    expect(
      (await readThreadAssociations("chat-a", changed)).byKey[threadKeyForRoot(second)],
    ).toBeUndefined();
    expect(
      await recordedRootForMessage({ content: "same", id: "live", role: "user" }, saved),
    ).toBeUndefined();
    expect(
      await recordedRootForMessage(
        {
          content: "changed",
          id: "transcript-0",
          recordedOrdinal: 0,
          role: "user",
        },
        saved,
      ),
    ).toBeUndefined();
  });

  it("keeps ambiguous legacy roots available until explicit relinking", async () => {
    const saved = transcript(["same", "answer", "same"]);
    const legacyKey = threadKeyForMessage({ content: "same", role: "user" });
    window.localStorage.setItem(
      "studio.chat.threads.chat-a",
      JSON.stringify({
        [legacyKey]: { sessionId: "older-thread" },
      }),
    );
    const first = await rootAt(0, saved);
    const second = await rootAt(2, saved);
    const unresolved = await readThreadAssociations("chat-a", saved);
    expect(unresolved.byKey).toEqual({});
    expect(unresolved.legacyCandidates[threadKeyForRoot(first)]).toEqual({
      legacyKey,
      sessionId: "older-thread",
    });
    expect(unresolved.legacyCandidates[threadKeyForRoot(second)]?.sessionId).toBe("older-thread");
    expect(parseThreadMap(window.localStorage.getItem("studio.chat.threads.chat-a"))).toEqual({
      entries: [],
      version: 2,
    });
    registerThreadSession("chat-a", threadKeyForRoot(second), "new-thread");
    expect(
      (await readThreadAssociations("chat-a", saved)).legacyCandidates[threadKeyForRoot(first)]
        ?.sessionId,
    ).toBe("older-thread");
    expect(await relinkLegacyThread("chat-a", legacyKey, threadKeyForRoot(first), saved)).toBe(
      true,
    );
    const linked = await readThreadAssociations("chat-a", saved);
    expect(linked.byKey[threadKeyForRoot(first)]?.sessionId).toBe("older-thread");
    expect(linked.byKey[threadKeyForRoot(second)]?.sessionId).toBe("new-thread");
    expect(linked.legacyCandidates).toEqual({});
  });

  it("migrates a legacy root only for one recorded match", async () => {
    const saved = transcript(["unique", "answer"]);
    const legacyKey = threadKeyForMessage({ content: "unique", role: "user" });
    window.localStorage.setItem(
      "studio.chat.threads.chat-a",
      JSON.stringify({
        [legacyKey]: { sessionId: "older-thread" },
      }),
    );
    const root = await rootAt(0, saved);
    const resolved = await readThreadAssociations("chat-a", saved);
    expect(resolved.byKey[threadKeyForRoot(root)]?.sessionId).toBe("older-thread");
    expect(resolved.legacyCandidates).toEqual({});
    expect(parseThreadMap(window.localStorage.getItem("studio.chat.threads.chat-a"))).toEqual({
      entries: [{ ...root, sessionId: "older-thread" }],
      version: 2,
    });
  });

  it("uses the authoritative entry index when tool results have no visible row", async () => {
    const saved: SessionTranscriptResponse = {
      complete: true,
      messages: [
        { images: [], role: "user", text: "question", toolCalls: [] },
        {
          images: [],
          role: "tool",
          text: "",
          toolCalls: [],
          toolResult: { callId: "call-1", content: "done", isError: false },
        },
        { images: [], role: "assistant", text: "answer", toolCalls: [] },
      ],
      sessionId: "chat-a",
    };
    const visible = messagesFromTranscript(saved.messages);
    expect(visible.map((message) => message.recordedOrdinal)).toEqual([0, 2]);
    const root = await recordedRootForMessage(
      visible[1] as NonNullable<(typeof visible)[1]>,
      saved,
    );
    expect(root?.ordinal).toBe(2);
  });

  it("creates a compact thread title", () => {
    expect(threadTitleFromRoot("one\n two")).toBe("Thread: one two");
    expect(threadTitleFromRoot(" ")).toBe("Thread: (empty message)");
    expect(threadTitleFromRoot("a".repeat(80))).toHaveLength(49);
  });
});
