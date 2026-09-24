// SPDX-License-Identifier: Apache-2.0

import { createFileRoute, redirect, useNavigate } from "@tanstack/react-router";
import { isSettingsSection } from "../features/settings/settings-sections";
import { SettingsWorkspace } from "../features/settings/settings-workspace";

export const Route = createFileRoute("/workspace/settings_/$section")({
  beforeLoad: ({ params, search }) => {
    if (!isSettingsSection(params.section)) {
      throw redirect({
        params: { section: "profile" },
        replace: true,
        search: { item: undefined },
        to: "/workspace/settings/$section",
      });
    }
    if (params.section === "memory" && search.item) {
      throw redirect({ replace: true, search: { item: search.item }, to: "/workspace/memory" });
    }
  },
  component: SettingsSectionPage,
  validateSearch: (search: Record<string, unknown>) => ({
    item: typeof search.item === "string" ? search.item : undefined,
  }),
});

export function SettingsSectionPage() {
  const navigate = useNavigate();
  const { section } = Route.useParams();
  if (!isSettingsSection(section)) return null;
  return (
    <SettingsWorkspace
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
