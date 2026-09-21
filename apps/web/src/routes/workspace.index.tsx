// SPDX-License-Identifier: Apache-2.0

import { createFileRoute } from "@tanstack/react-router";
import { EmptyWorkspace } from "../features/workspace/empty-workspace";

export const Route = createFileRoute("/workspace/")({
  component: EmptyWorkspace,
});
