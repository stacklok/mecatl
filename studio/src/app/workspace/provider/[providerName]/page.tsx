"use client";

import { ArrowLeft, Ellipsis } from "lucide-react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useMemo } from "react";
import {
  directed,
  SortableHead,
  useTableSort,
} from "@/components/sortable-head";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useDaemonDefaults } from "@/features/agent/hooks/use-daemon-defaults";
import {
  type HarnessModel,
  useHarnessRuntime,
} from "@/features/agent/hooks/use-harness-runtime";
import { useConfirm } from "@/hooks/use-confirm";
import { formatContextWindow } from "@/lib/formatters";
import { useDefaultModel, useDisabledModels } from "@/lib/model-preferences";
import { pageTitleClass } from "@/lib/typography";
import { cn } from "@/lib/utils";
import {
  modelDefaultsKind,
  RESTART_WARNING,
} from "../../settings/_components/daemon-defaults-card";
import {
  Note,
  OfflineNote,
  SettingsCard,
} from "../../settings/_components/settings-card";

/**
 * One provider's models from the daemon's live inventory (`GET /v1/models`,
 * filtered by provider id), with a per-model switch. HONEST SCOPE (see
 * model-preferences.ts): the daemon has no per-model disable knob — the
 * operator `models.allowlist` caps project-tier model BINDINGS, not the
 * inventory — so the switch is a Studio-side preference that hides the model
 * from Studio's own pickers. Other clients can still ask the daemon for it,
 * and the note below the table says so.
 */
export default function ProviderModelsPage() {
  const params = useParams<{ providerName: string }>();
  const providerName = decodeURIComponent(params.providerName ?? "");
  const runtime = useHarnessRuntime();
  const { disabled, setModelEnabled } = useDisabledModels();
  // The browser-local default for NEW chats (the composer picker's ★): a
  // Studio preference in this browser only — distinct from the daemon
  // default the kebab below writes, which every client inherits.
  const { defaultModel, setDefaultModel, clearDefaultModel } =
    useDefaultModel();
  const studioDefaultHere =
    defaultModel?.providerId === providerName ? defaultModel.modelId : "";
  const sort = useTableSort<"name" | "id" | "context" | "enabled">("name");
  // `providers set-default PROVIDER [MODEL]`: the daemon defaults' per-
  // provider model pair. The kebab writes THIS provider's --default-model
  // when it is the active provider (mecated validates the flag against the
  // current default provider fail-fast, so a default for an inactive
  // provider would only be stored for later).
  const daemonDefaults = useDaemonDefaults();
  const { confirm, ConfirmDialog } = useConfirm();
  const activeKind = modelDefaultsKind(runtime.status);
  const isActiveProvider = activeKind === providerName;
  const savedDefaultModel =
    daemonDefaults.defaults?.models[providerName]?.defaultModel ?? "";
  const canSetDefault =
    daemonDefaults.manageable && daemonDefaults.defaults !== null;

  const makeDaemonDefault = async (model: HarnessModel) => {
    const current = daemonDefaults.defaults;
    if (!current) return;
    const confirmed = await confirm({
      title: `Make ${model.displayName} the default model?`,
      description: `Chats on ${providerName} that do not pick a model will use ${model.id}. ${RESTART_WARNING}`,
      confirmText: "Set default and restart",
    });
    if (!confirmed) return;
    const { activeProvider: _owned, ...rest } = current;
    const ok = await daemonDefaults.save({
      ...rest,
      models: {
        ...rest.models,
        [providerName]: {
          defaultModel: model.id,
          subagentModel: rest.models[providerName]?.subagentModel ?? "",
        },
      },
    });
    if (ok) await runtime.refresh();
  };

  const models = useMemo(
    () =>
      runtime.models.filter(
        (model) => model.providerId === providerName && model.id,
      ),
    [runtime.models, providerName],
  );

  const sorted = useMemo(() => {
    const primary = (a: HarnessModel, b: HarnessModel) => {
      switch (sort.key) {
        case "id":
          return a.id.localeCompare(b.id);
        case "context":
          return a.contextLimit - b.contextLimit;
        case "enabled":
          return Number(disabled.has(b.id)) - Number(disabled.has(a.id));
        default:
          return a.displayName.localeCompare(b.displayName);
      }
    };
    // Display name stays the direction-independent tiebreak for stability.
    return [...models].sort(
      (a, b) =>
        directed(sort.dir, primary(a, b)) ||
        a.displayName.localeCompare(b.displayName),
    );
  }, [models, sort.key, sort.dir, disabled]);

  return (
    <div className="h-full overflow-y-auto px-3 pt-6 pb-8 min-[500px]:px-4">
      <div className="space-y-5">
        <Link
          href="/workspace/settings/provider"
          className="inline-flex h-9 w-fit items-center gap-1 self-start rounded-full border px-4 text-sm hover:bg-muted"
        >
          <ArrowLeft className="size-3.5" />
          Back
        </Link>
        <h1 className={pageTitleClass("truncate pb-0 text-3xl leading-tight")}>
          {providerName} models
        </h1>
        <SettingsCard
          title="Models"
          description="The daemon's live inventory for this provider. Switching a model off hides it from Studio's pickers only — the daemon can still be asked for it by other clients."
        >
          {!runtime.live ? (
            <OfflineNote />
          ) : models.length === 0 ? (
            <Note>
              {runtime.isLoading
                ? "Reading the model inventory…"
                : `The daemon lists no models for “${providerName}”. A provider added to auth.yaml surfaces its models after a daemon restart.`}
            </Note>
          ) : (
            <div className="overflow-hidden rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    <SortableHead label="Model" sortKey="name" sort={sort} />
                    <SortableHead
                      label="Context"
                      sortKey="context"
                      sort={sort}
                      className="w-px whitespace-nowrap"
                    />
                    <TableHead className="w-px whitespace-nowrap">
                      Capabilities
                    </TableHead>
                    <SortableHead
                      label="Enabled"
                      sortKey="enabled"
                      sort={sort}
                      className="w-px whitespace-nowrap"
                    />
                    <TableHead className="w-px whitespace-nowrap">
                      New chats
                    </TableHead>
                    {canSetDefault && (
                      <TableHead className="w-px whitespace-nowrap">
                        <span className="sr-only">Actions</span>
                      </TableHead>
                    )}
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {sorted.map((model) => {
                    const enabled = !disabled.has(model.id);
                    return (
                      <TableRow
                        key={model.id}
                        className={cn(!enabled && "opacity-60")}
                      >
                        <TableCell className="max-w-0">
                          <p className="flex items-center gap-2 truncate text-sm font-medium">
                            <span className="truncate">
                              {model.displayName}
                            </span>
                            {model.id === savedDefaultModel && (
                              <Badge variant="info">daemon default</Badge>
                            )}
                          </p>
                          <p className="truncate font-mono text-xs text-muted-foreground">
                            {model.id}
                          </p>
                        </TableCell>
                        <TableCell className="whitespace-nowrap font-mono text-xs text-muted-foreground">
                          {formatContextWindow(model.contextLimit) || "—"}
                        </TableCell>
                        <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                          {[
                            model.image ? "image" : "",
                            model.reasoning ? "reasoning" : "",
                          ]
                            .filter(Boolean)
                            .join(" · ") || "text"}
                        </TableCell>
                        <TableCell className="whitespace-nowrap">
                          <Switch
                            checked={enabled}
                            onCheckedChange={(next) =>
                              setModelEnabled(model.id, next)
                            }
                            aria-label={`Show ${model.displayName} in Studio's model pickers`}
                          />
                        </TableCell>
                        <TableCell className="whitespace-nowrap">
                          <input
                            type="radio"
                            name="studio-default-model"
                            className="size-4 accent-primary"
                            checked={studioDefaultHere === model.id}
                            onChange={() =>
                              setDefaultModel({
                                modelId: model.id,
                                providerId: providerName,
                              })
                            }
                            aria-label={`Use ${model.displayName} as my default for new chats`}
                          />
                        </TableCell>
                        {canSetDefault && (
                          <TableCell className="whitespace-nowrap">
                            <DropdownMenu modal={false}>
                              <DropdownMenuTrigger asChild>
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  className="size-8"
                                  aria-label={`Actions for ${model.displayName}`}
                                >
                                  <Ellipsis className="size-4" />
                                </Button>
                              </DropdownMenuTrigger>
                              <DropdownMenuContent align="end">
                                <DropdownMenuItem
                                  disabled={
                                    !isActiveProvider ||
                                    daemonDefaults.busy ||
                                    model.id === savedDefaultModel
                                  }
                                  title={
                                    isActiveProvider
                                      ? model.id === savedDefaultModel
                                        ? "Already the daemon default"
                                        : undefined
                                      : "Set this provider as active first"
                                  }
                                  onClick={() => void makeDaemonDefault(model)}
                                >
                                  {isActiveProvider
                                    ? model.id === savedDefaultModel
                                      ? "Daemon default"
                                      : "Make daemon default"
                                    : "Make daemon default (set this provider as active first)"}
                                </DropdownMenuItem>
                              </DropdownMenuContent>
                            </DropdownMenu>
                          </TableCell>
                        )}
                      </TableRow>
                    );
                  })}
                </TableBody>
              </Table>
            </div>
          )}
          {runtime.live && models.length > 0 && (
            <p className="mt-3 text-xs text-muted-foreground">
              “New chats” marks your default model for new chats in Studio —
              this browser only; the daemon default is unchanged.{" "}
              {defaultModel ? (
                <>
                  Currently {defaultModel.providerId}/{defaultModel.modelId}.{" "}
                  <button
                    type="button"
                    onClick={clearDefaultModel}
                    className="underline underline-offset-2 hover:text-foreground"
                  >
                    Clear my default
                  </button>
                </>
              ) : (
                "None set."
              )}
            </p>
          )}
        </SettingsCard>
        {canSetDefault && (daemonDefaults.error || daemonDefaults.notice) && (
          <p
            className={
              daemonDefaults.error
                ? "whitespace-pre-wrap text-sm text-destructive"
                : "text-sm text-muted-foreground"
            }
          >
            {daemonDefaults.error ?? daemonDefaults.notice}
          </p>
        )}
      </div>
      {ConfirmDialog}
    </div>
  );
}
