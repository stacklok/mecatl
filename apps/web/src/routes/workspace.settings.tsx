// SPDX-License-Identifier: Apache-2.0

import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { SettingsWorkspace } from "../features/settings/settings-workspace";

export const Route = createFileRoute("/workspace/settings")({
  component: SettingsIndexPage,
});

function SettingsIndexPage() {
  const navigate = useNavigate();
  return (
    <SettingsWorkspace
      onSectionChange={(section) =>
        void navigate({
          params: { section },
          search: { item: undefined },
          to: "/workspace/settings/$section",
        })
      }
      section="profile"
    />
  );
}
