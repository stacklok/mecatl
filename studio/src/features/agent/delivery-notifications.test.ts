import { afterEach, describe, expect, it, vi } from "vitest";
import type { DeliveryNoteInfo } from "@/lib/protocol/delivery-note";
import {
  DELIVERY_NOTIFICATION_TITLE,
  type NotificationFactory,
  newCompletedDeliveries,
  notifyDelivery,
} from "./delivery-notifications";
import type { AgentMessage } from "./types";

const note = (
  fireId: string,
  kind: DeliveryNoteInfo["kind"],
  stop?: string,
): AgentMessage => ({
  id: `${kind}-${fireId}`,
  role: "user",
  content: kind === "completed" ? "body" : "",
  timestamp: 0,
  delivery: { scheduleName: "nightly", fireId, kind, stop },
});

const plain: AgentMessage = {
  id: "u",
  role: "user",
  content: "hi",
  timestamp: 0,
};

describe("newCompletedDeliveries", () => {
  it("returns only the completed notes added since the previous list", () => {
    const previous = [plain, note("f-1", "completed", "end_turn")];
    const next = [
      ...previous,
      note("f-2", "started"),
      note("f-2", "completed", "end_turn"),
      note("f-3", "completed", "error"),
    ];
    expect(newCompletedDeliveries(previous, next).map((d) => d.fireId)).toEqual(
      ["f-2", "f-3"],
    );
  });

  it("ignores started notes and fires already present", () => {
    const previous = [note("f-1", "completed", "end_turn")];
    expect(
      newCompletedDeliveries(previous, [
        ...previous,
        note("f-9", "started"),
        note("f-1", "completed", "end_turn"),
      ]),
    ).toEqual([]);
  });

  it("reports a fire once even when the list carries it twice", () => {
    expect(
      newCompletedDeliveries(
        [],
        [note("f-1", "completed", "end_turn"), note("f-1", "completed")],
      ),
    ).toHaveLength(1);
  });
});

describe("notifyDelivery", () => {
  const info: DeliveryNoteInfo = {
    scheduleName: "nightly",
    fireId: "f-1",
    kind: "completed",
    stop: "end_turn",
  };

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

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("is a no-op without a Notification API or without permission", () => {
    expect(notifyDelivery(info, undefined, () => "hidden")).toBe(false);
    const denied = fakeNotification("denied");
    expect(notifyDelivery(info, denied.Fake, () => "hidden")).toBe(false);
    const asked = fakeNotification("default");
    expect(notifyDelivery(info, asked.Fake, () => "hidden")).toBe(false);
    expect(denied.created).toEqual([]);
    expect(asked.created).toEqual([]);
  });

  it("is a no-op while the document is visible", () => {
    const { Fake, created } = fakeNotification("granted");
    expect(notifyDelivery(info, Fake, () => "visible")).toBe(false);
    expect(created).toEqual([]);
  });

  it("constructs the per-fire notification when hidden and granted", () => {
    const { Fake, created } = fakeNotification("granted");
    expect(notifyDelivery(info, Fake, () => "hidden")).toBe(true);
    expect(created).toEqual([
      {
        title: DELIVERY_NOTIFICATION_TITLE,
        options: {
          body: "nightly — end_turn",
          tag: "mecatl-delivery-f-1",
          icon: "/favicon.ico",
        },
      },
    ]);
  });

  it("reads the ambient Notification and document visibility by default", () => {
    const { Fake, created } = fakeNotification("granted");
    vi.stubGlobal("Notification", Fake);
    const visibility = vi
      .spyOn(document, "visibilityState", "get")
      .mockReturnValue("hidden");
    try {
      expect(notifyDelivery(info)).toBe(true);
      expect(created[0]?.options?.tag).toBe("mecatl-delivery-f-1");
    } finally {
      visibility.mockRestore();
    }
  });
});
