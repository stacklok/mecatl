// SPDX-License-Identifier: Apache-2.0

import { createFileRoute } from "@tanstack/react-router";
import { SchedulesWorkspace } from "../features/schedules/schedules-workspace";

export const Route = createFileRoute("/workspace/schedules")({
  component: SchedulesPage,
  validateSearch: (search: Record<string, unknown>) => ({
    schedule: typeof search.schedule === "string" ? search.schedule : undefined,
  }),
});

function SchedulesPage() {
  const { schedule } = Route.useSearch();
  return <SchedulesWorkspace scheduleName={schedule} />;
}
