import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { resetHarnessClient } from "@/lib/harness/sdk";
import {
  jsonResponse,
  sessionSnapshot,
  stubHarnessFetch,
} from "@/lib/harness/sdk-test-stub";
import { deliveryMessage } from "../delivery-message";
import type { NotificationFactory } from "../delivery-notifications";
import type { AgentMessage } from "../types";
import {
  type DeliveryFollowInput,
  useDeliveryFollow,
} from "./use-delivery-follow";

/**
 * The between-polls pickup: when the inventory row's `updatedAt` advances
 * while nothing is running here, the transcript is read and the chat
 * re-reads it ONLY when a delivery note this list lacks is present — so a
 * local run's own bump never replaces the rich live messages. Plus the
 * hidden-tab notification for a newly landed completed note.
 */

const completedNote =
  "<<<UNTRUSTED\n[scheduled task nightly (fire f-1) completed with stop reason: end_turn]\nDigest: 3 PRs merged.\n<<<UNTRUSTED\n";

const history: AgentMessage[] = [
  { id: "u1", role: "user", content: "hi", timestamp: 0 },
  { id: "a1", role: "assistant", content: "hello", timestamp: 0 },
];

function transcriptWith(texts: Array<{ role: string; text: string }>) {
  return { session_id: "s1", complete: true, messages: texts };
}

function stub(transcriptTexts: Array<{ role: string; text: string }>) {
  return stubHarnessFetch((request) => {
    if (request.path === "/v1/sessions/s1") {
      return jsonResponse(200, sessionSnapshot("s1"));
    }
    if (request.path === "/v1/sessions/s1/transcript") {
      return transcriptWith(transcriptTexts);
    }
    return undefined;
  });
}

function baseInput(overrides: Partial<DeliveryFollowInput> = {}) {
  return {
    sessionId: "s1",
    updatedAt: 1_000,
    state: "idle",
    isStreaming: false,
    connected: true,
    messages: history,
    refreshTranscript: vi.fn(async () => undefined),
    ...overrides,
  } satisfies DeliveryFollowInput;
}

const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

describe("useDeliveryFollow — transcript pickup", () => {
  it("re-reads the transcript when updatedAt advances and it carries an unseen note", async () => {
    const fetches = stub([
      { role: "user", text: "hi" },
      { role: "assistant", text: "hello" },
      { role: "user", text: completedNote },
    ]);
    const input = baseInput();
    const { rerender } = renderHook((props) => useDeliveryFollow(props), {
      initialProps: input,
    });
    await settle();
    expect(input.refreshTranscript).not.toHaveBeenCalled();

    rerender({ ...input, updatedAt: 2_000 });
    await waitFor(() =>
      expect(input.refreshTranscript).toHaveBeenCalledTimes(1),
    );
    expect(
      fetches.requests.some((r) => r.path === "/v1/sessions/s1/transcript"),
    ).toBe(true);
  });

  it("leaves the rich local messages alone when the transcript has no unseen note", async () => {
    const fetches = stub([
      { role: "user", text: "hi" },
      { role: "assistant", text: "hello" },
    ]);
    const input = baseInput();
    const { rerender } = renderHook((props) => useDeliveryFollow(props), {
      initialProps: input,
    });
    rerender({ ...input, updatedAt: 2_000 });
    await waitFor(() =>
      expect(
        fetches.requests.some((r) => r.path === "/v1/sessions/s1/transcript"),
      ).toBe(true),
    );
    await settle();
    expect(input.refreshTranscript).not.toHaveBeenCalled();
  });

  it("does not re-read for a note the list already renders", async () => {
    const fetches = stub([{ role: "user", text: completedNote }]);
    const input = baseInput({
      messages: [...history, deliveryMessage("d1", completedNote, 0)],
    });
    const { rerender } = renderHook((props) => useDeliveryFollow(props), {
      initialProps: input,
    });
    rerender({ ...input, updatedAt: 2_000 });
    await waitFor(() =>
      expect(
        fetches.requests.some((r) => r.path === "/v1/sessions/s1/transcript"),
      ).toBe(true),
    );
    await settle();
    expect(input.refreshTranscript).not.toHaveBeenCalled();
  });

  it("absorbs advances while this tab streams or the row reads running/awaiting", async () => {
    const fetches = stub([{ role: "user", text: completedNote }]);
    const input = baseInput();
    const { rerender } = renderHook((props) => useDeliveryFollow(props), {
      initialProps: input,
    });
    rerender({ ...input, isStreaming: true, updatedAt: 2_000 });
    rerender({ ...input, isStreaming: false, updatedAt: 2_000 });
    rerender({ ...input, state: "running", updatedAt: 3_000 });
    rerender({ ...input, state: "awaiting", updatedAt: 3_500 });
    rerender({ ...input, state: "idle", updatedAt: 3_500 });
    await settle();
    expect(fetches.requests).toEqual([]);
    expect(input.refreshTranscript).not.toHaveBeenCalled();
  });

  it("treats a session switch as a new baseline, not a trigger", async () => {
    const fetches = stub([{ role: "user", text: completedNote }]);
    const input = baseInput({ sessionId: "s0", updatedAt: 9_000 });
    const { rerender } = renderHook((props) => useDeliveryFollow(props), {
      initialProps: input,
    });
    rerender({ ...input, sessionId: "s1", updatedAt: 1_000 });
    await settle();
    expect(fetches.requests).toEqual([]);
  });

  it("skips a draft, an offline daemon, and an empty local list", async () => {
    const fetches = stub([{ role: "user", text: completedNote }]);
    const draft = baseInput({ sessionId: null });
    const { rerender: rerenderDraft } = renderHook(
      (props) => useDeliveryFollow(props),
      { initialProps: draft },
    );
    rerenderDraft({ ...draft, updatedAt: 2_000 });

    const offline = baseInput({ connected: false });
    const { rerender: rerenderOffline } = renderHook(
      (props) => useDeliveryFollow(props),
      { initialProps: offline },
    );
    rerenderOffline({ ...offline, updatedAt: 2_000 });

    const empty = baseInput({ messages: [] });
    const { rerender: rerenderEmpty } = renderHook(
      (props) => useDeliveryFollow(props),
      { initialProps: empty },
    );
    rerenderEmpty({ ...empty, updatedAt: 2_000 });

    await settle();
    expect(fetches.requests).toEqual([]);
  });
});

describe("useDeliveryFollow — notifications", () => {
  function fakeNotification(permission: NotificationPermission) {
    const created: Array<{ title: string; options?: NotificationOptions }> = [];
    const Fake = class {
      static permission = permission;
      constructor(title: string, options?: NotificationOptions) {
        created.push({ title, options });
      }
    } as unknown as NotificationFactory;
    return { Fake, created };
  }

  it("notifies a hidden tab once for a newly landed completed note, never for history", () => {
    const { Fake, created } = fakeNotification("granted");
    vi.stubGlobal("Notification", Fake);
    const visibility = vi
      .spyOn(document, "visibilityState", "get")
      .mockReturnValue("hidden");
    try {
      const old = deliveryMessage("d0", completedNote, 0);
      const input = baseInput({ messages: [] });
      const { rerender } = renderHook((props) => useDeliveryFollow(props), {
        initialProps: input,
      });
      // The first non-empty render is the opened chat's history.
      rerender({ ...input, messages: [...history, old] });
      expect(created).toEqual([]);
      // A later note is news.
      const fresh = deliveryMessage(
        "d1",
        "<<<UNTRUSTED\n[scheduled task nightly (fire f-2) completed with stop reason: error]\nboom\n<<<UNTRUSTED\n",
        0,
      );
      rerender({ ...input, messages: [...history, old, fresh] });
      expect(created).toEqual([
        {
          title: "Scheduled task finished",
          options: {
            body: "nightly — error",
            tag: "mecatl-delivery-f-2",
            icon: "/favicon.ico",
          },
        },
      ]);
      // A rebuild carrying the same notes does not re-notify.
      rerender({ ...input, messages: [...history, old, { ...fresh }] });
      expect(created).toHaveLength(1);
    } finally {
      visibility.mockRestore();
    }
  });

  it("stays quiet while the tab is visible", () => {
    const { Fake, created } = fakeNotification("granted");
    vi.stubGlobal("Notification", Fake);
    const input = baseInput();
    const { rerender } = renderHook((props) => useDeliveryFollow(props), {
      initialProps: input,
    });
    rerender({
      ...input,
      messages: [...history, deliveryMessage("d1", completedNote, 0)],
    });
    expect(created).toEqual([]);
  });
});
