// SPDX-License-Identifier: Apache-2.0

import { Note, SettingsCard } from "@/features/settings/settings-card";

/**
 * Settings → Learning → Learning. The prototype's editable "Learning mode" and
 * "Sensitivity" pair stays out: the deployment owns that configuration, and
 * neither the BFF nor SDK 0.4.0 reports the daemon's effective mode or
 * sensitivity, so there is nothing true to show even as a read-only fact. The
 * card names who manages it instead. Settings only renders this section while
 * the runtime answers, so the prototype's offline variant is not needed here.
 */
export function LearningModeSection() {
  return (
    <SettingsCard title="Learning">
      <Note>Learning configuration is managed by this deployment and is read-only here.</Note>
    </SettingsCard>
  );
}
