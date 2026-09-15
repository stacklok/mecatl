import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentMessage } from "@/features/agent";
import {
  composeThreadPrompt,
  getThreadSession,
  isThreadSession,
  readThreadMap,
  registerThreadSession,
  sliceThreadReplies,
  stripRootQuote,
  syncThreadActivity,
  threadKeyForMessage,
  threadTitleFromRoot,
} from "./thread-map";

const msg = (
  role: AgentMessage["role"],
  content: string,
  timestamp = 0,
): AgentMessage => ({
  id: `id-${role}-${content.length}`,
  role,
  content,
  timestamp,
});

describe("threadKeyForMessage", () => {
  it("is stable for the same role and content (live vs rehydrated ids differ)", () => {
    const live = { ...msg("assistant", "hello world"), id: "assistant-123" };
    const rehydrated = {
      ...msg("assistant", "hello world"),
      id: "history-assistant-4",
    };
    expect(threadKeyForMessage(live)).toBe(threadKeyForMessage(rehydrated));
  });

  it("distinguishes role and content", () => {
    expect(threadKeyForMessage(msg("user", "same text"))).not.toBe(
      threadKeyForMessage(msg("assistant", "same text")),
    );
    expect(threadKeyForMessage(msg("user", "one"))).not.toBe(
      threadKeyForMessage(msg("user", "two")),
    );
  });
});

describe("threadTitleFromRoot", () => {
  it("prefixes and keeps a short root verbatim", () => {
    expect(threadTitleFromRoot("Fix the login bug")).toBe(
      "Thread: Fix the login bug",
    );
  });

  it("collapses newlines and runs of whitespace into single spaces", () => {
    expect(threadTitleFromRoot("line one\nline   two\n\nthree")).toBe(
      "Thread: line one line two three",
    );
  });

  it("clamps the snippet to ~40 chars with an ellipsis", () => {
    const title = threadTitleFromRoot("a".repeat(100));
    expect(title).toBe(`Thread: ${"a".repeat(40)}…`);
  });

  it("names an empty root honestly", () => {
    expect(threadTitleFromRoot("   ")).toBe("Thread: (empty message)");
  });
});

describe("composeThreadPrompt", () => {
  it("quotes a single-line root above the user's text", () => {
    expect(composeThreadPrompt("the root", "my reply")).toBe(
      "> the root\n\nmy reply",
    );
  });

  it("carries newlines as '> ' continuation lines (blank lines as '>')", () => {
    expect(composeThreadPrompt("first\n\nsecond", "ok")).toBe(
      "> first\n>\n> second\n\nok",
    );
  });

  it("clamps the quoted root at ~500 chars", () => {
    const prompt = composeThreadPrompt("x".repeat(600), "reply");
    expect(prompt).toBe(`> ${"x".repeat(500)}…\n\nreply`);
  });

  it("keeps the user's text verbatim after the blank line", () => {
    const prompt = composeThreadPrompt("root", "multi\nline reply");
    expect(prompt.endsWith("\n\nmulti\nline reply")).toBe(true);
  });
});

describe("sliceThreadReplies", () => {
  const root = "the root message\nwith two lines";
  const quoted = composeThreadPrompt(root, "first thread reply");

  it("returns nothing when the transcript is only seeded parent history", () => {
    const history = [
      msg("user", "parent question"),
      msg("assistant", "parent answer"),
    ];
    expect(sliceThreadReplies(history, root)).toEqual([]);
  });

  it("returns messages from the quoted first reply onward", () => {
    const messages = [
      msg("user", "parent question"),
      msg("assistant", "parent answer"),
      msg("user", quoted),
      msg("assistant", "thread answer"),
    ];
    const replies = sliceThreadReplies(messages, root);
    expect(replies).toHaveLength(2);
    expect(replies[0].content).toBe(quoted);
    expect(replies[1].content).toBe("thread answer");
  });

  it("ignores an assistant message that merely contains the quote block", () => {
    const messages = [msg("assistant", quoted)];
    expect(sliceThreadReplies(messages, root)).toEqual([]);
  });
});

/** This vitest environment ships a method-less localStorage shim (Node's
 *  --localstorage-file stub shadows jsdom's), so storage tests stub a real
 *  in-memory Storage; the global afterEach unstubs it. */
function memoryStorage(): Storage {
  let store = new Map<string, string>();
  return {
    get length() {
      return store.size;
    },
    clear: () => {
      store = new Map();
    },
    getItem: (key: string) => store.get(key) ?? null,
    key: (index: number) => [...store.keys()][index] ?? null,
    removeItem: (key: string) => {
      store.delete(key);
    },
    setItem: (key: string, value: string) => {
      store.set(key, String(value));
    },
  };
}

describe("thread map storage", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("returns null / empty for an unknown parent session", () => {
    expect(getThreadSession("parent-1", "key-1")).toBeNull();
    expect(readThreadMap("parent-1")).toEqual({});
  });

  it("registers a thread session and reads it back with zeroed activity", () => {
    registerThreadSession("parent-1", "key-1", "thread-abc");
    expect(getThreadSession("parent-1", "key-1")).toBe("thread-abc");
    expect(readThreadMap("parent-1")["key-1"]).toEqual({
      sessionId: "thread-abc",
      replyCount: 0,
      lastReplyAt: 0,
    });
  });

  it("keeps parent sessions isolated from each other", () => {
    registerThreadSession("parent-1", "key-1", "thread-abc");
    expect(getThreadSession("parent-2", "key-1")).toBeNull();
  });

  it("syncThreadActivity sets the count and advances lastReplyAt", () => {
    registerThreadSession("parent-1", "key-1", "thread-abc");
    syncThreadActivity("parent-1", "key-1", 3, 1_000);
    expect(readThreadMap("parent-1")["key-1"]).toEqual({
      sessionId: "thread-abc",
      replyCount: 3,
      lastReplyAt: 1_000,
    });
  });

  it("lastReplyAt only advances — a rehydrated 0 never erases a real time", () => {
    registerThreadSession("parent-1", "key-1", "thread-abc");
    syncThreadActivity("parent-1", "key-1", 2, 5_000);
    syncThreadActivity("parent-1", "key-1", 2, 0);
    expect(readThreadMap("parent-1")["key-1"].lastReplyAt).toBe(5_000);
    // The count still follows the authoritative reply list downward or upward.
    syncThreadActivity("parent-1", "key-1", 4, 1_000);
    expect(readThreadMap("parent-1")["key-1"]).toEqual({
      sessionId: "thread-abc",
      replyCount: 4,
      lastReplyAt: 5_000,
    });
  });

  it("syncThreadActivity is a no-op for a key that was never registered", () => {
    syncThreadActivity("parent-1", "ghost", 3, 1_000);
    expect(readThreadMap("parent-1")).toEqual({});
  });

  it("tolerates garbage in localStorage", () => {
    window.localStorage.setItem("mecatl-studio.threads.parent-1", "{not json");
    expect(readThreadMap("parent-1")).toEqual({});
    window.localStorage.setItem(
      "mecatl-studio.threads.parent-2",
      JSON.stringify({
        good: { sessionId: "s", replyCount: 1, lastReplyAt: 2 },
        bad: { replyCount: 9 },
      }),
    );
    expect(readThreadMap("parent-2")).toEqual({
      good: { sessionId: "s", replyCount: 1, lastReplyAt: 2 },
    });
  });
});

describe("thread session registry", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("knows nothing before any thread was registered", () => {
    expect(isThreadSession("session-1")).toBe(false);
  });

  it("registers thread session ids across parents into one flat set", () => {
    registerThreadSession("parent-1", "key-1", "thread-a");
    registerThreadSession("parent-2", "key-9", "thread-b");
    expect(isThreadSession("thread-a")).toBe(true);
    expect(isThreadSession("thread-b")).toBe(true);
    expect(isThreadSession("parent-1")).toBe(false);
    expect(isThreadSession("some-ordinary-chat")).toBe(false);
  });

  it("registering the same thread twice keeps one entry", () => {
    registerThreadSession("parent-1", "key-1", "thread-a");
    registerThreadSession("parent-1", "key-2", "thread-a");
    expect(
      JSON.parse(
        window.localStorage.getItem("mecatl-studio.thread-sessions") ?? "[]",
      ),
    ).toEqual(["thread-a"]);
  });

  it("membership, not the 'Thread: ' title, decides — a user's own chat named that way is not a thread", () => {
    // Nothing registered for this id: even a session titled "Thread: …"
    // must answer false, so the sidebar never hides a user's own chat.
    expect(isThreadSession("chat-the-user-titled-thread")).toBe(false);
  });

  it("also recognizes legacy threads recorded only in a per-parent map", () => {
    // A thread minted before the flat registry existed: present in the
    // parent's map, absent from the registry key.
    window.localStorage.setItem(
      "mecatl-studio.threads.parent-legacy",
      JSON.stringify({
        "key-1": { sessionId: "thread-legacy", replyCount: 2, lastReplyAt: 5 },
      }),
    );
    expect(isThreadSession("thread-legacy")).toBe(true);
  });

  it("tolerates garbage in the registry key", () => {
    window.localStorage.setItem("mecatl-studio.thread-sessions", "{not json");
    expect(isThreadSession("thread-a")).toBe(false);
    window.localStorage.setItem(
      "mecatl-studio.thread-sessions",
      JSON.stringify(["ok", 7, "", null]),
    );
    expect(isThreadSession("ok")).toBe(true);
    expect(isThreadSession("")).toBe(false);
    // A registration on top of the garbage entries keeps only valid ids.
    registerThreadSession("parent-1", "key-1", "thread-new");
    expect(
      JSON.parse(
        window.localStorage.getItem("mecatl-studio.thread-sessions") ?? "[]",
      ),
    ).toEqual(["ok", "thread-new"]);
  });
});

describe("stripRootQuote", () => {
  it("removes the root's own quote from a reply, keeping the words", () => {
    const root = "the original message";
    const composed = composeThreadPrompt(root, "my question");
    const [stripped] = stripRootQuote(
      [{ role: "user", content: composed }],
      root,
    );
    expect(stripped.content).toBe("my question");
  });
  it("keeps quotes of other text and non-user replies", () => {
    const replies = [
      { role: "user", content: "> some other selection\n\nthoughts?" },
      { role: "assistant", content: "> not touched" },
    ];
    expect(stripRootQuote(replies, "the original message")).toEqual(replies);
  });
});
