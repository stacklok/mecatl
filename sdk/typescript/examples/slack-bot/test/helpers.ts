import type { ApprovalContext, ApprovalMessagingClient } from "../src/approvals.js";

export function fakeAsk(
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

export function fakeClient(): ApprovalMessagingClient & {
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

export function fakeContext(overrides: Partial<ApprovalContext> = {}): ApprovalContext & {
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
export async function flush(): Promise<void> {
  await new Promise((resolveTick) => setTimeout(resolveTick, 0));
}
