// SPDX-License-Identifier: Apache-2.0

import { createFileRoute, redirect } from "@tanstack/react-router";
import { ProviderDetail } from "../features/settings/provider-detail";

export const Route = createFileRoute("/workspace/provider")({
  beforeLoad: ({ search }) => {
    if (!search.providerId) {
      throw redirect({
        params: { section: "providers" },
        replace: true,
        search: { item: undefined },
        to: "/workspace/settings/$section",
      });
    }
  },
  component: ProviderDetailPage,
  validateSearch: (search: Record<string, unknown>) => ({
    providerId: typeof search.providerId === "string" ? search.providerId : undefined,
  }),
});

function ProviderDetailPage() {
  const { providerId } = Route.useSearch();
  if (!providerId) return null;
  return <ProviderDetail providerId={providerId} />;
}
