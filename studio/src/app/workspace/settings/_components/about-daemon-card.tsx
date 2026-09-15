"use client";

import { Copy } from "lucide-react";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import {
  fetchHarnessServerInfo,
  type HarnessServerInfo,
} from "@/lib/harness/server-info";
import { SettingsCard } from "./settings-card";

/**
 * The daemon's safe identity (ADR 0245): opaque build id, composition
 * family, the sanitized endpoint of the selected provider, and the
 * operator's deployment label — the only daemon-side identity available in
 * external mode, and the payload a bug report wants. Renders nothing against
 * an older daemon (the probe 404s).
 */
export function AboutDaemonCard({
  selectedProviderId,
}: {
  /** Names the provider whose sanitized endpoint the probe should project. */
  selectedProviderId?: string;
}) {
  const { connected, deployment } = useRuntimeStatus();
  const [info, setInfo] = useState<HarnessServerInfo | null>(null);

  useEffect(() => {
    if (!connected) return;
    const controller = new AbortController();
    fetchHarnessServerInfo(selectedProviderId, controller.signal)
      .then((doc) => {
        if (!controller.signal.aborted) setInfo(doc);
      })
      .catch(() => {
        if (!controller.signal.aborted) setInfo(null);
      });
    return () => controller.abort();
  }, [connected, selectedProviderId]);

  if (!info) return null;

  const rows: [string, string][] = [];
  if (info.buildId) rows.push(["Build", info.buildId]);
  if (info.serverImplementation)
    rows.push(["Implementation", info.serverImplementation]);
  if (info.providerEndpoint)
    rows.push(["Provider endpoint", info.providerEndpoint]);
  if (deployment) rows.push(["Deployment", deployment]);

  const debugBlob = rows.map(([k, v]) => `${k}: ${v}`).join("\n");

  return (
    <SettingsCard title="About this daemon">
      <div className="flex flex-col gap-3">
        <div className="divide-y rounded-lg border bg-background">
          {rows.map(([label, value]) => (
            <div
              key={label}
              className="flex items-center justify-between gap-3 px-4 py-3"
            >
              <span className="text-sm">{label}</span>
              <span className="break-all text-right font-mono text-sm text-muted-foreground">
                {value}
              </span>
            </div>
          ))}
        </div>
        <div className="flex justify-start">
          <Button
            variant="outline"
            size="sm"
            className="rounded-full"
            onClick={() => {
              void navigator.clipboard
                .writeText(debugBlob)
                .then(() => toast.success("Debug info copied"))
                .catch(() => toast.error("Couldn't copy — clipboard blocked"));
            }}
          >
            <Copy className="size-4" />
            Copy debug info
          </Button>
        </div>
      </div>
    </SettingsCard>
  );
}
