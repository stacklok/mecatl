// SPDX-License-Identifier: Apache-2.0

export type BrowserNotificationPermission = NotificationPermission | "unsupported";

export function browserNotificationPermission(): BrowserNotificationPermission {
  return "Notification" in window ? Notification.permission : "unsupported";
}

export async function requestBrowserNotifications(): Promise<BrowserNotificationPermission> {
  if (!("Notification" in window)) return "unsupported";
  return Notification.requestPermission();
}

export function sendBrowserNotification(
  title: string,
  options?: NotificationOptions,
  onlyWhenHidden = false,
) {
  if (!("Notification" in window) || Notification.permission !== "granted") return false;
  if (onlyWhenHidden && document.visibilityState === "visible") return false;
  new Notification(title, options);
  return true;
}

export function notifyRunCompletion(title: string, failed = false) {
  return sendBrowserNotification(
    failed ? "Mecatl needs your attention" : "Mecatl finished",
    {
      body: failed ? `${title} stopped before completing.` : `${title} is ready to review.`,
      icon: "/stacklok-favicon.png",
      tag: `mecatl-run-${title}`,
    },
    true,
  );
}
