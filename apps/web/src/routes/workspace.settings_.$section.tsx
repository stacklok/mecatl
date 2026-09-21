// SPDX-License-Identifier: Apache-2.0

import { createFileRoute, Navigate, useNavigate } from "@tanstack/react-router";
import { isSettingsSection } from "../features/settings/settings-sections";
import { SettingsWorkspace } from "../features/settings/settings-workspace";

export const Route = createFileRoute("/workspace/settings_/$section")({
  component: SettingsSectionPage,
  validateSearch: (search: Record<string, unknown>) => ({
    item: typeof search.item === "string" ? search.item : undefined,
  }),
});

function SettingsSectionPage() {
  const navigate = useNavigate();
  const { section } = Route.useParams();
  const { item } = Route.useSearch();
  if (!isSettingsSection(section)) return <Navigate to="/workspace/settings" />;
  return (
    <SettingsWorkspace
      item={item}
      onItemChange={(nextItem) =>
        void navigate({
          params: { section },
          search: { item: nextItem },
          to: "/workspace/settings/$section",
        })
      }
      onSectionChange={(nextSection) =>
        void navigate({
          params: { section: nextSection },
          search: { item: undefined },
          to: "/workspace/settings/$section",
        })
      }
      section={section}
    />
  );
}
