// SPDX-License-Identifier: Apache-2.0

import { createFileRoute, redirect } from "@tanstack/react-router";
import { MemoryFactDetail } from "../features/settings/memory-settings";

export const Route = createFileRoute("/workspace/memory")({
  beforeLoad: ({ search }) => {
    if (!search.item) {
      throw redirect({
        params: { section: "memory" },
        replace: true,
        search: { item: undefined },
        to: "/workspace/settings/$section",
      });
    }
  },
  component: MemoryDetailPage,
  validateSearch: (search: Record<string, unknown>) => ({
    item: typeof search.item === "string" ? search.item : undefined,
  }),
});

function MemoryDetailPage() {
  const { item } = Route.useSearch();
  if (!item) return null;
  return <MemoryFactDetail memoryKey={item} />;
}
