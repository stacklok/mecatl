/**
 * Browser notifications for scheduled-task deliveries: the production trigger
 * behind the appearance page's "Scheduled task finished" test button. A
 * notification fires only for a COMPLETED note that newly appeared in the
 * chat, only when the user granted permission, and only while the tab is
 * hidden (a visible chat shows the card itself). There is no stored
 * preference beyond the browser permission — the appearance page owns the
 * permission request and nothing else.
 */

import type { DeliveryNoteInfo } from "@/lib/protocol/delivery-note";
import { deliveryKey } from "./delivery-message";
import type { AgentMessage } from "./types";

export const DELIVERY_NOTIFICATION_TITLE = "Scheduled task finished";

/**
 * The completed delivery notes present in `next` but not in `previous`
 * (keyed by fire + kind). Started notes never notify; a fire already on
 * screen never re-notifies, however the list was rebuilt.
 */
export function newCompletedDeliveries(
  previous: readonly AgentMessage[],
  next: readonly AgentMessage[],
): DeliveryNoteInfo[] {
  const seen = new Set<string>();
  for (const message of previous) {
    if (message.delivery) seen.add(deliveryKey(message.delivery));
  }
  const fresh: DeliveryNoteInfo[] = [];
  for (const message of next) {
    const delivery = message.delivery;
    if (!delivery || delivery.kind !== "completed") continue;
    const key = deliveryKey(delivery);
    if (seen.has(key)) continue;
    seen.add(key);
    fresh.push(delivery);
  }
  return fresh;
}

/** The `Notification` constructor surface `notifyDelivery` needs. */
export type NotificationFactory = {
  readonly permission: NotificationPermission;
  new (title: string, options?: NotificationOptions): unknown;
};

function ambientNotification(): NotificationFactory | undefined {
  return typeof Notification === "undefined" ? undefined : Notification;
}

function ambientVisibility(): DocumentVisibilityState {
  return typeof document === "undefined" ? "visible" : document.visibilityState;
}

/**
 * Shows the "Scheduled task finished" notification for one completed note.
 * Returns whether one was created: false when the browser has no
 * Notification API, permission is not granted, or the document is visible.
 * The tag is per fire, so a duplicate record collapses browser-side too.
 */
export function notifyDelivery(
  info: DeliveryNoteInfo,
  notify: NotificationFactory | undefined = ambientNotification(),
  visibility: () => DocumentVisibilityState = ambientVisibility,
): boolean {
  if (!notify || notify.permission !== "granted") return false;
  if (visibility() !== "hidden") return false;
  new notify(DELIVERY_NOTIFICATION_TITLE, {
    body: `${info.scheduleName} — ${info.stop || "completed"}`,
    tag: `mecatl-delivery-${info.fireId}`,
    icon: "/favicon.ico",
  });
  return true;
}
