"use client";

import { Ellipsis } from "lucide-react";
import Link from "next/link";
import { useState } from "react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import type { useHarnessRuntime } from "@/features/agent/hooks/use-harness-runtime";
import type {
  ProviderKeyHealth,
  useProviderManagement,
} from "@/features/agent/hooks/use-provider-management";
import type { HarnessProviderInfo } from "@/lib/harness/client";
import { cn } from "@/lib/utils";
import { AddProviderDialog } from "./add-provider-dialog";
import {
  ExternalManagedNote,
  Note,
  OfflineNote,
  SettingsCard,
} from "./settings-card";

type Runtime = ReturnType<typeof useHarnessRuntime>;
type Management = ReturnType<typeof useProviderManagement>;

/** Dot color + label for a row's key health. Green = a test passed, red =
 *  the provider rejected the key, amber = the test could not complete, gray
 *  = untested (or no key in the block yet). */
function healthPresentation(
  row: HarnessProviderInfo,
  health: ProviderKeyHealth | undefined,
): { dot: string; label: string; detail?: string } {
  if (!row.keyPresent) {
    return { dot: "bg-muted-foreground/50", label: "no key in block" };
  }
  switch (health?.state) {
    case "ok":
      return { dot: "bg-emerald-500", label: "key OK" };
    case "rejected":
      return {
        dot: "bg-red-500",
        label: "key rejected",
        detail: health.detail,
      };
    case "error":
      return {
        dot: "bg-amber-500",
        label: "test failed",
        detail: health.detail,
      };
    default:
      return { dot: "bg-muted-foreground/50", label: "untested" };
  }
}

/**
 * Provider management, SERVER-MEDIATED end to end (Studio rule 3):
 * credentials never cross the browser/controller boundary, so there is no
 * key input anywhere on this surface — not on add, not on test, not on
 * remove. The controller owns auth.yaml server-side: it reports names and
 * key-present booleans, key-tests a STORED key with one bounded outbound
 * call (only the verdict reaches the browser), and removes a block with a
 * conservative line-range cut. Adding a provider is a guided copy of a
 * snippet (a `<YOUR_KEY>` placeholder) into auth.yaml on the daemon's
 * machine, then a re-check + restart. Every mutation restarts the daemon and
 * confirms first; external mode disables all of it (the controller answers
 * 409 there anyway).
 */
export function ProviderSection({
  runtime,
  management,
}: {
  runtime: Runtime;
  management: Management;
}) {
  const status = runtime.status;
  const [removing, setRemoving] = useState<HarnessProviderInfo | null>(null);

  const modelsFor = (name: string) =>
    runtime.models.filter((model) => model.providerId === name).length;

  // management.load() only re-reads the auth.yaml inventory; runtime.status
  // (the top card's provider/running/selectedProvider) is a SEPARATE poll
  // owned by useHarnessRuntime, so a mutation that restarts the daemon must
  // explicitly refresh it too or the card shows the pre-mutation provider.
  const activateProvider = (kind: string) =>
    management.setActiveProvider(kind).then(() => runtime.refresh());
  const removeProvider = (name: string) =>
    management.removeProvider(name).then(() => runtime.refresh());

  return (
    <SettingsCard title="Model provider">
      {!runtime.live ? (
        <OfflineNote />
      ) : status === null ? (
        <Note>Reading the controller&rsquo;s status…</Note>
      ) : (
        <div className="flex flex-col gap-3">
          {runtime.mode === "external" ? (
            <ExternalManagedNote />
          ) : (
            <>
              {management.error && (
                <p className="whitespace-pre-wrap text-sm text-destructive">
                  {management.error}
                </p>
              )}
              {management.notice && (
                <p className="text-sm text-muted-foreground">
                  {management.notice}
                </p>
              )}

              <ul className="divide-y overflow-hidden rounded-lg border">
                {management.providers.map((row) => (
                  <ProviderRow
                    key={row.name}
                    row={row}
                    active={row.name === status.selectedProvider}
                    running={status.running}
                    modelCount={modelsFor(row.name)}
                    health={management.health[row.name]}
                    busy={management.busy}
                    onTest={() => void management.testKey(row.name)}
                    onActivate={() => void activateProvider(row.name)}
                    onRemove={() => setRemoving(row)}
                  />
                ))}
              </ul>
              <div className="flex justify-start">
                <AddProviderDialog
                  known={management.known}
                  configured={management.providers.map((p) => p.name)}
                  authFile={status.authFile}
                  operatorSettings={status.operatorSettings}
                  reload={management.reload}
                  restartDaemon={management.restartDaemon}
                  restarting={management.busy === "restart"}
                />
              </div>
            </>
          )}
        </div>
      )}

      <AlertDialog
        open={removing !== null}
        onOpenChange={(open) => !open && setRemoving(null)}
      >
        {removing && (
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Remove {removing.name}?</AlertDialogTitle>
              <AlertDialogDescription>
                Its block — key included — is removed from auth.yaml on the
                daemon&rsquo;s machine, and the daemon restarts: in-flight runs
                and session ids die with it.
                {removing.name === status?.selectedProvider &&
                  " This is the SELECTED provider (MECATL_STUDIO_PROVIDER names it) — the daemon will fail to restart until the variable changes or the key returns."}
                {management.providers.length === 1 &&
                  " It is also the only configured provider: mecated will come back on the offline mock."}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction
                onClick={() => {
                  void removeProvider(removing.name);
                  setRemoving(null);
                }}
              >
                Remove
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        )}
      </AlertDialog>
    </SettingsCard>
  );
}

/**
 * One provider row: health dot, mono name (a real anchor to its models
 * subpage), key-health label, model count, and the actions kebab. The
 * whole row is a Link so click-through works everywhere; kebab clicks stop
 * propagation. The built-in mock is deliberately NOT listed — it is the
 * daemon's silent fallback (and the Labs demo target), not a provider the
 * user manages here.
 */
function ProviderRow({
  row,
  active,
  running,
  modelCount,
  health,
  busy,
  onTest,
  onActivate,
  onRemove,
}: {
  row: HarnessProviderInfo;
  active: boolean;
  running: boolean;
  modelCount: number;
  health: ProviderKeyHealth | undefined;
  busy: string;
  onTest: () => void;
  onActivate: () => void;
  onRemove: () => void;
}) {
  const presentation = healthPresentation(row, health);
  const testing = busy === `test:${row.name}`;
  const activating = busy === `activate:${row.name}`;
  const removingBusy = busy === `remove:${row.name}`;
  const href = `/workspace/provider/${encodeURIComponent(row.name)}`;

  return (
    <li className="flex items-center gap-3 px-4 py-3">
      <span
        aria-hidden="true"
        className={cn("size-2 shrink-0 rounded-full", presentation.dot)}
        title={presentation.detail}
      />
      <Link href={href} className="min-w-0 flex-1">
        <span className="flex flex-wrap items-center gap-x-2">
          <span className="truncate font-mono text-sm font-medium hover:underline">
            {row.name}
          </span>
          {active && <Badge variant="info">active</Badge>}
          {active && (
            <Badge variant={running ? "default" : "secondary"}>
              {running ? "running" : "stopped"}
            </Badge>
          )}
        </span>
        <span
          className="block truncate text-xs text-muted-foreground"
          title={presentation.detail}
        >
          {presentation.label}
          {presentation.detail ? ` — ${presentation.detail}` : ""}
          {" · "}
          {modelCount} model{modelCount === 1 ? "" : "s"}
          {" · "}
          {row.source}
        </span>
      </Link>
      <DropdownMenu modal={false}>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            className="size-8 shrink-0"
            aria-label={`Actions for ${row.name}`}
          >
            <Ellipsis className="size-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem
            disabled={!row.testable || !row.keyPresent || testing}
            title={
              row.testable
                ? row.keyPresent
                  ? undefined
                  : "No key in the block to test"
                : "Key testing is not supported for this provider"
            }
            onClick={onTest}
          >
            {testing ? "Testing key…" : "Test key"}
          </DropdownMenuItem>
          <DropdownMenuItem
            disabled={active || !row.keyPresent || activating}
            title={
              active
                ? undefined
                : !row.keyPresent
                  ? "No key in the block to activate"
                  : undefined
            }
            onClick={onActivate}
          >
            {activating ? "Switching…" : "Set as active"}
          </DropdownMenuItem>
          <DropdownMenuItem asChild>
            <Link href={href}>View models</Link>
          </DropdownMenuItem>
          <DropdownMenuItem
            variant="destructive"
            disabled={removingBusy}
            onClick={onRemove}
          >
            {removingBusy ? "Removing…" : "Remove"}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </li>
  );
}
