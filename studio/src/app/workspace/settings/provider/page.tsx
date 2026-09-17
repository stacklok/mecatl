"use client";

import { useDaemonDefaults } from "@/features/agent/hooks/use-daemon-defaults";
import { useHarnessRuntime } from "@/features/agent/hooks/use-harness-runtime";
import { useProviderManagement } from "@/features/agent/hooks/use-provider-management";
import { useProviderStatus } from "@/features/agent/hooks/use-provider-status";
import { DaemonDefaultsCard } from "../_components/daemon-defaults-card";
import { OidcLoginCard } from "../_components/oidc-login-card";
import { ProviderSection } from "../_components/provider-section";
import { RuntimeStatusLine } from "../_components/runtime-status-line";

export default function ProviderSettingsPage() {
  const runtime = useHarnessRuntime();
  const management = useProviderManagement();
  // The saved defaults document (the default model per provider).
  const daemonDefaults = useDaemonDefaults();
  // The agent's own per-provider status, read-only in every mode: merged
  // into the rows in managed mode, listed on its own in external mode.
  const providerStatus = useProviderStatus();
  return (
    <>
      <RuntimeStatusLine runtime={runtime} />
      <ProviderSection
        runtime={runtime}
        management={management}
        providerStatus={providerStatus}
      />
      <DaemonDefaultsCard runtime={runtime} defaults={daemonDefaults} />
      {/* Only an external deployment signs in upstream. */}
      {runtime.mode === "external" && <OidcLoginCard />}
    </>
  );
}
