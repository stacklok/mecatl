"use client";

import { useState } from "react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Switch } from "@/components/ui/switch";
import { useRuntimeSettings } from "@/features/agent/hooks/use-runtime-settings";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import {
  ExternalManagedNote,
  Note,
  OfflineNote,
  RESTART_SENTENCE,
  SettingsCard,
  SettingsRow,
} from "./settings-card";

/** The row's visible label; the switch's aria-label repeats it. */
const STEER_LABEL = "Take messages while working";

/**
 * The row's description in plain words, plus ONE line when the daemon's
 * LIVE word on it (`capabilities.steer`) differs from the saved switch —
 * a restart still in flight, or an operator `steer: false` keeping it off.
 * Silent when the daemon reports nothing or agrees.
 */
export function steerRowDescription(
  liveSteer: unknown,
  enabled: boolean,
): string {
  const base = "Let the agent take your messages while it is still working.";
  if (typeof liveSteer !== "boolean" || liveSteer === enabled) return base;
  return `${base} Right now this is ${liveSteer ? "on" : "off"}.`;
}

/**
 * Settings → Agent: the managed DAEMON's runtime behaviour — the operator
 * half of the TUI's steer opt-out (`mecated --no-steer` / `steer: false`).
 * Off, the controller respawns mecated with `--no-steer`, the daemon reports
 * `capabilities.steer: false`, and EVERY client's mid-run messages queue
 * instead of steering. Distinct from the browser-local "Queue only" Enter
 * preference on Settings → Personalize, which silences steering for this
 * browser alone.
 *
 * Managed mode only: external mode renders the managed note (the deployment
 * owns its flags — the controller answers 409); offline renders the offline
 * note. A `steer: false` in the operator's own settings.yaml already keeps
 * steering off whatever Studio passes (`--no-steer` can only tighten), so
 * the row then reads Off with no switch and says why. Every flip RESTARTS
 * the daemon — in-flight runs end — so the switch confirms first. The copy
 * is written for people who are not developers: "the agent", never the
 * daemon or its flags.
 */
export function RuntimeBehaviourSection() {
  const { live, manageable, doc, isLoading, busy, error, notice, save } =
    useRuntimeSettings();
  const { serverCapabilities } = useRuntimeStatus();
  // The flip awaiting confirmation (the value the switch would take).
  const [pending, setPending] = useState<boolean | null>(null);

  const inheritedOff = doc?.inherited.steer === false;
  const enabled = doc?.config.steer.enabled ?? true;

  let body: React.ReactNode;
  if (!live) {
    body = <OfflineNote />;
  } else if (!manageable) {
    body = <ExternalManagedNote />;
  } else if (!doc) {
    body = (
      <Note>
        {isLoading
          ? "Reading the agent’s settings…"
          : (error ?? "These settings could not be read right now.")}
      </Note>
    );
  } else {
    body = (
      <>
        <div className="divide-y divide-border/60">
          <SettingsRow
            label={STEER_LABEL}
            htmlFor={inheritedOff ? undefined : "daemon-steer"}
            description={steerRowDescription(serverCapabilities.steer, enabled)}
          >
            {inheritedOff ? (
              <span className="text-sm text-muted-foreground">Off</span>
            ) : (
              <Switch
                id="daemon-steer"
                checked={enabled}
                disabled={busy !== ""}
                onCheckedChange={(next) => setPending(next)}
                aria-label={STEER_LABEL}
              />
            )}
          </SettingsRow>
        </div>
        {inheritedOff ? (
          <div className="mt-3">
            <Note>
              This was turned off where the agent runs, so it can&rsquo;t be
              changed here.
            </Note>
          </div>
        ) : null}
        {error ? (
          <p role="alert" className="mt-3 text-sm text-destructive">
            {error}
          </p>
        ) : null}
        {notice ? (
          <p role="status" className="mt-3 text-sm text-muted-foreground">
            {notice}
          </p>
        ) : null}
        <AlertDialog
          open={pending !== null}
          onOpenChange={(open) => !open && setPending(null)}
        >
          {pending !== null && (
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>
                  {pending ? "Turn this on?" : "Turn this off?"}
                </AlertDialogTitle>
                <AlertDialogDescription>
                  {RESTART_SENTENCE}
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>Cancel</AlertDialogCancel>
                <AlertDialogAction
                  onClick={() => {
                    void save({ steer: { enabled: pending } });
                    setPending(null);
                  }}
                >
                  Save and restart
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          )}
        </AlertDialog>
      </>
    );
  }

  return (
    <SettingsCard
      title="Agent behaviour"
      description="Applies to everyone who uses this agent."
    >
      {body}
    </SettingsCard>
  );
}
