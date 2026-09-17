"use client";

import { useState } from "react";
import { Switch } from "@/components/ui/switch";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import { useDeveloperTools, useMockFeatures } from "@/lib/profile-preferences";
import { providerLabel } from "@/lib/provider-label";
import { SettingsCard, SettingsRow } from "../_components/settings-card";

export default function LabsSettingsPage() {
  const { enabled, setEnabled } = useMockFeatures();
  const { enabled: developerTools, setEnabled: setDeveloperTools } =
    useDeveloperTools();
  const runtime = useRuntimeStatus();
  const [switchingTo, setSwitchingTo] = useState<string | null>(null);
  const [switchError, setSwitchError] = useState<string | null>(null);

  // A user-requested convenience coupling, NOT a semantic link: the demo chat
  // itself is browser-local and never touches the agent. But switching it OFF
  // while the agent idles on the offline mock provider usually means "back to
  // real work" — so if a real provider is configured (managed mode only), the
  // agent is switched back to it, which restarts it.
  const onToggle = (next: boolean) => {
    setEnabled(next);
    if (next || switchingTo) return;
    const target = runtime.configuredProviders[0];
    if (runtime.mode !== "managed" || !runtime.isMock || !target) return;
    setSwitchError(null);
    setSwitchingTo(target);
    void runtime
      .switchProvider(target)
      .catch((caught) =>
        setSwitchError(
          caught instanceof Error ? caught.message : String(caught),
        ),
      )
      .finally(() => setSwitchingTo(null));
  };

  return (
    <SettingsCard
      title="Labs"
      description="Optional features that are still being tested."
    >
      <div className="divide-y divide-border/60">
        <SettingsRow
          label="Show demo chat"
          htmlFor="mock-features"
          description="Adds a sample chat you can explore without sending anything."
        >
          <Switch
            id="mock-features"
            checked={enabled}
            onCheckedChange={onToggle}
            aria-label="Show demo chat"
          />
        </SettingsRow>
        <SettingsRow
          label="Developer tools"
          htmlFor="developer-tools"
          description="Adds testing commands and technical details to the chat."
        >
          <Switch
            id="developer-tools"
            checked={developerTools}
            onCheckedChange={setDeveloperTools}
            aria-label="Developer tools"
          />
        </SettingsRow>
      </div>
      {switchingTo && (
        <p className="pt-2 text-xs text-muted-foreground">
          Switching the agent back to {providerLabel(switchingTo)}…
        </p>
      )}
      {switchError && (
        <p className="pt-2 text-sm text-destructive">{switchError}</p>
      )}
    </SettingsCard>
  );
}
