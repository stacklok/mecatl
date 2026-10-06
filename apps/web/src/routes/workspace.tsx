// SPDX-License-Identifier: Apache-2.0

import { createFileRoute } from "@tanstack/react-router";
import { WorkspaceShell } from "../components/shell/workspace-shell";

export const Route = createFileRoute("/workspace")({
  component: WorkspaceShell,
});
