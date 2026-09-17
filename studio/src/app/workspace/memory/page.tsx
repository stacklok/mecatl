import { redirect } from "next/navigation";

/** Memory moved under Settings; keep old bookmarks working. */
export default function LegacyMemoryPage() {
  redirect("/workspace/settings/memory");
}
