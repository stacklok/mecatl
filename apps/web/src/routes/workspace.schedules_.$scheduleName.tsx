// SPDX-License-Identifier: Apache-2.0

import { createFileRoute } from "@tanstack/react-router";
import { ScheduleDetail } from "../features/schedules/schedule-detail";

export const Route = createFileRoute("/workspace/schedules_/$scheduleName")({
  component: ScheduleDetailPage,
});

function ScheduleDetailPage() {
  const { scheduleName } = Route.useParams();
  return <ScheduleDetail scheduleName={scheduleName} />;
}
