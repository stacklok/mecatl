"use client";

import type { ApprovalChoice, ApprovalRequest } from "@/features/agent";
import {
  offersAlwaysAllow,
  visibleApprovalVerdicts,
} from "@/features/agent/approval-verdicts";
import { useShortcut } from "@/lib/shortcuts/use-shortcuts";

/**
 * The app-wide keyboard verdicts for the main chat's head ask (the TUI's
 * approval modal keys: a/y allow once, w always allow, d/n deny). Registers
 * the `approval.*` shortcut handlers ONLY while an ask is waiting — with
 * nothing pending the letters keep their native meaning everywhere — and
 * gives a verdict only when the ask offers it (no Always allow for a child's
 * or a debugger MCP ask, mirroring the withheld button). Bare letters never
 * fire while the caret is in a text field (`comboFiresWhileTyping`), and the
 * composer is disabled during an ask anyway.
 *
 * Esc is not registered here: it rides the layered `close.esc` handler
 * (`useComposerEscape`), which denies a pending ask before it closes a panel
 * or stops the run.
 */
export function useApprovalShortcuts({
  approval,
  onRespond,
  debugSession = false,
}: {
  approval: ApprovalRequest | null;
  onRespond: (choice: ApprovalChoice) => void;
  debugSession?: boolean;
}): void {
  const enabled = approval !== null;
  const verdicts = approval
    ? visibleApprovalVerdicts(offersAlwaysAllow(approval, debugSession))
    : [];
  const give = (choice: ApprovalChoice) => {
    if (approval && verdicts.includes(choice)) onRespond(choice);
  };
  useShortcut("approval.allow", () => give("once"), { enabled });
  useShortcut("approval.allow.alt", () => give("once"), { enabled });
  useShortcut("approval.always", () => give("always"), { enabled });
  useShortcut("approval.deny", () => give("deny"), { enabled });
  useShortcut("approval.deny.alt", () => give("deny"), { enabled });
}
