import { describe, expect, it, vi } from "vitest";

import {
  type ApprovalContext,
  type ApprovalMessagingClient,
  PermissionApprovalGateway,
} from "../src/approvals.js";

function fakeAsk(
  overrides: Partial<{ args: string; askId: string; reason: string; tool: string }> = {},
) {
  return {
    args: "{}",
    askId: "ask-1",
    reason: "configured ask",
    tool: "Write",
    ...overrides,
  };
}

function fakeClient(): ApprovalMessagingClient & {
  posted: { channel: string; text: string; blocks: unknown[] }[];
  updated: { channel: string; ts: string; text: string; blocks: unknown[] | undefined }[];
} {
  const posted: { channel: string; text: string; blocks: unknown[] }[] = [];
  const updated: { channel: string; ts: string; text: string; blocks: unknown[] | undefined }[] =
    [];
  let nextTs = 0;
  return {
    async openDm(userId) {
      return `dm-${userId}`;
    },
    async postMessage(channel, text, blocks) {
      posted.push({ blocks, channel, text });
      nextTs += 1;
      return `ts-${nextTs}`;
    },
    async updateMessage(channel, ts, text, blocks) {
      updated.push({ blocks, channel, text, ts });
    },
    posted,
    updated,
  };
}

function fakeContext(overrides: Partial<ApprovalContext> = {}): ApprovalContext & {
  statuses: Array<"suspended" | "processing">;
} {
  const statuses: Array<"suspended" | "processing"> = [];
  return {
    authorizedUserId: "U1",
    originLabel: "a DM with the bot",
    setStatus: async (status) => {
      statuses.push(status);
    },
    statuses,
    ...overrides,
  };
}

/** Waits a microtask/timer tick so an ask's fire-and-forget setup (`openDm`/`postMessage`) has
 * settled before the test asserts on it — mirrors how the real SDK responder is invoked
 * fire-and-forget from `RunImpl#startAsk`. */
async function flush(): Promise<void> {
  await new Promise((resolveTick) => setTimeout(resolveTick, 0));
}

describe("PermissionApprovalGateway", () => {
  it("resolves allow_once from a matching click and clears the pending entry", async () => {
    const client = fakeClient();
    const gateway = new PermissionApprovalGateway(client);
    const context = fakeContext();
    const responder = gateway.createResponder(context);

    const controller = new AbortController();
    const verdictPromise = responder(fakeAsk(), controller.signal);
    await flush();

    expect(client.posted).toHaveLength(1);
    expect(context.statuses).toEqual(["suspended"]);

    await gateway.handleAction({
      actionId: "mecatl_approval:allow_once",
      askId: "ask-1",
      clickerUserId: "U1",
    });

    await expect(verdictPromise).resolves.toBe("allow_once");
    expect(context.statuses).toEqual(["suspended", "processing"]);
    expect(client.updated).toEqual([
      {
        blocks: [
          {
            subtitle: { text: "Requested from a DM with the bot", type: "plain_text" },
            title: { text: "Allowed once: Write", type: "plain_text" },
            type: "card",
          },
        ],
        channel: "dm-U1",
        text: "Allowed once.",
        ts: "ts-1",
      },
    ]);
  });

  it("resolves allow_always and deny the same way", async () => {
    const client = fakeClient();
    const gateway = new PermissionApprovalGateway(client);

    const allowAlways = gateway.createResponder(fakeContext());
    const allowAlwaysVerdict = allowAlways(
      fakeAsk({ askId: "ask-allow-always" }),
      new AbortController().signal,
    );
    await flush();
    await gateway.handleAction({
      actionId: "mecatl_approval:allow_always",
      askId: "ask-allow-always",
      clickerUserId: "U1",
    });
    await expect(allowAlwaysVerdict).resolves.toBe("allow_always");

    const deny = gateway.createResponder(fakeContext());
    const denyVerdict = deny(fakeAsk({ askId: "ask-deny" }), new AbortController().signal);
    await flush();
    await gateway.handleAction({
      actionId: "mecatl_approval:deny",
      askId: "ask-deny",
      clickerUserId: "U1",
    });
    await expect(denyVerdict).resolves.toBe("deny");
  });

  it("ignores a click from anyone other than the authorized user", async () => {
    const client = fakeClient();
    const gateway = new PermissionApprovalGateway(client);
    const responder = gateway.createResponder(fakeContext({ authorizedUserId: "U1" }));
    const verdictPromise = responder(fakeAsk(), new AbortController().signal);
    await flush();

    await gateway.handleAction({
      actionId: "mecatl_approval:allow_once",
      askId: "ask-1",
      clickerUserId: "someone-else",
    });

    // Still pending: a follow-up click from the rightful owner still resolves it.
    await gateway.handleAction({
      actionId: "mecatl_approval:deny",
      askId: "ask-1",
      clickerUserId: "U1",
    });
    await expect(verdictPromise).resolves.toBe("deny");
    expect(client.updated).toHaveLength(1); // only the rightful click updated the DM
  });

  it("ignores a duplicate click after the ask is already resolved", async () => {
    const client = fakeClient();
    const gateway = new PermissionApprovalGateway(client);
    const responder = gateway.createResponder(fakeContext());
    const verdictPromise = responder(fakeAsk(), new AbortController().signal);
    await flush();

    await gateway.handleAction({
      actionId: "mecatl_approval:allow_once",
      askId: "ask-1",
      clickerUserId: "U1",
    });
    await expect(verdictPromise).resolves.toBe("allow_once");

    // A second click (double-tap, or a stale button) after resolution: no-op, no second update.
    await gateway.handleAction({
      actionId: "mecatl_approval:deny",
      askId: "ask-1",
      clickerUserId: "U1",
    });
    expect(client.updated).toHaveLength(1);
  });

  it("clears the pending ask and the DM when the signal aborts (run ended/retracted)", async () => {
    const client = fakeClient();
    const gateway = new PermissionApprovalGateway(client);
    const context = fakeContext();
    const responder = gateway.createResponder(context);
    const controller = new AbortController();
    const verdictPromise = responder(fakeAsk(), controller.signal);
    await flush();

    controller.abort();
    await flush();

    await expect(verdictPromise).resolves.toBeUndefined();
    expect(context.statuses).toEqual(["suspended", "processing"]);
    expect(client.updated).toEqual([
      {
        blocks: [
          {
            subtitle: { text: "Requested from a DM with the bot", type: "plain_text" },
            title: { text: "No longer pending: Write", type: "plain_text" },
            type: "card",
          },
        ],
        channel: "dm-U1",
        text: "This request is no longer pending — the run ended or was cancelled.",
        ts: "ts-1",
      },
    ]);

    // A click arriving after the abort has nothing pending to act on.
    await gateway.handleAction({
      actionId: "mecatl_approval:allow_once",
      askId: "ask-1",
      clickerUserId: "U1",
    });
    expect(client.updated).toHaveLength(1);
  });

  it("only suspends on the first of two concurrent asks and resumes after the last resolves", async () => {
    const client = fakeClient();
    const gateway = new PermissionApprovalGateway(client);
    const context = fakeContext();
    const responder = gateway.createResponder(context);

    const first = responder(fakeAsk({ askId: "ask-1" }), new AbortController().signal);
    const second = responder(fakeAsk({ askId: "ask-2" }), new AbortController().signal);
    await flush();

    expect(context.statuses).toEqual(["suspended"]);

    await gateway.handleAction({
      actionId: "mecatl_approval:allow_once",
      askId: "ask-1",
      clickerUserId: "U1",
    });
    await first;
    // ask-2 is still pending — status must not flip back to processing yet.
    expect(context.statuses).toEqual(["suspended"]);

    await gateway.handleAction({
      actionId: "mecatl_approval:deny",
      askId: "ask-2",
      clickerUserId: "U1",
    });
    await second;
    expect(context.statuses).toEqual(["suspended", "processing"]);
  });

  it("fails closed (denies) if it can't even show the approver the ask, and logs it", async () => {
    const client: ApprovalMessagingClient = {
      openDm: vi.fn().mockRejectedValue(new Error("missing im:write scope")),
      postMessage: vi.fn(),
      updateMessage: vi.fn(),
    };
    const warn = vi.fn();
    const gateway = new PermissionApprovalGateway(client, { warn });
    const context = fakeContext();
    const responder = gateway.createResponder(context);

    await expect(responder(fakeAsk(), new AbortController().signal)).resolves.toBe("deny");
    expect(context.statuses).toEqual(["suspended", "processing"]);
    expect(warn).toHaveBeenCalledWith(expect.stringContaining("DM"), expect.any(Error));
  });

  it("ignores an action_id outside the mecatl_approval: namespace", async () => {
    const client = fakeClient();
    const gateway = new PermissionApprovalGateway(client);
    const responder = gateway.createResponder(fakeContext());
    const verdictPromise = responder(fakeAsk(), new AbortController().signal);
    await flush();

    await gateway.handleAction({
      actionId: "some_other_plugin:button",
      askId: "ask-1",
      clickerUserId: "U1",
    });

    await gateway.handleAction({
      actionId: "mecatl_approval:allow_once",
      askId: "ask-1",
      clickerUserId: "U1",
    });
    await expect(verdictPromise).resolves.toBe("allow_once");
  });
});

describe("PermissionApprovalGateway args rendering", () => {
  function tableBlock(blocks: unknown[]): { rows: unknown[][] } | undefined {
    return blocks.find(
      (block): block is { rows: unknown[][] } =>
        typeof block === "object" && block !== null && "rows" in block,
    );
  }

  function firstPostedBlocks(client: ReturnType<typeof fakeClient>): unknown[] {
    const post = client.posted[0];
    if (post === undefined) throw new Error("expected a message to have been posted");
    return post.blocks;
  }

  /** Mirrors `keyCell`/`richTextCell` in src/approvals.ts: a bold `rich_text` run. */
  function keyCell(text: string): unknown {
    return {
      elements: [
        { elements: [{ style: { bold: true }, text, type: "text" }], type: "rich_text_section" },
      ],
      type: "rich_text",
    };
  }

  /** Mirrors `valueCell` for the non-numeric branch: a code-styled `rich_text` run. */
  function valueCell(text: string): unknown {
    return {
      elements: [
        { elements: [{ style: { code: true }, text, type: "text" }], type: "rich_text_section" },
      ],
      type: "rich_text",
    };
  }

  it("renders a flat args object as a table, one row per key", async () => {
    const client = fakeClient();
    const gateway = new PermissionApprovalGateway(client);
    const responder = gateway.createResponder(fakeContext());
    void responder(
      fakeAsk({ args: JSON.stringify({ content: "hello", path: "test.txt" }) }),
      new AbortController().signal,
    );
    await flush();

    const table = tableBlock(firstPostedBlocks(client));
    expect(table?.rows).toEqual([
      [keyCell("content"), valueCell("hello")],
      [keyCell("path"), valueCell("test.txt")],
    ]);
  });

  it("renders a number as a raw_number cell", async () => {
    const client = fakeClient();
    const gateway = new PermissionApprovalGateway(client);
    const responder = gateway.createResponder(fakeContext());
    void responder(
      fakeAsk({ args: JSON.stringify({ timeout: 30 }) }),
      new AbortController().signal,
    );
    await flush();

    const table = tableBlock(firstPostedBlocks(client));
    expect(table?.rows).toEqual([[keyCell("timeout"), { text: "30", type: "raw_number" }]]);
  });

  it("renders a nested value as inline code-styled JSON instead of a leaf value", async () => {
    const client = fakeClient();
    const gateway = new PermissionApprovalGateway(client);
    const responder = gateway.createResponder(fakeContext());
    void responder(
      fakeAsk({ args: JSON.stringify({ options: { recursive: true }, tags: ["a", "b"] }) }),
      new AbortController().signal,
    );
    await flush();

    const table = tableBlock(firstPostedBlocks(client));
    expect(table?.rows).toEqual([
      [keyCell("options"), valueCell('{"recursive":true}')],
      [keyCell("tags"), valueCell('["a","b"]')],
    ]);
  });

  it("falls back to a single-cell raw dump for non-object args (e.g. invalid JSON)", async () => {
    const client = fakeClient();
    const gateway = new PermissionApprovalGateway(client);
    const responder = gateway.createResponder(fakeContext());
    void responder(fakeAsk({ args: "not valid json" }), new AbortController().signal);
    await flush();

    const table = tableBlock(firstPostedBlocks(client));
    expect(table?.rows).toEqual([[{ text: "not valid json", type: "raw_text" }]]);
  });

  it("omits the args table entirely for an empty object", async () => {
    const client = fakeClient();
    const gateway = new PermissionApprovalGateway(client);
    const responder = gateway.createResponder(fakeContext());
    void responder(fakeAsk({ args: "{}" }), new AbortController().signal);
    await flush();

    expect(firstPostedBlocks(client)).toHaveLength(1); // just the card, no table block
  });

  it("caps the table at 10 rows and notes how many were omitted", async () => {
    const client = fakeClient();
    const gateway = new PermissionApprovalGateway(client);
    const responder = gateway.createResponder(fakeContext());
    const manyArgs = Object.fromEntries(
      Array.from({ length: 12 }, (_, i) => [`key${i}`, `value${i}`]),
    );
    void responder(fakeAsk({ args: JSON.stringify(manyArgs) }), new AbortController().signal);
    await flush();

    const rows = tableBlock(firstPostedBlocks(client))?.rows ?? [];
    expect(rows).toHaveLength(10);
    expect(rows[9]).toEqual([
      { text: "…", type: "raw_text" },
      { text: "and 3 more", type: "raw_text" },
    ]);
  });

  it("puts the tool on the title and origin+reason on the subtitle, not the body", async () => {
    const client = fakeClient();
    const gateway = new PermissionApprovalGateway(client);
    const responder = gateway.createResponder(fakeContext({ originLabel: "a channel" }));
    void responder(
      fakeAsk({ args: "{}", reason: "matched a configured ask rule", tool: "Shell" }),
      new AbortController().signal,
    );
    await flush();

    const [card] = firstPostedBlocks(client) as [Record<string, unknown>];
    expect(card).toMatchObject({
      subtitle: {
        text: "Requested from a channel · matched a configured ask rule",
        type: "plain_text",
      },
      title: { text: "Permission request: Shell", type: "plain_text" },
      type: "card",
    });
    expect(card).not.toHaveProperty("body");
  });

  it("truncates a too-long subtitle to exactly 150 characters, never 151 (panel-review, samuv)", async () => {
    const client = fakeClient();
    const gateway = new PermissionApprovalGateway(client);
    // originLabel alone (via "Requested from <originLabel>") pushes the subtitle to 151+ raw
    // characters before clamping — long enough to catch an off-by-one in the truncation itself.
    const originLabel = "x".repeat(200);
    const responder = gateway.createResponder(fakeContext({ originLabel }));
    void responder(fakeAsk({ reason: "" }), new AbortController().signal);
    await flush();

    const [card] = firstPostedBlocks(client) as [Record<string, unknown>];
    const subtitle = card.subtitle as { text: string; type: string };
    expect(subtitle.text).toHaveLength(150);
    expect(subtitle.text.endsWith("…")).toBe(true);
  });
});
