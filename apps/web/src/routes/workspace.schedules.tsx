// SPDX-License-Identifier: Apache-2.0

import { createFileRoute } from "@tanstack/react-router";
import { SchedulesWorkspace } from "../features/schedules/schedules-workspace";

export const Route = createFileRoute("/workspace/schedules")({
  component: SchedulesWorkspace,
});
