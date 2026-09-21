// SPDX-License-Identifier: Apache-2.0

import { createFileRoute } from "@tanstack/react-router";
import { ShortcutReference } from "../features/shortcuts/shortcut-reference";

export const Route = createFileRoute("/workspace/shortcuts")({
  component: ShortcutReference,
});
