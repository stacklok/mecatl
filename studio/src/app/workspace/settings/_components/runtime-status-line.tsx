"use client";

import { Info, Loader2 } from "lucide-react";
import type { useHarnessRuntime } from "@/features/agent/hooks/use-harness-runtime";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import { useDeveloperTools } from "@/lib/profile-preferences";

/**
 * The shared status strip for the runtime subpages: load/busy state and the
 * one error/notice channel config writes report through. There is no manual
 * Refresh control — every surface re-reads after each action (and the
 * runtime provider re-probes on its own), so the strip renders nothing when
 * there is nothing to report.
 */
export function RuntimeStatusLine({
  runtime,
}: {
  runtime: ReturnType<typeof useHarnessRuntime>;
}) {
  const { deployment } = useRuntimeStatus();
  const { enabled: developerTools } = useDeveloperTools();
  // The operator's deployment label is a technical detail: shown only once
  // Labs → Developer tools is on, for people running more than one agent.
  const deploymentLabel = developerTools && deployment ? deployment : null;
  const working = runtime.isLoading || Boolean(runtime.busy);
  if (!working && !runtime.error && !runtime.notice && !deploymentLabel)
    return null;
  return (
    <div className="space-y-2">
      {working && (
        <div className="flex justify-end">
          <Loader2 className="size-3.5 animate-spin text-muted-foreground" />
        </div>
      )}
      {runtime.error && (
        <p className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
          {runtime.error}
        </p>
      )}
      {runtime.notice && (
        <div className="flex items-start gap-2 rounded-lg border border-sky-500/30 bg-sky-500/5 px-3 py-2 text-sm">
          <Info
            aria-hidden="true"
            className="mt-0.5 size-4 shrink-0 text-sky-600 dark:text-sky-400"
          />
          <span>{runtime.notice}</span>
        </div>
      )}
      {deploymentLabel && (
        <p className="text-xs text-muted-foreground">
          Agent: <span className="font-mono">{deploymentLabel}</span>
        </p>
      )}
    </div>
  );
}
