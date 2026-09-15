"use client";

import { useState } from "react";
import { Switch } from "@/components/ui/switch";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import { useMockFeatures } from "@/lib/profile-preferences";
import { SettingsCard, SettingsRow } from "../_components/settings-card";

export default function LabsSettingsPage() {
  const { enabled, setEnabled } = useMockFeatures();
  const runtime = useRuntimeStatus();
  const [switchingTo, setSwitchingTo] = useState<string | null>(null);
  const [switchError, setSwitchError] = useState<string | null>(null);

  // A user-requested convenience coupling, NOT a semantic link: the mock tour
  // itself is browser-local and never touches the daemon. But switching the
  // preview OFF while the daemon idles on the offline mock provider usually
  // means "back to real work" — so if a real provider is configured (managed
  // mode only), the daemon is switched back to it, which restarts it.
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
    <SettingsCard title="Labs">
      <SettingsRow
        label="Show mock features"
        htmlFor="mock-features"
        description="Adds a clearly-labeled mock chat with local demo content."
      >
        <Switch
          id="mock-features"
          checked={enabled}
          onCheckedChange={onToggle}
          aria-label="Show mock features"
        />
      </SettingsRow>
      {switchingTo && (
        <p className="pt-2 text-xs text-muted-foreground">
          Switching the daemon back to {switchingTo}…
        </p>
      )}
      {switchError && (
        <p className="pt-2 text-sm text-destructive">{switchError}</p>
      )}
    </SettingsCard>
  );
}
