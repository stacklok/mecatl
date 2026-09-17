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
import type { useProviderStatus } from "@/features/agent/hooks/use-provider-status";
import type {
  HarnessProviderInfo,
  HarnessProviderStatus,
} from "@/lib/harness/client";
import { providerLabel } from "@/lib/provider-label";
import { cn } from "@/lib/utils";
import { AddProviderDialog } from "./add-provider-dialog";
import { groupProviderRows } from "./provider-inventory";
import {
  ExternalManagedNote,
  Note,
  OfflineNote,
  RESTART_SENTENCE,
  SettingsCard,
} from "./settings-card";

type Runtime = ReturnType<typeof useHarnessRuntime>;
type Management = ReturnType<typeof useProviderManagement>;
type ProviderStatus = ReturnType<typeof useProviderStatus>;

/** The gateway row: its lifecycle is not managed here, so it lists only
 *  once it can be used (reachable) or is already the one in use. */
function isGatewayRow(row: HarnessProviderInfo): boolean {
  return row.class === "external" || row.name === "toolhive";
}

/** Dot color + one plain status for a row. Green = the key was checked and
 *  works (or the gateway answers), red = the provider rejected the key,
 *  amber = the check could not complete, gray = not checked yet. */
function keyPresentation(
  row: HarnessProviderInfo,
  health: ProviderKeyHealth | undefined,
): { dot: string; label: string } {
  if (isGatewayRow(row)) {
    return row.reachable === true
      ? { dot: "bg-emerald-500", label: "Ready" }
      : { dot: "bg-muted-foreground/50", label: "Not reachable" };
  }
  if (row.authMethod === "none") {
    return { dot: "bg-emerald-500", label: "No key needed" };
  }
  if (!row.keyPresent) {
    return { dot: "bg-muted-foreground/50", label: "No key added" };
  }
  switch (health?.state) {
    case "ok":
      return { dot: "bg-emerald-500", label: "Key works" };
    case "rejected":
      return { dot: "bg-red-500", label: "Key rejected" };
    case "error":
      return { dot: "bg-amber-500", label: "Could not check the key" };
    default:
      return { dot: "bg-muted-foreground/50", label: "Key added" };
  }
}

/** The agent's own verdict on a provider as one plain sentence; null when
 *  it is fine (a healthy provider owes no extra line). */
function providerProblem(
  status: HarnessProviderStatus | null | undefined,
): string | null {
  if (!status || status.state === "ok") return null;
  switch (status.state) {
    case "unauthorized":
      return "The provider did not accept the key.";
    case "unreachable":
      return "The provider could not be reached.";
    case "empty":
      return "The provider lists no models.";
    default:
      return "The provider is not available right now.";
  }
}

const modelCountText = (count: number) =>
  `${count} model${count === 1 ? "" : "s"}`;

/**
 * The providers the agent can use. Server-mediated end to end: there is no
 * key input anywhere on this surface — adding a provider copies a snippet
 * into the agent's own key file, checking a key runs on the server and only
 * the verdict comes back, and removing cuts the provider from that file.
 * Every change restarts the agent and confirms first; an external
 * deployment manages its own providers, so the list is read-only there.
 */
export function ProviderSection({
  runtime,
  management,
  providerStatus,
}: {
  runtime: Runtime;
  management: Management;
  /** The agent's per-provider status, merged into each row in managed mode
   *  and listed read-only in external mode. Optional: without it no status
   *  line renders. */
  providerStatus?: ProviderStatus;
}) {
  const status = runtime.status;
  const [removing, setRemoving] = useState<HarnessProviderInfo | null>(null);

  const modelsFor = (name: string) =>
    runtime.models.filter((model) => model.providerId === name).length;

  // management.load() only re-reads the provider inventory; runtime.status
  // (which provider is active) is a separate poll, so a change that restarts
  // the agent refreshes both.
  const refreshAll = async () => {
    await Promise.all([runtime.refresh(), providerStatus?.refresh()]);
  };
  const activateProvider = (kind: string) =>
    management.setActiveProvider(kind).then(refreshAll);
  const removeProvider = (name: string) =>
    management.removeProvider(name, "all").then(refreshAll);
  const groups = groupProviderRows(management.providers);
  const gateway =
    groups.toolhive &&
    (groups.toolhive.reachable === true ||
      groups.toolhive.active === true ||
      status?.selectedProvider === "toolhive")
      ? groups.toolhive
      : null;
  const rows = gateway ? [...groups.configured, gateway] : groups.configured;
  const statusFor = (name: string) => providerStatus?.forProvider(name) ?? null;

  return (
    <SettingsCard
      title="Providers"
      description="The services the agent uses to answer."
    >
      {!runtime.live ? (
        <OfflineNote />
      ) : status === null ? (
        <Note>Loading…</Note>
      ) : (
        <div className="flex flex-col gap-3">
          {runtime.mode === "external" ? (
            <>
              <ExternalManagedNote />
              <ExternalProviderList rows={providerStatus?.rows ?? []} />
            </>
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

              {rows.length === 0 && !management.isLoading ? (
                <Note>No provider yet. Add one to start chatting.</Note>
              ) : null}
              {rows.length > 0 ? (
                <ul className="divide-y overflow-hidden rounded-lg border">
                  {rows.map((row) => (
                    <ProviderRow
                      key={row.name}
                      row={row}
                      active={
                        row.name === status.selectedProvider ||
                        (isGatewayRow(row) && row.active === true)
                      }
                      modelCount={modelsFor(row.name)}
                      health={management.health[row.name]}
                      problem={providerProblem(statusFor(row.name))}
                      busy={management.busy}
                      operatorSettings={status.operatorSettings}
                      onTest={() => void management.testKey(row.name)}
                      onActivate={() => void activateProvider(row.name)}
                      onRemove={() => setRemoving(row)}
                    />
                  ))}
                </ul>
              ) : null}
              <div className="flex flex-wrap items-center justify-between gap-2">
                <AddProviderDialog
                  known={management.known}
                  configured={groups.configured.map((p) => p.name)}
                  authFile={status.authFile}
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
              <AlertDialogTitle>
                Remove {providerLabel(removing.name)}?
              </AlertDialogTitle>
              <AlertDialogDescription>
                {removing.keyPresent && removing.authMethod !== "none"
                  ? "The agent forgets this provider and its key."
                  : "The agent forgets this provider."}{" "}
                {RESTART_SENTENCE}
                {removing.name === status?.selectedProvider &&
                  " It is the active provider, so choose another one afterwards."}
                {groups.configured.length === 1 &&
                  removing.name === groups.configured[0]?.name &&
                  " It is the only provider, so the agent will be offline until you add one."}
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

/** A custom provider defined in the agent's settings, as opposed to a
 *  built-in kind. The source fallback covers an older server with no class
 *  field. */
function isCustomRow(row: HarnessProviderInfo): boolean {
  return row.class === "custom" || row.source.includes("settings.yaml");
}

const MANAGED_ELSEWHERE_TITLE =
  "This provider is set where the agent runs and can't be removed here.";

/**
 * One provider row: status dot, name (a link to its models page), one
 * plain status line, and the actions menu. The whole row is a link so
 * click-through works everywhere; menu clicks stop propagation. The
 * built-in offline mode is deliberately not listed.
 */
function ProviderRow({
  row,
  active,
  modelCount,
  health,
  problem,
  busy,
  operatorSettings = false,
  onTest,
  onActivate,
  onRemove,
}: {
  row: HarnessProviderInfo;
  active: boolean;
  modelCount: number;
  health: ProviderKeyHealth | undefined;
  /** The agent's own problem with this provider, when it reports one. */
  problem: string | null;
  busy: string;
  /** The agent runs on a settings file Studio does not edit. */
  operatorSettings?: boolean;
  onTest: () => void;
  onActivate: () => void;
  onRemove: () => void;
}) {
  const presentation = keyPresentation(row, health);
  const testing = busy === `test:${row.name}`;
  const activating = busy === `activate:${row.name}`;
  const removingBusy = busy === `remove:${row.name}`;
  const href = `/workspace/provider/${encodeURIComponent(row.name)}`;
  const gateway = isGatewayRow(row);
  const custom = isCustomRow(row);
  const hasAuthBlock = row.source.includes("auth.yaml");
  const canActivate = gateway
    ? row.reachable === true
    : row.keyPresent || row.envShadowed === true;
  const canRemove = custom ? !operatorSettings : hasAuthBlock;
  const label = providerLabel(row.name);

  return (
    <li className="flex items-center gap-3 px-4 py-3">
      <span
        aria-hidden="true"
        className={cn("size-2 shrink-0 rounded-full", presentation.dot)}
      />
      <Link href={href} className="min-w-0 flex-1">
        <span className="flex flex-wrap items-center gap-x-2">
          <span className="truncate text-sm font-medium hover:underline">
            {label}
          </span>
          {active && <Badge variant="info">Active</Badge>}
        </span>
        <span className="block truncate text-xs text-muted-foreground">
          {presentation.label}
          {" · "}
          {modelCountText(modelCount)}
        </span>
        {problem ? (
          <span className="block text-xs text-warning">{problem}</span>
        ) : null}
      </Link>
      <DropdownMenu modal={false}>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            className="size-8 shrink-0"
            aria-label={`Actions for ${label}`}
          >
            <Ellipsis className="size-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {row.testable && !gateway ? (
            <DropdownMenuItem
              disabled={!row.keyPresent || testing}
              title={row.keyPresent ? undefined : "Add a key first."}
              onClick={onTest}
            >
              {testing ? "Checking key…" : "Check key"}
            </DropdownMenuItem>
          ) : null}
          <DropdownMenuItem
            disabled={active || !canActivate || activating}
            title={
              active || canActivate
                ? undefined
                : gateway
                  ? "The gateway is not reachable."
                  : "Add a key first."
            }
            onClick={onActivate}
          >
            {activating ? "Switching…" : "Set as active"}
          </DropdownMenuItem>
          <DropdownMenuItem asChild>
            <Link href={href}>View models</Link>
          </DropdownMenuItem>
          {!gateway ? (
            <DropdownMenuItem
              variant="destructive"
              disabled={removingBusy || !canRemove}
              title={canRemove ? undefined : MANAGED_ELSEWHERE_TITLE}
              onClick={onRemove}
            >
              {removingBusy ? "Removing…" : "Remove provider"}
            </DropdownMenuItem>
          ) : null}
        </DropdownMenuContent>
      </DropdownMenu>
    </li>
  );
}

/**
 * The agent's per-provider status on its own — the read-only external-mode
 * list. A row carries a problem line only when it is not fine.
 */
function ExternalProviderList({ rows }: { rows: HarnessProviderStatus[] }) {
  if (rows.length === 0) return null;
  return (
    <ul className="divide-y overflow-hidden rounded-lg border">
      {rows.map((status) => {
        const problem = providerProblem(status);
        return (
          <li
            key={status.providerId}
            className="flex items-center gap-3 px-4 py-2"
            data-provider-status={status.providerId}
          >
            <span
              aria-hidden="true"
              className={cn(
                "size-2 shrink-0 rounded-full",
                problem ? "bg-amber-500" : "bg-emerald-500",
              )}
            />
            <div className="min-w-0 flex-1">
              <span className="flex flex-wrap items-center gap-x-2">
                <span className="text-sm font-medium">
                  {providerLabel(status.providerId)}
                </span>
                <span className="text-xs text-muted-foreground">
                  {problem ? "Not available" : "Ready"}
                  {" · "}
                  {modelCountText(status.modelCount)}
                </span>
              </span>
              {problem ? (
                <span
                  className="block text-xs text-muted-foreground"
                  data-role="hint"
                >
                  {problem}
                </span>
              ) : null}
            </div>
          </li>
        );
      })}
    </ul>
  );
}
