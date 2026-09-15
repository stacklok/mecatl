"use client";

import { TriangleAlert } from "lucide-react";
import Link from "next/link";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { useRuntimeStatus } from "@/features/agent/runtime-status";

/**
 * Warns above the composer when the daemon is answering from the offline
 * mock rather than a real model, with a one-click way off it: switch
 * straight to the sole other configured provider when there is exactly one
 * unambiguous choice, otherwise link to the provider settings page. External
 * mode owns its own provider (the deployment, not this UI), so it renders
 * nothing there.
 */
export function MockProviderNotice() {
  const {
    mode,
    isMock,
    configuredProviders,
    toolhiveAvailable,
    switchProvider,
  } = useRuntimeStatus();
  const [switching, setSwitching] = useState(false);
  const [switchError, setSwitchError] = useState<string | null>(null);

  if (mode !== "managed" || !isMock) return null;

  const quickTarget =
    configuredProviders.length === 1 && !toolhiveAvailable
      ? configuredProviders[0]
      : configuredProviders.length === 0 && toolhiveAvailable
        ? "toolhive"
        : null;

  const handleSwitch = async () => {
    if (!quickTarget) return;
    setSwitching(true);
    setSwitchError(null);
    try {
      await switchProvider(quickTarget);
    } catch (caught) {
      setSwitchError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setSwitching(false);
    }
  };

  return (
    <div className="flex flex-col gap-1.5 rounded-lg border border-amber-500/40 bg-background bg-gradient-to-b from-amber-500/5 to-amber-500/5 px-3 py-2">
      <div className="flex items-center gap-2">
        <TriangleAlert className="size-4 shrink-0 text-amber-600 dark:text-amber-400" />
        <p className="min-w-0 flex-1 text-sm text-amber-800 dark:text-amber-400">
          Running on the offline mock provider — replies are canned, not from a
          real model.
        </p>
        {quickTarget ? (
          <Button
            size="sm"
            variant="outline"
            className="h-7 shrink-0 border-amber-500/40 text-amber-800 hover:bg-amber-500/10 dark:text-amber-400"
            disabled={switching}
            onClick={handleSwitch}
          >
            {switching
              ? "Switching…"
              : `Switch to ${quickTarget === "toolhive" ? "ToolHive" : quickTarget}`}
          </Button>
        ) : (
          <Button
            size="sm"
            variant="outline"
            className="h-7 shrink-0 border-amber-500/40 text-amber-800 hover:bg-amber-500/10 dark:text-amber-400"
            asChild
          >
            <Link href="/workspace/settings/provider">Switch provider</Link>
          </Button>
        )}
      </div>
      {switchError && <p className="text-xs text-destructive">{switchError}</p>}
    </div>
  );
}
