// SPDX-License-Identifier: Apache-2.0

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "../../components/ui/alert-dialog";
import { DEBUG_SESSION_CONSENT } from "./debug-session";

export interface DebugSessionTarget {
  id: string;
  title: string;
}

/**
 * The "Debug with AI" consent, ported from the prototype's
 * `debug-session-dialog.tsx`: the disclosure that the debugger sends the
 * target's stored transcript and event evidence, secrets included, to the
 * model, though the target itself is never modified. Nothing is created
 * until the user confirms; the caller creates the debug chat.
 */
export function DebugSessionDialog({
  onConfirm,
  onOpenChange,
  target,
}: {
  onConfirm: (target: DebugSessionTarget) => void;
  onOpenChange: (open: boolean) => void;
  target?: DebugSessionTarget;
}) {
  const title = target ? target.title.trim() || "Untitled chat" : "";
  return (
    <AlertDialog onOpenChange={onOpenChange} open={Boolean(target)}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {target ? `Debug with AI: “${title}”` : "Debug with AI"}
          </AlertDialogTitle>
          <AlertDialogDescription>{DEBUG_SESSION_CONSENT}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            onClick={() => {
              if (target) onConfirm(target);
            }}
          >
            Send evidence &amp; debug
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
