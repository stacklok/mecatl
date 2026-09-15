import { redirect } from "next/navigation";

/**
 * Workspace-scoped agent settings were merged into the personal Settings page,
 * so this route now redirects there; bookmarks and old links still land on the
 * right place.
 */
export default function WorkspaceSettingsPage() {
  redirect("/workspace/settings");
}
