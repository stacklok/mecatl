"use client";

import { useHarnessRuntime } from "@/features/agent/hooks/use-harness-runtime";
import { AboutDaemonCard } from "../_components/about-daemon-card";
import { AboutStudioCard } from "../_components/about-studio-card";

/**
 * Settings → About: Studio's version with the documentation, support and
 * keyboard-shortcut links, then the agent's version and whether Studio runs
 * it. Read-only by design — nothing here is a setting.
 */
export default function HelpSettingsPage() {
  const runtime = useHarnessRuntime();
  return (
    <>
      <AboutStudioCard />
      <AboutDaemonCard
        selectedProviderId={runtime.status?.selectedProvider ?? undefined}
      />
    </>
  );
}
