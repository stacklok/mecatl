import { redirect } from "next/navigation";

/** The Enter-behavior setting lives on Personalize now; keep old links. */
export default function LegacyChatSettingsPage() {
  redirect("/workspace/settings/appearance");
}
