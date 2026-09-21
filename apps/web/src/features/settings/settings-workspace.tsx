// SPDX-License-Identifier: Apache-2.0

import type {
  GetRuntimeResponse,
  GetRuntimeSettingsResponse,
} from "@mecatl-studio/contracts/generated";
import { getRuntimeOptions, getRuntimeSettingsOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import {
  BrainCircuit,
  Cloud,
  ExternalLink,
  Image,
  Laptop,
  LifeBuoy,
  Search,
  Server,
} from "lucide-react";
import { type ReactNode, useMemo, useState } from "react";
import { AuthControl } from "../../components/shell/auth-control";
import { Badge } from "../../components/ui/badge";
import { Button } from "../../components/ui/button";
import { Input } from "../../components/ui/input";
import { Switch } from "../../components/ui/switch";
import { modelPreferenceId, useDisabledModels } from "../../lib/model-preferences";
import { LearningReview } from "../knowledge/learning-review";
import { AgentSettings } from "./agent-settings";
import { IdentitySettings } from "./identity-settings";
import { InterfaceSettings } from "./interface-settings";
import { managementNotes } from "./management-notes";
import { MemorySettings } from "./memory-settings";
import type { SettingsSection } from "./settings-sections";
import { StorageSettings } from "./storage-settings";

type Model = GetRuntimeSettingsResponse["models"][number];

/** Where a problem with this app (not the connected agent) is reported. */
const supportUrl = "https://github.com/stacklok/mecatl/issues";

/**
 * Grouped as Preferences / Agent runtime / Support. A section appears only
 * when the BFF has data behind it; permissions, MCP tools, and experimental
 * switches need daemon-side configuration Studio cannot change, so they are
 * omitted rather than faked.
 */
const settingsGroups: Array<{
  items: Array<{ label: string; value: SettingsSection }>;
  title: string;
}> = [
  {
    items: [
      { label: "You", value: "profile" },
      { label: "Personalise", value: "appearance" },
    ],
    title: "Preferences",
  },
  {
    items: [
      { label: "Agent", value: "agent" },
      { label: "Memory", value: "memory" },
      { label: "Learning", value: "learning" },
      { label: "Providers", value: "models" },
      { label: "Storage", value: "storage" },
    ],
    title: "Agent runtime",
  },
  { items: [{ label: "About", value: "about" }], title: "Support" },
];

export function SettingsWorkspace({
  item,
  onItemChange,
  onSectionChange,
  section = "profile",
}: {
  item?: string;
  onItemChange?: (item?: string) => void;
  onSectionChange?: (section: SettingsSection) => void;
  section?: SettingsSection;
}) {
  const runtime = useQuery(getRuntimeOptions());
  const settings = useQuery(getRuntimeSettingsOptions());
  const modelPreferences = useDisabledModels();
  const loading = runtime.isPending || settings.isPending;
  const error = runtime.error ?? settings.error;

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto w-full max-w-6xl px-4 py-7 sm:px-8 sm:py-10">
        <h1 className="text-3xl font-semibold tracking-tight">Settings</h1>
        <p className="mt-2 max-w-2xl text-sm text-muted-foreground">
          See how the agent is connected and which models it can use.
        </p>

        <div className="mt-7 sm:hidden">
          <label className="text-xs font-medium text-muted-foreground" htmlFor="settings-section">
            Settings section
          </label>
          <select
            className="mt-2 h-10 w-full rounded-lg border bg-card px-3 text-sm"
            id="settings-section"
            onChange={(event) => onSectionChange?.(event.target.value as SettingsSection)}
            value={section}
          >
            {settingsGroups.map((group) => (
              <optgroup key={group.title} label={group.title}>
                {group.items.map((item) => (
                  <option key={item.value} value={item.value}>
                    {item.label}
                  </option>
                ))}
              </optgroup>
            ))}
          </select>
        </div>

        <div className="mt-7 flex items-start gap-8">
          <nav aria-label="Settings sections" className="hidden w-40 shrink-0 sm:block">
            <ul className="space-y-4">
              {settingsGroups.map((group) => (
                <li key={group.title}>
                  <p className="px-3 pb-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                    {group.title}
                  </p>
                  <ul className="space-y-1">
                    {group.items.map((item) => (
                      <li key={item.value}>
                        <button
                          aria-current={section === item.value ? "page" : undefined}
                          className={`w-full rounded-lg px-3 py-2 text-left text-sm ${section === item.value ? "bg-muted font-medium" : "text-muted-foreground hover:bg-muted/60 hover:text-foreground"}`}
                          onClick={() => onSectionChange?.(item.value)}
                          type="button"
                        >
                          {item.label}
                        </button>
                      </li>
                    ))}
                  </ul>
                </li>
              ))}
            </ul>
          </nav>

          <div className="min-w-0 flex-1 space-y-6">
            {section === "profile" && <IdentitySettings />}
            {section === "agent" && <AgentSettings />}
            {section === "appearance" && <InterfaceSettings />}
            {section === "models" &&
              (error ? (
                <StateCard text={errorMessage(error)} />
              ) : loading ? (
                <StateCard text="Loading settings…" />
              ) : (
                settings.data && (
                  <>
                    <ProviderInventory settings={settings.data} />
                    <ModelInventory modelPreferences={modelPreferences} settings={settings.data} />
                  </>
                )
              ))}
            {section === "about" &&
              (error ? (
                <StateCard text={errorMessage(error)} />
              ) : loading ? (
                <StateCard text="Loading settings…" />
              ) : (
                runtime.data &&
                settings.data && <AboutAgent runtime={runtime.data} settings={settings.data} />
              ))}
            {section === "memory" && (
              <MemorySettings onSelect={onItemChange ?? (() => {})} selectedKey={item} />
            )}
            {section === "learning" && <LearningReview />}
            {section === "storage" && <StorageSettings />}
          </div>
        </div>
      </div>
    </div>
  );
}

function ProviderInventory({ settings }: { settings: GetRuntimeSettingsResponse }) {
  const modelsFor = (providerId: string) =>
    settings.models.filter((model) => model.providerId === providerId).length;

  return (
    <Section icon={Server} title="Providers">
      <ul className="space-y-1 text-sm text-muted-foreground">
        {managementNotes(settings.management).map((note) => (
          <li key={note}>{note}</li>
        ))}
        <li>Credentials are never shown here.</li>
      </ul>
      {!settings.modelsSupported ? (
        <StateCard text="Model providers aren’t available in this workspace." />
      ) : settings.providers.length === 0 ? (
        <StateCard text="No providers are ready yet." />
      ) : (
        <div className="mt-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {settings.providers.map((provider) => {
            const modelCount = modelsFor(provider.id) || provider.modelCount;
            return (
              <article className="rounded-xl border bg-background p-4" key={provider.id}>
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <h3 className="font-medium">{humanize(provider.id)}</h3>
                  <ProviderState state={provider.state} />
                </div>
                <p className="mt-2 text-sm text-muted-foreground">
                  {modelCount} model{modelCount === 1 ? "" : "s"} available
                </p>
              </article>
            );
          })}
        </div>
      )}
    </Section>
  );
}

function ModelInventory({
  modelPreferences,
  settings,
}: {
  modelPreferences: ReturnType<typeof useDisabledModels>;
  settings: GetRuntimeSettingsResponse;
}) {
  const [search, setSearch] = useState("");
  const models = useMemo(() => {
    const query = search.trim().toLowerCase();
    return query
      ? settings.models.filter((model) =>
          `${model.providerId} ${model.id} ${model.displayName}`.toLowerCase().includes(query),
        )
      : settings.models;
  }, [search, settings.models]);

  return (
    <Section icon={BrainCircuit} title="Models">
      {settings.models.length > 0 && (
        <div className="relative max-w-sm">
          <Search className="pointer-events-none absolute left-3 top-2.5 size-4 text-muted-foreground" />
          <Input
            aria-label="Filter models"
            className="pl-9"
            onChange={(event) => setSearch(event.target.value)}
            placeholder="Find a model"
            value={search}
          />
        </div>
      )}
      {settings.models.length === 0 ? (
        <StateCard text="No models are available yet." />
      ) : models.length === 0 ? (
        <StateCard text="No models match your search." />
      ) : (
        <div className="mt-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {models.map((model) => (
            <ModelCard
              enabled={!modelPreferences.disabled.has(modelPreferenceId(model))}
              key={`${model.providerId}-${model.id}`}
              model={model}
              onEnabledChange={(enabled) =>
                modelPreferences.setModelEnabled(modelPreferenceId(model), enabled)
              }
            />
          ))}
        </div>
      )}
    </Section>
  );
}

function ModelCard({
  enabled,
  model,
  onEnabledChange,
}: {
  enabled: boolean;
  model: Model;
  onEnabledChange: (enabled: boolean) => void;
}) {
  return (
    <article className="rounded-xl border bg-background p-4" title={model.id}>
      <div className="flex items-start justify-between gap-3">
        <h3 className="font-medium">{model.displayName}</h3>
        <label
          className="flex items-center gap-2 text-xs text-muted-foreground"
          htmlFor={`visible-model-${model.providerId}-${model.id}`}
        >
          Visible
          <Switch
            aria-label={`Show ${model.displayName} in model pickers`}
            checked={enabled}
            id={`visible-model-${model.providerId}-${model.id}`}
            onCheckedChange={(checked) => onEnabledChange(checked === true)}
          />
        </label>
      </div>
      <p className="mt-1 text-sm text-muted-foreground">{humanize(model.providerId)}</p>
      {(model.reasoning || model.image) && (
        <div className="mt-3 flex flex-wrap gap-2">
          {model.reasoning && <Badge variant="info">Reasoning</Badge>}
          {model.image && (
            <Badge variant="outline">
              <Image aria-hidden="true" />
              Images
            </Badge>
          )}
        </div>
      )}
    </article>
  );
}

function AboutAgent({
  runtime,
  settings,
}: {
  runtime: GetRuntimeResponse;
  settings: GetRuntimeSettingsResponse;
}) {
  const online = runtime.connection === "online";
  return (
    <Section icon={runtime.source === "local" ? Laptop : Cloud} title="About">
      <dl className="grid gap-3 sm:grid-cols-3">
        <Fact label="Status">
          <Badge variant={online ? "success" : "warning"}>
            {online ? "Connected" : "Needs attention"}
          </Badge>
        </Fact>
        <Fact label="Location">
          {runtime.source === "local" ? "On this device" : "Remote workspace"}
        </Fact>
        <Fact label="Agent version">{settings.buildId || "Not reported"}</Fact>
      </dl>
      <div className="mt-4 flex flex-wrap gap-2">
        <Button asChild className="rounded-full" size="sm" variant="outline">
          <a href={supportUrl} rel="noreferrer" target="_blank">
            <LifeBuoy aria-hidden="true" />
            Report a problem
            <ExternalLink aria-hidden="true" className="size-3 text-muted-foreground" />
          </a>
        </Button>
        {/* The keyboard-shortcuts reference page arrives with its own plan layer. */}
      </div>
      <div className="mt-2 divide-y border-t">
        <AuthControl />
      </div>
    </Section>
  );
}

function Section({
  children,
  icon: Icon,
  title,
}: {
  children: ReactNode;
  icon: typeof Server;
  title: string;
}) {
  return (
    <section className="rounded-2xl border bg-card p-5 sm:p-6">
      <div className="flex items-center gap-3">
        <span className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-brand/10 text-brand">
          <Icon className="size-5" />
        </span>
        <h2 className="text-lg font-semibold">{title}</h2>
      </div>
      <div className="mt-5">{children}</div>
    </section>
  );
}

function Fact({ children, label }: { children: ReactNode; label: string }) {
  return (
    <div className="rounded-lg border bg-background p-3">
      <dt className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{label}</dt>
      <dd className="mt-2 break-all text-sm">{children}</dd>
    </div>
  );
}

function ProviderState({ state }: { state: string }) {
  const normalized = state.toLowerCase();
  if (normalized === "ok" || normalized === "available") {
    return <Badge variant="success">Ready</Badge>;
  }
  if (normalized === "unauthorized") {
    return <Badge variant="warning">Sign-in needed</Badge>;
  }
  if (normalized === "unreachable") {
    return <Badge variant="destructive">Unavailable</Badge>;
  }
  return <Badge variant="muted">Not ready</Badge>;
}

function StateCard({ text }: { text: string }) {
  return (
    <div className="mt-4 rounded-xl border border-dashed p-8 text-center text-sm text-muted-foreground">
      {text}
    </div>
  );
}

function humanize(value: string): string {
  return value
    .split(/[-_]+/u)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function errorMessage(error: unknown) {
  if (typeof error === "object" && error !== null && "detail" in error) return String(error.detail);
  return error instanceof Error ? error.message : "Settings could not be loaded.";
}
