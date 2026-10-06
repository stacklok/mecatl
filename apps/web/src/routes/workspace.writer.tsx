// SPDX-License-Identifier: Apache-2.0

import { createFileRoute } from "@tanstack/react-router";
import { WriterWorkspace } from "../features/writer/writer-workspace";

export const Route = createFileRoute("/workspace/writer")({
  component: WriterWorkspace,
});
