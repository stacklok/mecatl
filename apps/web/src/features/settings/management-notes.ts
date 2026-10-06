// SPDX-License-Identifier: Apache-2.0

import type { GetRuntimeSettingsResponse } from "@mecatl-studio/contracts/generated";

type Management = GetRuntimeSettingsResponse["management"];

/**
 * The deployment-managed controls this browser cannot use, each with the
 * reason the BFF gives. An editable surface contributes no note.
 */
export function managementNotes(management: Management): string[] {
  const notes: string[] = [];
  if (!management.providerConfiguration) {
    notes.push(
      management.providerConfigurationReason ||
        "Provider configuration is managed by the deployment.",
    );
  }
  if (!management.routingConfiguration) {
    notes.push(
      management.routingConfigurationReason || "Model routing is managed by the deployment.",
    );
  }
  return notes;
}
