import { describe, expect, it } from "vitest";
import {
  attachHookToMessage,
  formatHookNotice,
  type HookStreamEvent,
  hookChipClass,
  hookNoticeBody,
  hookOutcomeText,
  hookTextClass,
  isHookNotice,
  parseHookNotice,
  reduceHookEvent,
} from "./hook-notice";
import type { AgentMessage, HookDecision, HookNotice } from "./types";

const notice = (partial: Partial<HookNotice> = {}): HookNotice => ({
  phase: "PreToolUse",
  tool: "Shell",
  decision: "blocked",
  text: "rm -rf is not allowed",
  ...partial,
});

const hookEvent = (
  partial: Partial<Omit<HookStreamEvent, "type">> = {},
): HookStreamEvent => ({
  type: "hook",
  phase: "PreToolUse",
  tool: "Shell",
  decision: "blocked",
  callId: "c1",
  text: "rm -rf is not allowed",
  ...partial,
});

const assistant = (partial: Partial<AgentMessage> = {}): AgentMessage => ({
  id: "m1",
  role: "assistant",
  content: "",
  timestamp: 0,
  ...partial,
});

const runningCall = {
  callId: "c1",
  name: "Shell",
  input: "cmd: rm -rf /",
  status: "running" as const,
};

/**
 * The words a hook fire renders with: the phase (and tool) the daemon named,
 * then its message — or what the decision did when it sent none. The daemon's
 * own "PreToolUse hook rewrote …" messages already open with the phase and
 * must not be prefixed twice.
 */
describe("hookNoticeBody", () => {
  it("names the phase and tool before the daemon's message", () => {
    expect(hookNoticeBody(notice())).toBe(
      "PreToolUse hook (Shell): rm -rf is not allowed",
    );
  });

  it("keeps a message that already opens with the phase verbatim", () => {
    expect(
      hookNoticeBody(
        notice({
          decision: "modified",
          text: "PreToolUse hook rewrote tool arguments for Shell",
        }),
      ),
    ).toBe("PreToolUse hook rewrote tool arguments for Shell");
  });

  it("says what the decision did when the daemon sent no message", () => {
    expect(hookNoticeBody(notice({ decision: "modified", text: "" }))).toBe(
      "PreToolUse hook (Shell) rewrote the action",
    );
    expect(hookNoticeBody(notice({ decision: "advisory", text: "  " }))).toBe(
      "PreToolUse hook (Shell) flagged the action",
    );
  });

  it("handles a call-less lifecycle hook with no tool, and no phase at all", () => {
    expect(
      hookNoticeBody(
        notice({ phase: "Stop", tool: "", decision: "info", text: "wrap up" }),
      ),
    ).toBe("Stop hook: wrap up");
    expect(
      hookNoticeBody(
        notice({ phase: "", tool: "", decision: "info", text: "" }),
      ),
    ).toBe("Hook ran");
  });
});

describe("hookOutcomeText", () => {
  it("prefers the daemon's message and falls back to the decision's outcome", () => {
    expect(hookOutcomeText(notice())).toBe("rm -rf is not allowed");
    expect(hookOutcomeText(notice({ text: "" }))).toBe(
      "The hook blocked the action.",
    );
  });
});

/**
 * Call-less fires travel through `AgentMessage.notices` as strings, so the
 * bubble needs a marker it can parse back into a decision — and that marker
 * must not collide with the other marked notice (`[conversation compacted]`).
 */
describe("hook notice marker", () => {
  it("round-trips a formatted notice through the parser", () => {
    const formatted = formatHookNotice(
      notice({ phase: "SessionStart", tool: "", text: "no network today" }),
    );
    expect(formatted).toBe(
      "[hook:blocked] SessionStart hook: no network today",
    );
    expect(parseHookNotice(formatted)).toEqual({
      decision: "blocked",
      text: "SessionStart hook: no network today",
    });
    expect(isHookNotice(formatted)).toBe(true);
  });

  it("parses every decision label", () => {
    const decisions: HookDecision[] = [
      "info",
      "blocked",
      "modified",
      "advisory",
    ];
    for (const decision of decisions) {
      expect(parseHookNotice(`[hook:${decision}] x`)).toEqual({
        decision,
        text: "x",
      });
    }
  });

  it("recognises only marked hook notices", () => {
    expect(parseHookNotice("[conversation compacted] older turns")).toBeNull();
    expect(parseHookNotice("Permission: Shell allowed")).toBeNull();
    expect(parseHookNotice("[hook:unknown] x")).toBeNull();
    expect(isHookNotice("read 3 of 9 files")).toBe(false);
  });
});

/** Four decisions, four distinguishable tones. */
describe("hook tones", () => {
  it.each<[HookDecision, string]>([
    ["blocked", "destructive"],
    ["modified", "info"],
    ["advisory", "warning"],
    ["info", "muted"],
  ])("tints a %s fire with the %s tone", (decision, tone) => {
    expect(hookChipClass(decision)).toContain(tone);
    expect(hookTextClass(decision)).toContain(tone);
  });

  it("keeps the four chip tones distinct", () => {
    const classes = (
      ["blocked", "modified", "advisory", "info"] as HookDecision[]
    ).map(hookChipClass);
    expect(new Set(classes).size).toBe(4);
  });
});

/**
 * A fire attributed to a call lands on that call as a chip and never touches
 * its status: a PreToolUse block is followed by the daemon's own is_error
 * tool result, which is what fails the call. A fire the message cannot place
 * becomes a marked transcript notice.
 */
describe("attachHookToMessage", () => {
  it("attributes a fire to the call it names and leaves the call's status alone", () => {
    const message = attachHookToMessage(
      assistant({ toolCalls: [runningCall] }),
      hookEvent(),
    );
    expect(message.toolCalls?.[0]).toMatchObject({
      status: "running",
      hooks: [
        {
          phase: "PreToolUse",
          tool: "Shell",
          decision: "blocked",
          text: "rm -rf is not allowed",
        },
      ],
    });
    expect(message.notices).toBeUndefined();
  });

  it("appends a second fire on the same call in order", () => {
    const once = attachHookToMessage(
      assistant({ toolCalls: [runningCall] }),
      hookEvent({ decision: "advisory", text: "looks risky" }),
    );
    const twice = attachHookToMessage(
      once,
      hookEvent({ phase: "PostToolUse", decision: "modified", text: "" }),
    );
    expect(twice.toolCalls?.[0].hooks?.map((hook) => hook.decision)).toEqual([
      "advisory",
      "modified",
    ]);
  });

  it("appends a marked notice when the named call is not on the message", () => {
    const message = attachHookToMessage(
      assistant({ toolCalls: [runningCall], notices: ["earlier"] }),
      hookEvent({ callId: "other" }),
    );
    expect(message.toolCalls?.[0].hooks).toBeUndefined();
    expect(message.notices).toEqual([
      "earlier",
      "[hook:blocked] PreToolUse hook (Shell): rm -rf is not allowed",
    ]);
  });

  it("appends a marked notice for a call-less lifecycle hook", () => {
    const message = attachHookToMessage(
      assistant(),
      hookEvent({
        phase: "UserPromptSubmit",
        tool: "",
        callId: "",
        decision: "advisory",
        text: "prompt mentions a secret",
      }),
    );
    expect(message.notices).toEqual([
      "[hook:advisory] UserPromptSubmit hook: prompt mentions a secret",
    ]);
  });
});

describe("reduceHookEvent", () => {
  let serial = 0;
  const open = (): AgentMessage => assistant({ id: `new-${++serial}` });
  /** The watch reducer's trailing-assistant helper, verbatim in shape. */
  const onAssistantFor =
    (messages: AgentMessage[]) =>
    (apply: (message: AgentMessage) => AgentMessage) => {
      const last = messages.at(-1);
      return last?.role === "assistant"
        ? [...messages.slice(0, -1), apply(last)]
        : [...messages, apply(open())];
    };

  it("lands on the latest message carrying the call, not the trailing bubble", () => {
    const messages: AgentMessage[] = [
      assistant({ id: "turn-1", toolCalls: [runningCall] }),
      { id: "u2", role: "user", content: "and then?", timestamp: 0 },
      assistant({ id: "turn-2", content: "Sure." }),
    ];
    const next = reduceHookEvent(
      messages,
      hookEvent({ phase: "PostToolUse", decision: "advisory" }),
      onAssistantFor(messages),
    );
    expect(next).toHaveLength(3);
    expect(next[0].toolCalls?.[0].hooks).toHaveLength(1);
    expect(next[2].notices).toBeUndefined();
  });

  it("opens an assistant bubble for a call-less hook after a user message", () => {
    const messages: AgentMessage[] = [
      { id: "u1", role: "user", content: "hi", timestamp: 0 },
    ];
    const next = reduceHookEvent(
      messages,
      hookEvent({ phase: "SessionStart", tool: "", callId: "" }),
      onAssistantFor(messages),
    );
    expect(next).toHaveLength(2);
    expect(next[1].role).toBe("assistant");
    expect(next[1].notices).toEqual([
      "[hook:blocked] SessionStart hook: rm -rf is not allowed",
    ]);
  });

  it("falls back to a notice when no message carries the named call", () => {
    const messages: AgentMessage[] = [assistant({ id: "turn-1" })];
    const next = reduceHookEvent(
      messages,
      hookEvent({ callId: "gone" }),
      onAssistantFor(messages),
    );
    expect(next[0].notices).toEqual([
      "[hook:blocked] PreToolUse hook (Shell): rm -rf is not allowed",
    ]);
  });
});
