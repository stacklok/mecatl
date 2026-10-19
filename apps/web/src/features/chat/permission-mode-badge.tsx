// SPDX-License-Identifier: Apache-2.0

import { Badge } from "../../components/ui/badge";
import { modeTitle, type SessionPermissionMode } from "../../lib/permission-mode";

/**
 * The chat header's permission-mode badge, from the prototype (`stack-08`).
 * It is silent for Manual, the everyday posture, so a badge in the header
 * always means the chat does not ask before every change.
 */
export function PermissionModeBadge({ mode }: { mode?: SessionPermissionMode }) {
  if (!mode || mode === "default") return null;
  const label = modeTitle(mode);
  return (
    <Badge
      className="shrink-0 select-none"
      title={`Permission mode: ${label}`}
      variant={mode === "plan" ? "info" : "success"}
    >
      <span className="sr-only">Permission mode: </span>
      {label}
    </Badge>
  );
}
