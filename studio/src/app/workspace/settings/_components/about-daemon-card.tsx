"use client";

import { Copy } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import { copyToClipboard } from "@/lib/clipboard";
import {
  type HarnessServerInfoProbe,
  probeHarnessServerInfo,
} from "@/lib/harness/server-info";
import { studioVersion } from "@/lib/studio-version";
import { SettingsCard } from "./settings-card";

/** How the agent-identity probe stands: its lookup class, or the two states
 *  before/without a probe. */
export type AboutLookup =
  | HarnessServerInfoProbe["lookup"]
  | "loading"
  | "offline";

/** The `Version` cell in plain words, so the card is never blank. */
function agentVersionText(
  lookup: AboutLookup,
  info: HarnessServerInfoProbe["info"],
): string {
  switch (lookup) {
    case "ok":
      return info?.buildId || "Not available";
    case "loading":
      return "Checking…";
    case "offline":
      return "The agent is offline";
    case "not-supported":
    case "unreachable":
    case "invalid-response":
      return "Not available";
  }
}

/**
 * The agent's identity next to Studio's: its version (the safe build id
 * from GET /v1/info) and whether Studio runs it (managed mode) or connects
 * to one running elsewhere (external mode). One action copies the details
 * — with Studio's own version — for a support request. The probe result,
 * the runtime status and the copy path are unchanged; only the rows and
 * words are the short set an office user needs.
 */
export function AboutDaemonCard({
  selectedProviderId,
}: {
  /** Names the provider whose identity projection the probe should read. */
  selectedProviderId?: string;
}) {
  const { state, mode } = useRuntimeStatus();
  const connected = state === "connected";
  const [probe, setProbe] = useState<HarnessServerInfoProbe | null>(null);

  useEffect(() => {
    if (!connected) {
      setProbe(null);
      return;
    }
    const controller = new AbortController();
    void probeHarnessServerInfo(selectedProviderId, controller.signal).then(
      (result) => {
        if (!controller.signal.aborted) setProbe(result);
      },
    );
    return () => controller.abort();
  }, [connected, selectedProviderId]);

  const lookup: AboutLookup =
    state === "offline" ? "offline" : (probe?.lookup ?? "loading");
  const agentVersion = agentVersionText(
    lookup,
    lookup === "ok" ? (probe?.info ?? null) : null,
  );
  const managed = mode === "managed" ? "Yes" : "No";

  const details = [
    `Studio version: ${studioVersion()}`,
    `Agent version: ${agentVersion}`,
    `Managed by Studio: ${managed}`,
  ].join("\n");

  return (
    <SettingsCard
      title="About the agent"
      description="The agent's version, and whether Studio runs it for you."
    >
      <div className="flex flex-col gap-3">
        <div className="divide-y rounded-lg border bg-background">
          <div className="flex items-center justify-between gap-3 px-4 py-3">
            <span className="text-sm">Version</span>
            <span
              className="break-all text-right text-sm text-muted-foreground"
              data-testid="about-server-build"
            >
              {agentVersion}
            </span>
          </div>
          <div className="flex items-center justify-between gap-3 px-4 py-3">
            <span className="text-sm">Managed by Studio</span>
            <span
              className="text-right text-sm text-muted-foreground"
              data-testid="about-server-mode"
            >
              {managed}
            </span>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            className="rounded-full"
            onClick={() => void copyToClipboard(details, "Details")}
          >
            <Copy className="size-4" />
            Copy details
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          Include these details when you report a problem.
        </p>
      </div>
    </SettingsCard>
  );
}
