"use client";

import { useId } from "react";
import { Switch } from "@/components/ui/switch";
import { useDiagnosticsOptions } from "@/features/agent/hooks/use-diagnostics-options";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import { useConfirm } from "@/hooks/use-confirm";
import {
  Note,
  OfflineNote,
  RESTART_SENTENCE,
  SettingsCard,
  SettingsRow,
} from "./settings-card";

const TITLE = "Usage statistics";

const CARD_DESCRIPTION =
  "Help improve the agent by sharing anonymous usage statistics.";

/** What is shared, in plain words: counts, never content. */
export const SHARED_NOTE =
  "Only counts of which features are used — never your chats, files or names.";

/** The computer the agent runs on opts out, so the switch cannot turn the
 *  statistics back on from here. */
export const ENVIRONMENT_OPT_OUT_NOTE =
  "Turned off on this computer, so it can't be changed here.";

export const RESTART_NOTE = RESTART_SENTENCE;

/** Shown when the agent runs elsewhere: its setting lives there. */
export const EXTERNAL_PRODUCT_METRICS_NOTE =
  "Usage statistics are set where the agent runs and can't be changed here.";

/** Shown when the controller answered but had no setting to report. */
export const OPTIONS_UNAVAILABLE_NOTE =
  "This setting is not available right now. Restart Studio and try again.";

/** Shown after a save stood: every save from this card restarts the agent. */
export const SAVED_NOTICE = "Saved. The agent restarted.";

/**
 * The anonymous usage-statistics switch. Managed mode shows the effective
 * verdict the controller derives (its saved switch AND no opt-out in the
 * environment the agent inherits) and lets the person turn the statistics
 * off or on, restart-confirmed. An environment opt-out wins and is
 * explained rather than fought. When the agent runs elsewhere the setting
 * lives there.
 */
export function ProductMetricsCard() {
  const { connected, mode } = useRuntimeStatus();
  const diagnostics = useDiagnosticsOptions();
  const { confirm, ConfirmDialog } = useConfirm();
  const enabledId = useId();

  if (mode === "external") {
    return (
      <SettingsCard title={TITLE}>
        <Note>{EXTERNAL_PRODUCT_METRICS_NOTE}</Note>
      </SettingsCard>
    );
  }

  const options = diagnostics.options;
  if (!options) {
    return (
      <SettingsCard title={TITLE}>
        {diagnostics.isLoading ? (
          <Note>Loading…</Note>
        ) : !connected ? (
          <OfflineNote />
        ) : (
          <Note>{diagnostics.error ?? OPTIONS_UNAVAILABLE_NOTE}</Note>
        )}
      </SettingsCard>
    );
  }

  const saved = options.productMetrics;
  const environmentOptOut =
    diagnostics.productMetrics?.source === "environment";
  const checked = environmentOptOut ? false : saved.enabled;

  const toggleEnabled = async (next: boolean) => {
    if (environmentOptOut || next === saved.enabled) return;
    const confirmed = await confirm({
      title: next ? "Turn usage statistics on?" : "Turn usage statistics off?",
      description: RESTART_NOTE,
      confirmText: "Save and restart",
    });
    if (!confirmed) return;
    await diagnostics.save({
      productMetrics: { ...saved, enabled: next },
    });
  };

  return (
    <SettingsCard title={TITLE} description={CARD_DESCRIPTION}>
      <div className="flex flex-col gap-4">
        {diagnostics.error && (
          <p className="whitespace-pre-wrap text-sm text-destructive">
            {diagnostics.error}
          </p>
        )}
        {diagnostics.notice && (
          <p className="text-sm text-muted-foreground" role="status">
            {SAVED_NOTICE}
          </p>
        )}

        <div className="divide-y divide-border/60">
          <SettingsRow
            label="Share anonymous usage statistics"
            htmlFor={enabledId}
            description={
              environmentOptOut ? ENVIRONMENT_OPT_OUT_NOTE : SHARED_NOTE
            }
          >
            <Switch
              id={enabledId}
              checked={checked}
              disabled={diagnostics.busy || environmentOptOut}
              onCheckedChange={(next) => void toggleEnabled(next)}
            />
          </SettingsRow>
        </div>
      </div>
      {ConfirmDialog}
    </SettingsCard>
  );
}
