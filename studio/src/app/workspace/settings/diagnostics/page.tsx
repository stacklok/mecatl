"use client";

import { useHarnessRuntime } from "@/features/agent/hooks/use-harness-runtime";
import { AboutDaemonCard } from "../_components/about-daemon-card";
import { DaemonLogCard } from "../_components/daemon-log-card";
import { ProductMetricsCard } from "../_components/product-metrics-card";
import { RuntimeStatusLine } from "../_components/runtime-status-line";

/**
 * Settings → Diagnostics: what a person needs when something goes wrong or
 * when reporting a problem — the agent's recent log lines (with a download
 * of the full file), the anonymous usage-statistics switch, and the About
 * rows. The safety level is shown and changed on the Permissions page.
 */
export default function DiagnosticsSettingsPage() {
  const runtime = useHarnessRuntime();
  return (
    <>
      <RuntimeStatusLine runtime={runtime} />
      <DaemonLogCard />
      <ProductMetricsCard />
      <AboutDaemonCard
        selectedProviderId={runtime.status?.selectedProvider ?? undefined}
      />
    </>
  );
}
