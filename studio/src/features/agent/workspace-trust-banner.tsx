"use client";

import { ShieldAlert } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { useConfirm } from "@/hooks/use-confirm";
import {
  type DaemonSoulTrust,
  fetchDaemonSoulTrust,
  type HarnessTrustState,
  trustWorkspace,
  trustWorkspaceOnce,
} from "@/lib/harness/client";
import { useRuntimeStatus } from "./runtime-status";

/**
 * The first-encounter and drift TRUST PROMPT, as a workspace banner —
 * mecatui's pre-TUI "trust this project? [trust / trust once / no]" and its
 * "instructions changed, trust again?" re-prompt, which mecated itself never
 * asks (it is declarative). The controller keeps Studio's own trust registry
 * and reports its decision on `/status.trust`; this banner renders only when
 * (managed mode) the workspace carries something a grant would ADMIT and the
 * spawn did not get the grant — untrusted, or a remembered grant that
 * drifted — and the user has not said "Not now" for this exact authority set.
 *
 * Honesty: the controller cannot see the daemon's OWN registry (trust.yaml
 * remembered through mecatui, the user-global `trustedWorkspaces:` list). The
 * one daemon-side signal on the wire, the project soul's `trusted` bit, is
 * cross-checked so a project already admitted that way is not nagged; the
 * copy names the residual for a project without a soul.
 */

export type TrustBannerKind = "untrusted" | "drifted";

export interface TrustBannerState {
  kind: TrustBannerKind;
  /** The one-line prompt. */
  message: string;
}

export const TRUST_UNTRUSTED_MESSAGE =
  "This project ships its own instructions (soul, agents, commands, skills or allow rules). Mecatl withholds them until you trust the project.";

export const TRUST_DRIFTED_MESSAGE =
  "This project's instructions changed since you trusted it (checked when the daemon last started). Review them, then trust it again.";

/** The residual the banner states: a grant Studio cannot see. */
export const TRUST_RESIDUAL_NOTE =
  "Trusted it in mecatui or settings.yaml already? Studio cannot see that grant — trusting here makes it explicit.";

export const TRUST_REMEMBER_CONFIRM =
  "Trusting lets the repository's checked-in rules auto-approve tool calls and steer the agent (soul, agents, commands, skills, allow rules, AGENTS.md). Studio remembers it until the project's instructions change. The daemon restarts; in-flight runs end.";

export const TRUST_ONCE_CONFIRM =
  "Trusting lets the repository's checked-in rules auto-approve tool calls and steer the agent (soul, agents, commands, skills, allow rules, AGENTS.md). Nothing is saved: the grant lasts until Studio's controller restarts (not just this daemon). The daemon restarts; in-flight runs end.";

/** The per-workspace localStorage key holding the anchor "Not now" dismissed. */
export function trustDismissKey(workspace: string): string {
  return `mecatl.trust.dismissed:${workspace}`;
}

/**
 * Whether — and which — prompt to show. Pure, so the table is testable:
 *
 *   external mode / no controller trust / no authority ⇒ null
 *   trusted or once                                   ⇒ null
 *   the daemon reports a TRUSTED project soul         ⇒ null (admitted from
 *                                                        its own registry)
 *   "Not now" said for THIS anchor                    ⇒ null (a changed
 *                                                        authority set — a
 *                                                        new anchor — re-prompts)
 *   untrusted ⇒ the first-encounter prompt; drifted ⇒ the re-prompt.
 *
 * `dismissedAnchor` is null when nothing was dismissed and undefined while
 * the stored value has not been read yet (no flash before the read).
 */
export function trustBannerState({
  mode,
  trust,
  dismissedAnchor,
  daemonSoul,
}: {
  mode: "managed" | "external";
  trust: HarnessTrustState | null;
  dismissedAnchor: string | null | undefined;
  daemonSoul: DaemonSoulTrust | null;
}): TrustBannerState | null {
  if (mode !== "managed" || !trust || !trust.hasAuthority) return null;
  if (trust.decision !== "untrusted" && trust.decision !== "drifted")
    return null;
  if (daemonSoul?.projectSoul && daemonSoul.trusted) return null;
  if (dismissedAnchor === undefined) return null;
  if (dismissedAnchor !== null && dismissedAnchor === trust.anchor) return null;
  return trust.decision === "drifted"
    ? { kind: "drifted", message: TRUST_DRIFTED_MESSAGE }
    : { kind: "untrusted", message: TRUST_UNTRUSTED_MESSAGE };
}

function readDismissed(workspace: string): string | null {
  try {
    return window.localStorage.getItem(trustDismissKey(workspace));
  } catch {
    return null;
  }
}

function writeDismissed(workspace: string, anchor: string): void {
  try {
    window.localStorage.setItem(trustDismissKey(workspace), anchor);
  } catch {
    // Private mode / blocked storage: the dismissal then lasts this page load.
  }
}

export function WorkspaceTrustBanner() {
  const { connected, mode, trust, workspace, refresh } = useRuntimeStatus();
  const { confirm, ConfirmDialog } = useConfirm();
  // undefined until the stored value is read for THIS workspace label.
  const [dismissedAnchor, setDismissedAnchor] = useState<
    string | null | undefined
  >(undefined);
  const [daemonSoul, setDaemonSoul] = useState<DaemonSoulTrust | null>(null);
  const [busy, setBusy] = useState<"" | "trust" | "once">("");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setDismissedAnchor(readDismissed(workspace));
  }, [workspace]);

  // The daemon-side cross-check, re-read whenever the controller's decision
  // or anchor changes (a restart may change the daemon's own answer). The
  // key is "" while no check is wanted, so the effect has one dependency.
  const decision = trust?.decision ?? null;
  const soulCheckKey =
    connected &&
    mode === "managed" &&
    trust?.hasAuthority === true &&
    (decision === "untrusted" || decision === "drifted")
      ? `${decision}:${trust.anchor}`
      : "";
  useEffect(() => {
    if (!soulCheckKey) {
      setDaemonSoul(null);
      return;
    }
    const controller = new AbortController();
    fetchDaemonSoulTrust(controller.signal).then((result) => {
      if (!controller.signal.aborted) setDaemonSoul(result);
    });
    return () => controller.abort();
  }, [soulCheckKey]);

  const state = trustBannerState({ mode, trust, dismissedAnchor, daemonSoul });

  const grant = useCallback(
    async (kind: "trust" | "once") => {
      const confirmed = await confirm({
        title:
          kind === "trust"
            ? "Trust this project?"
            : "Trust this project for this session?",
        description:
          kind === "trust" ? TRUST_REMEMBER_CONFIRM : TRUST_ONCE_CONFIRM,
        confirmText:
          kind === "trust" ? "Trust project" : "Trust for this session",
      });
      if (!confirmed) return;
      setBusy(kind);
      setError(null);
      try {
        if (kind === "trust") await trustWorkspace();
        else await trustWorkspaceOnce();
        // The provider's 5 s probe would catch up; ask it now so the banner
        // clears as soon as the restarted daemon answers.
        await refresh();
      } catch (caught) {
        setError(caught instanceof Error ? caught.message : String(caught));
      } finally {
        setBusy("");
      }
    },
    [confirm, refresh],
  );

  const dismiss = useCallback(() => {
    if (!trust) return;
    writeDismissed(workspace, trust.anchor);
    setDismissedAnchor(trust.anchor);
  }, [trust, workspace]);

  if (!state) return null;

  return (
    <div
      role="status"
      aria-label="Project trust"
      className="flex flex-wrap items-center justify-center gap-x-3 gap-y-1.5 border-b border-amber-500/40 bg-amber-500/10 px-4 py-1.5 text-xs text-amber-700 dark:text-amber-400"
    >
      <span className="flex items-center gap-2">
        <ShieldAlert className="size-3.5 shrink-0" aria-hidden="true" />
        <span className="font-medium">{state.message}</span>
      </span>
      {state.kind === "untrusted" && (
        <span className="hidden xl:inline">{TRUST_RESIDUAL_NOTE}</span>
      )}
      <span className="flex items-center gap-1.5">
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="h-6 rounded-full border-amber-600/40 px-2.5 text-xs text-amber-800 hover:bg-amber-500/20 dark:text-amber-300"
          disabled={busy !== ""}
          onClick={() => void grant("trust")}
        >
          {busy === "trust" ? "Trusting…" : "Trust project"}
        </Button>
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="h-6 rounded-full border-amber-600/40 px-2.5 text-xs text-amber-800 hover:bg-amber-500/20 dark:text-amber-300"
          disabled={busy !== ""}
          onClick={() => void grant("once")}
        >
          {busy === "once" ? "Trusting…" : "Trust for this session"}
        </Button>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="h-6 rounded-full px-2.5 text-xs text-amber-800 hover:bg-amber-500/20 dark:text-amber-300"
          disabled={busy !== ""}
          onClick={dismiss}
        >
          Not now
        </Button>
      </span>
      {error && (
        <span role="alert" className="basis-full text-center text-destructive">
          {error}
        </span>
      )}
      {ConfirmDialog}
    </div>
  );
}
