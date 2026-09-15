"use client";

import { useHarnessRuntime } from "@/features/agent/hooks/use-harness-runtime";
import { useProviderManagement } from "@/features/agent/hooks/use-provider-management";
import { AboutDaemonCard } from "../_components/about-daemon-card";
import { OidcLoginCard } from "../_components/oidc-login-card";
import { ProviderSection } from "../_components/provider-section";
import { RuntimeStatusLine } from "../_components/runtime-status-line";

export default function ProviderSettingsPage() {
  const runtime = useHarnessRuntime();
  const management = useProviderManagement();
  return (
    <>
      <RuntimeStatusLine runtime={runtime} />
      <ProviderSection runtime={runtime} management={management} />
      <AboutDaemonCard
        selectedProviderId={runtime.status?.selectedProvider ?? undefined}
      />
      {/* Remote-daemon login (H3): only external mode authenticates upstream,
          and the card itself explains a half-configured issuer. */}
      {runtime.mode === "external" && <OidcLoginCard />}
    </>
  );
}
