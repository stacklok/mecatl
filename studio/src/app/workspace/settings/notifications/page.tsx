import { redirect } from "next/navigation";

/** Notification settings live on Personalize now; keep old links. */
export default function LegacyNotificationSettingsPage() {
  redirect("/workspace/settings/appearance");
}
