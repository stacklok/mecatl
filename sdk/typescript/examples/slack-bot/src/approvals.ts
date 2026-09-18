import type { App } from "@slack/bolt";
import type {
  PermissionAskEventPayload,
  PermissionAskResponder,
  PermissionVerdict,
} from "@stacklok-oss/mecatl-sdk";

const ACTION_PREFIX = "mecatl_approval:";
const MAX_ARGS_PREVIEW = 800;

/** The minimal shape this gateway needs from `app.client` — deliberately not the real
 * `@slack/web-api` `WebClient` type, same rationale as `access.ts`'s `UserLookupClient`: that
 * package is only a transitive dep via `@slack/bolt`, not declared in this package's own
 * `package.json`, and a narrow interface is trivial for tests to fake without any Slack SDK
 * types at all. */
export interface ApprovalWebClient {
  conversations: {
    open(args: { users: string }): Promise<{ channel?: { id?: string } }>;
  };
  chat: {
    postMessage(args: {
      channel: string;
      text: string;
      blocks?: unknown[];
    }): Promise<{ ts?: string; channel?: string }>;
    update(args: {
      channel: string;
      ts: string;
      text: string;
      blocks?: unknown[];
    }): Promise<unknown>;
  };
}

/** The Slack-facing surface `PermissionApprovalGateway` actually needs — narrower than
 * {@link ApprovalWebClient}, and the seam tests fake directly (see `test/approvals.test.ts`),
 * so gateway logic never has to know about `conversations.open`/`chat.*` argument shapes. */
export interface ApprovalMessagingClient {
  /** Resolves to the id of a 1:1 DM channel with `userId`, opening one if needed. */
  openDm(userId: string): Promise<string>;
  /** Posts a new message, returning its `ts` for a later `updateMessage`. */
  postMessage(channel: string, text: string, blocks: unknown[]): Promise<string>;
  /** Replaces a previously-posted message's content in place (e.g. to remove its buttons). */
  updateMessage(channel: string, ts: string, text: string, blocks?: unknown[]): Promise<void>;
}

/** Adapts a real Bolt `app.client` into {@link ApprovalMessagingClient}, caching each user's DM
 * channel id in memory for the process's lifetime — same in-memory-only precedent as
 * `MecatlBridge`'s session cache and `agentSessions.ts`'s thread-tracking sets; a bot restart
 * just reopens the DM next time, which is harmless (Slack returns the same channel either way). */
export function slackApprovalMessaging(client: ApprovalWebClient): ApprovalMessagingClient {
  const dmChannels = new Map<string, string>();
  return {
    async openDm(userId) {
      const cached = dmChannels.get(userId);
      if (cached !== undefined) return cached;
      const response = await client.conversations.open({ users: userId });
      const channel = response.channel?.id;
      if (channel === undefined) {
        throw new Error(`conversations.open returned no channel id for ${userId}`);
      }
      dmChannels.set(userId, channel);
      return channel;
    },
    async postMessage(channel, text, blocks) {
      const response = await client.chat.postMessage({ blocks, channel, text });
      const ts = response.ts;
      if (ts === undefined) throw new Error("chat.postMessage did not return a message ts");
      return ts;
    },
    async updateMessage(channel, ts, text, blocks) {
      await client.chat.update({ channel, text, ts, ...(blocks === undefined ? {} : { blocks }) });
    },
  };
}

interface PendingApproval {
  readonly resolve: (verdict: PermissionVerdict | undefined) => void;
  readonly authorizedUserId: string;
  readonly dmChannel: string;
  readonly ts: string;
  readonly onSettled: () => void;
  readonly tool: string;
  readonly originLabel: string;
}

/** Context `agentSessions.ts` supplies per run so the gateway can address the right person and
 * reflect the ask back onto that thread's Slack session status. */
export interface ApprovalContext {
  /** The Slack user id who initiated this run — the only person the ask is delivered to and
   * the only person whose click is honored. */
  authorizedUserId: string;
  /** A short human label for where this run came from (e.g. "your DM" or a channel mention),
   * shown on the approval card so it's clear which conversation the request belongs to. */
  originLabel: string;
  /** Best-effort: reflects the run's suspended/resumed state via `agents.sessions.setStatus`.
   * Called at most once per transition — never twice in a row with the same status. */
  setStatus: (status: "suspended" | "processing") => Promise<void>;
}

function verdictFromActionId(actionId: string): PermissionVerdict | undefined {
  if (!actionId.startsWith(ACTION_PREFIX)) return undefined;
  const suffix = actionId.slice(ACTION_PREFIX.length);
  if (suffix === "allow_once" || suffix === "allow_always" || suffix === "deny") return suffix;
  return undefined;
}

function verdictLabel(verdict: PermissionVerdict): string {
  switch (verdict) {
    case "allow_once":
      return "Allowed once";
    case "allow_always":
      return "Allowed for the rest of this session";
    case "deny":
      return "Denied";
    default:
      return verdict;
  }
}

/** Truncates to `max` characters INCLUDING the ellipsis (panel-review, samuv: appending "…"
 * after a full `max`-length slice produced `max + 1` characters, which could push a card
 * title/subtitle one character past Slack's exact 150-char limit and get the whole ask
 * rejected — denying it, via the fail-closed catch below, for a reason that had nothing to do
 * with the ask itself). */
function clamp(value: string, max: number): string {
  return value.length > max ? `${value.slice(0, max - 1)}…` : value;
}

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** A leaf (string/boolean/null — `valueCell` handles `number` separately, as `raw_number`)
 * renders as itself; anything else (a nested object or array) renders as compact inline JSON —
 * the "further JSON" case. */
function formatArgValue(value: unknown): string {
  if (value === null || typeof value !== "object") return String(value);
  return JSON.stringify(value);
}

const CARD_TITLE_MAX = 150;
const CARD_SUBTITLE_MAX = 150;
const MAX_ARGS_ROWS = 10;
const MAX_ARGS_CELL_VALUE = 300;

function rawTextCell(text: string): unknown {
  return { text, type: "raw_text" };
}

/** A `rich_text` table cell wrapping one styled text run — structural, not markdown, so styling
 * a model-influenced string (a key or an arg value) this way can't be hijacked into forging
 * different text or breaking out of the cell the way an unescaped mrkdwn string could. */
function richTextCell(text: string, style: Record<string, boolean>): unknown {
  return {
    elements: [{ elements: [{ style, text, type: "text" }], type: "rich_text_section" }],
    type: "rich_text",
  };
}

function keyCell(key: string): unknown {
  return richTextCell(key, { bold: true });
}

/** A genuine JSON number gets `raw_number` (Slack sorts/aligns it numerically); everything else
 * (including the "further JSON" nested-object/array case) renders as a code-styled text run. */
function valueCell(value: unknown): unknown {
  if (typeof value === "number") return { text: String(value), type: "raw_number" };
  return richTextCell(clamp(formatArgValue(value), MAX_ARGS_CELL_VALUE), { code: true });
}

/** Renders `ask.args` as a Block Kit `table` block, one row per key. Falls back to a single-cell
 * raw dump for anything that isn't a flat JSON object (invalid JSON, or a top-level array/
 * primitive — nothing to tabulate). Returns `undefined` for empty/absent args. */
function argsTableBlock(argsJson: string): unknown | undefined {
  if (argsJson.length === 0) return undefined;
  let parsed: unknown;
  try {
    parsed = JSON.parse(argsJson);
  } catch {
    parsed = undefined;
  }
  if (!isPlainRecord(parsed)) {
    return { rows: [[rawTextCell(clamp(argsJson, MAX_ARGS_PREVIEW))]], type: "table" };
  }
  const keys = Object.keys(parsed);
  if (keys.length === 0) return undefined;
  const cap = keys.length > MAX_ARGS_ROWS ? MAX_ARGS_ROWS - 1 : MAX_ARGS_ROWS;
  const rows = keys.slice(0, cap).map((key) => [keyCell(key), valueCell(parsed[key])]);
  const omitted = keys.length - cap;
  if (omitted > 0) rows.push([rawTextCell("…"), rawTextCell(`and ${omitted} more`)]);
  return { rows, type: "table" };
}

function approvalBlocks(
  ask: PermissionAskEventPayload,
  originLabel: string,
  askId: string,
): unknown[] {
  // Block Kit's `card` has no slot for a nested table — `body` is a plain 200-char string, not
  // a block container — so the reason (secondary info) rides the small gray `subtitle`, and the
  // args table (the important part) is a separate block placed ABOVE the card so it reads as
  // the primary content, with the card (title + approve/deny buttons) anchoring it below.
  const subtitleParts = [`Requested from ${originLabel}`];
  if (ask.reason.length > 0) subtitleParts.push(ask.reason);
  const card = {
    actions: [
      {
        action_id: `${ACTION_PREFIX}allow_once`,
        style: "primary",
        text: { text: "Allow once", type: "plain_text" },
        type: "button",
        value: askId,
      },
      {
        action_id: `${ACTION_PREFIX}allow_always`,
        text: { text: "Allow always", type: "plain_text" },
        type: "button",
        value: askId,
      },
      {
        action_id: `${ACTION_PREFIX}deny`,
        style: "danger",
        text: { text: "Deny", type: "plain_text" },
        type: "button",
        value: askId,
      },
    ],
    subtitle: { text: clamp(subtitleParts.join(" · "), CARD_SUBTITLE_MAX), type: "plain_text" },
    title: { text: clamp(`Permission request: ${ask.tool}`, CARD_TITLE_MAX), type: "plain_text" },
    type: "card",
  };
  const argsTable = argsTableBlock(ask.args);
  return argsTable === undefined ? [card] : [argsTable, card];
}

/** Replaces the approval card once it's resolved (clicked or retracted) — a small `card` with
 * just a headline, no actions, so the DM keeps its card look instead of degrading to plain
 * text once the buttons are gone. */
function outcomeCard(originLabel: string, headline: string): unknown {
  return {
    subtitle: {
      text: clamp(`Requested from ${originLabel}`, CARD_SUBTITLE_MAX),
      type: "plain_text",
    },
    title: { text: clamp(headline, CARD_TITLE_MAX), type: "plain_text" },
    type: "card",
  };
}

/**
 * Drives Slack-side manual approval for ordinary `permission.ask` events (issue #1397): DMs the
 * authorized user a Block Kit card and resolves the SDK's `onPermissionAsk` responder promise
 * from their click, instead of the previous unconditional `() => "allow_once"`.
 *
 * DMs, not in-thread messages: the completion criteria require both that the ask never renders
 * as an actionable control to a whole channel, AND that the UI clears when the run
 * terminates/cancels — not only when clicked. An in-thread `chat.postEphemeral` message would
 * satisfy the first but Slack gives no way to update an ephemeral message outside of a
 * `response_url` from an actual click, so there's no way to satisfy the second for "nobody
 * clicked, the run just ended." A regular DM message is just as user-scoped, but `chat.update`
 * works on it any time, from any code path — including a run-end/cancel/retract with no click
 * at all.
 *
 * Correlation is intentionally simple: `askId` is globally unique (it's a server-minted,
 * session-scoped id), so one flat in-memory `Map<askId, PendingApproval>` is enough — no
 * separate per-run/per-thread indexing needed. A double-click, a stale click, and a
 * post-terminal click are all handled by the same guard: the map entry is deleted
 * synchronously (before any `await`) the first time it's consumed, whether by a click or by
 * the ask's own `AbortSignal` firing, so anything arriving after that finds nothing pending.
 */
/** Same minimal shape as `access.ts`'s `MinimalLogger` — kept as its own type here so this
 * module doesn't need to import from a sibling for one method signature. */
export interface MinimalLogger {
  warn(msg: string, ...meta: unknown[]): void;
}

const noopLogger: MinimalLogger = { warn: () => {} };

export class PermissionApprovalGateway {
  readonly #client: ApprovalMessagingClient;
  readonly #logger: MinimalLogger;
  readonly #pending = new Map<string, PendingApproval>();

  constructor(client: ApprovalMessagingClient, logger: MinimalLogger = noopLogger) {
    this.#client = client;
    this.#logger = logger;
  }

  /** Builds the `onPermissionAsk` responder for one run. Closes over a pending-ask counter so
   * `setStatus` only fires at the 0→1 ("suspended") and 1→0 ("processing") boundaries — a
   * second concurrent ask on the same run must not flip status back while the first is still
   * pending. */
  createResponder(context: ApprovalContext): PermissionAskResponder {
    let pendingCount = 0;

    const suspend = async (): Promise<void> => {
      pendingCount += 1;
      if (pendingCount === 1) await context.setStatus("suspended");
    };
    const resume = async (): Promise<void> => {
      pendingCount = Math.max(0, pendingCount - 1);
      if (pendingCount === 0) await context.setStatus("processing");
    };

    return (ask, signal) =>
      new Promise<PermissionVerdict | undefined>((resolve) => {
        void this.#start(ask, context, suspend, resume, resolve, signal);
      });
  }

  /** Bolt-agnostic: the `app.action(...)` handler registered by {@link registerPermissionApprovals}
   * is a thin adapter onto this. Kept separate so tests exercise it without any Bolt object. */
  async handleAction(input: {
    askId: string;
    actionId: string;
    clickerUserId: string;
  }): Promise<void> {
    const verdict = verdictFromActionId(input.actionId);
    if (verdict === undefined) return;

    const pending = this.#pending.get(input.askId);
    if (pending === undefined) return; // stale, duplicate, or already-terminal — nothing to do
    this.#pending.delete(input.askId);

    if (input.clickerUserId !== pending.authorizedUserId) {
      // Structurally shouldn't happen (the card is DM'd only to that user), but reject rather
      // than silently drop: put the entry back so the rightful owner can still decide.
      this.#pending.set(input.askId, pending);
      return;
    }

    pending.onSettled();
    pending.resolve(verdict);
    const headline = `${verdictLabel(verdict)}: ${pending.tool}`;
    await this.#safeUpdate(pending.dmChannel, pending.ts, `${verdictLabel(verdict)}.`, [
      outcomeCard(pending.originLabel, headline),
    ]);
  }

  async #start(
    ask: PermissionAskEventPayload,
    context: ApprovalContext,
    suspend: () => Promise<void>,
    resume: () => Promise<void>,
    resolve: (verdict: PermissionVerdict | undefined) => void,
    signal: AbortSignal,
  ): Promise<void> {
    await suspend();

    const retractedText = "This request is no longer pending — the run ended or was cancelled.";
    const retractedCard = (): unknown[] => [
      outcomeCard(context.originLabel, `No longer pending: ${ask.tool}`),
    ];

    let settled = false;
    const onSettled = (): void => {
      if (settled) return;
      settled = true;
      void resume();
    };
    signal.addEventListener("abort", () => {
      const pending = this.#pending.get(ask.askId);
      if (pending === undefined) return;
      this.#pending.delete(ask.askId);
      pending.onSettled();
      resolve(undefined);
      void this.#safeUpdate(pending.dmChannel, pending.ts, retractedText, retractedCard());
    });

    try {
      const channel = await this.#client.openDm(context.authorizedUserId);
      const ts = await this.#client.postMessage(
        channel,
        `Permission request from ${context.originLabel}: ${ask.tool}`,
        approvalBlocks(ask, context.originLabel, ask.askId),
      );
      if (signal.aborted) {
        // Retracted/ended while the DM was still in flight — nothing was ever added to
        // #pending for the abort listener above to have found, so clean up here instead.
        onSettled();
        resolve(undefined);
        await this.#safeUpdate(channel, ts, retractedText, retractedCard());
        return;
      }
      this.#pending.set(ask.askId, {
        authorizedUserId: context.authorizedUserId,
        dmChannel: channel,
        onSettled,
        originLabel: context.originLabel,
        resolve,
        tool: ask.tool,
        ts,
      });
    } catch (error) {
      // Fail closed: an ask this bot can't even show the approver never silently proceeds.
      this.#logger.warn("failed to DM a permission-approval card — denying closed", error);
      onSettled();
      resolve("deny");
    }
  }

  async #safeUpdate(channel: string, ts: string, text: string, blocks: unknown[]): Promise<void> {
    try {
      await this.#client.updateMessage(channel, ts, text, blocks);
    } catch (error) {
      // Best-effort UI cleanup only — the ask itself is already resolved either way.
      this.#logger.warn("failed to update a permission-approval DM", error);
    }
  }
}

/** Registers the one Bolt-specific action handler and returns the gateway `agentSessions.ts`
 * calls `createResponder` on per run. */
export function registerPermissionApprovals(app: App): PermissionApprovalGateway {
  const gateway = new PermissionApprovalGateway(slackApprovalMessaging(app.client), app.logger);
  app.action(/^mecatl_approval:/, async ({ ack, action, body }) => {
    await ack();
    if (!("action_id" in action) || !("value" in action)) return;
    if (typeof action.action_id !== "string" || typeof action.value !== "string") return;
    await gateway.handleAction({
      actionId: action.action_id,
      askId: action.value,
      clickerUserId: body.user.id,
    });
  });
  return gateway;
}
