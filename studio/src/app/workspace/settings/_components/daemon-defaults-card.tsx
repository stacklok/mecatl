"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { useDaemonDefaults } from "@/features/agent/hooks/use-daemon-defaults";
import type {
  HarnessModel,
  useHarnessRuntime,
} from "@/features/agent/hooks/use-harness-runtime";
import { useConfirm } from "@/hooks/use-confirm";
import { BASE_URL_KINDS } from "@/lib/daemon-defaults.mjs";
import {
  type HarnessDaemonDefaults,
  validateDaemonDefaults,
} from "@/lib/harness/client";
import {
  Note,
  OfflineNote,
  RESTART_SENTENCE,
  SettingsCard,
  SettingsRow,
} from "./settings-card";

type Runtime = ReturnType<typeof useHarnessRuntime>;
type DefaultsHook = ReturnType<typeof useDaemonDefaults>;

/** Radix Select refuses an empty-string item value, so "not set" rides a
 *  sentinel that can never collide with a model id or an enum token. */
const NONE = "__none__";

/** One alias / slot binding as the saved document holds it. */
interface KeyValueRow {
  key: string;
  value: string;
}

/**
 * The form's working copy — the saved document flattened for editing: the
 * ACTIVE provider's model pair pulled out of `models`, the number as typed,
 * the maps as ordered rows. Only `defaultModel` has a control on the card;
 * every other field rides along unchanged so a save never drops what an
 * operator configured elsewhere. Exported for its vitest.
 */
export interface DaemonDefaultsFormDraft {
  defaultModel: string;
  subagentModel: string;
  reasoningEffort: string;
  contextWindowOverride: string;
  /** Whole seconds as typed; "" saves as the agent's own default. */
  llmPerAttemptTimeout: string;
  llmStreamIdleTimeout: string;
  promptCacheEnabled: boolean;
  anthropicTtl: string;
  baseUrls: Record<string, string>;
  toolhiveEnabled: boolean;
  toolhiveBaseUrl: string;
  toolhiveMode: string;
  aliases: KeyValueRow[];
  slots: KeyValueRow[];
  apiKeyFile: string;
}

const rowsOf = (map: Record<string, string>): KeyValueRow[] =>
  Object.entries(map).map(([key, value]) => ({ key, value }));

/** Rows → map, skipping rows left entirely blank; a half-filled row is
 *  kept so the validator names it. */
const mapOf = (rows: KeyValueRow[]): Record<string, string> => {
  const out: Record<string, string> = {};
  for (const row of rows) {
    const key = row.key.trim();
    const value = row.value.trim();
    if (!key && !value) continue;
    out[key] = value;
  }
  return out;
};

/** The saved document as the form edits it, for the provider `kind`. */
export function draftFromDefaults(
  saved: HarnessDaemonDefaults,
  kind: string | null,
): DaemonDefaultsFormDraft {
  const pair = kind ? saved.models[kind] : undefined;
  return {
    defaultModel: pair?.defaultModel ?? "",
    subagentModel: pair?.subagentModel ?? "",
    reasoningEffort: saved.reasoningEffort,
    contextWindowOverride: saved.contextWindowOverride
      ? String(saved.contextWindowOverride)
      : "",
    llmPerAttemptTimeout: String(saved.llmTimeouts.perAttemptSeconds),
    llmStreamIdleTimeout: String(saved.llmTimeouts.streamIdleSeconds),
    promptCacheEnabled: !saved.promptCache.disabled,
    anthropicTtl: saved.promptCache.anthropicTtl,
    baseUrls: Object.fromEntries(
      BASE_URL_KINDS.map((url) => [url, saved.baseUrls[url] ?? ""]),
    ),
    toolhiveEnabled: saved.toolhive.enabled,
    toolhiveBaseUrl: saved.toolhive.baseUrl,
    toolhiveMode: saved.toolhive.mode,
    aliases: rowsOf(saved.aliases),
    slots: rowsOf(saved.slots),
    apiKeyFile: saved.apiKeyFile,
  };
}

/**
 * The exact document Save sends: the draft folded back over the saved
 * document — the active provider's model pair replaces ITS entry in
 * `models` (other providers' pairs are kept untouched), everything else is
 * taken from the draft. Validated by the server's own grammar; throws its
 * user-facing message. Exported for its vitest.
 */
export function defaultsFromDraft(
  draft: DaemonDefaultsFormDraft,
  saved: HarnessDaemonDefaults,
  kind: string | null,
): HarnessDaemonDefaults {
  const models = { ...saved.models };
  if (kind) {
    models[kind] = {
      defaultModel: draft.defaultModel,
      subagentModel: draft.subagentModel,
    };
  }
  return validateDaemonDefaults({
    models,
    reasoningEffort: draft.reasoningEffort,
    contextWindowOverride: draft.contextWindowOverride.trim() || 0,
    llmTimeouts: {
      perAttemptSeconds: draft.llmPerAttemptTimeout.trim(),
      streamIdleSeconds: draft.llmStreamIdleTimeout.trim(),
    },
    promptCache: {
      disabled: !draft.promptCacheEnabled,
      anthropicTtl: draft.anthropicTtl,
    },
    baseUrls: draft.baseUrls,
    toolhive: {
      enabled: draft.toolhiveEnabled,
      baseUrl: draft.toolhiveBaseUrl,
      mode: draft.toolhiveMode,
    },
    aliases: mapOf(draft.aliases),
    slots: mapOf(draft.slots),
    apiKeyFile: draft.apiKeyFile,
  });
}

/** The provider whose default model the card edits: the active selection,
 *  or the ToolHive gateway when the agent fell back to it; never the
 *  offline mode (nothing to validate a default against). */
export function modelDefaultsKind(status: Runtime["status"]): string | null {
  if (!status) return null;
  const kind =
    status.selectedProvider ??
    (status.toolhiveGateway?.active ? "toolhive" : null);
  return kind && kind !== "mock" ? kind : null;
}

export const RESTART_WARNING = RESTART_SENTENCE;

/**
 * The default model for the active provider — what the agent uses when a
 * chat does not pick one. The saved document carries more than this one
 * field; the rest rides along unchanged on every save. Managed mode only:
 * an external deployment chose its own, so the card renders nothing there.
 */
export function DaemonDefaultsCard({
  runtime,
  defaults,
}: {
  runtime: Runtime;
  defaults: DefaultsHook;
}) {
  const { confirm, ConfirmDialog } = useConfirm();
  const [draft, setDraft] = useState<DaemonDefaultsFormDraft | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const title = "Default model";
  const description = "The model the agent uses when a chat does not pick one.";

  if (!runtime.live) {
    return (
      <SettingsCard title={title} description={description}>
        <OfflineNote />
      </SettingsCard>
    );
  }
  if (runtime.mode === "external" || !defaults.manageable) {
    return null;
  }
  const status = runtime.status;
  const saved = defaults.defaults;
  if (!saved || !status) {
    return (
      <SettingsCard title={title} description={description}>
        {defaults.error ? (
          <p className="whitespace-pre-wrap text-sm text-destructive">
            {defaults.error}
          </p>
        ) : defaults.isLoading || runtime.status === null ? (
          <Note>Loading…</Note>
        ) : (
          <Note>The default model could not be read right now.</Note>
        )}
      </SettingsCard>
    );
  }

  const kind = modelDefaultsKind(status);
  if (!kind) {
    return (
      <SettingsCard title={title} description={description}>
        <Note>Set a provider as active above to choose its default model.</Note>
      </SettingsCard>
    );
  }

  const savedDraft = draftFromDefaults(saved, kind);
  const view = draft ?? savedDraft;
  const dirty = JSON.stringify(view) !== JSON.stringify(savedDraft);
  const busy = defaults.busy;
  const patch = (next: Partial<DaemonDefaultsFormDraft>) => {
    setFormError(null);
    setDraft({ ...view, ...next });
  };
  const providerModels = runtime.models.filter(
    (model) => model.providerId === kind && model.id,
  );

  const save = async () => {
    let document: HarnessDaemonDefaults;
    try {
      document = defaultsFromDraft(view, saved, kind);
    } catch (caught) {
      setFormError(caught instanceof Error ? caught.message : String(caught));
      return;
    }
    const confirmed = await confirm({
      title: "Save and restart the agent?",
      description: RESTART_WARNING,
      confirmText: "Save and restart",
    });
    if (!confirmed) return;
    const ok = await defaults.save(document);
    if (ok) {
      setDraft(null);
      await runtime.refresh();
    }
  };

  return (
    <SettingsCard title={title} description={description}>
      <div className="flex flex-col gap-4">
        {defaults.error && (
          <p className="whitespace-pre-wrap text-sm text-destructive">
            {defaults.error}
          </p>
        )}
        {defaults.notice && (
          <p className="text-sm text-muted-foreground">{defaults.notice}</p>
        )}

        <div className="divide-y divide-border/60">
          <SettingsRow label="Default model" htmlFor="daemon-default-model">
            <ModelSelect
              id="daemon-default-model"
              value={view.defaultModel}
              noneLabel="Provider's default"
              models={providerModels}
              onChange={(next) => patch({ defaultModel: next })}
            />
          </SettingsRow>
        </div>

        {formError && (
          <p
            role="alert"
            className="whitespace-pre-wrap text-sm text-destructive"
          >
            {formError}
          </p>
        )}

        <Note>{RESTART_WARNING}</Note>

        <div className="flex flex-wrap items-center justify-end gap-2">
          {dirty && (
            <Button
              variant="outline"
              className="rounded-full"
              onClick={() => {
                setDraft(null);
                setFormError(null);
              }}
              disabled={busy}
            >
              Discard
            </Button>
          )}
          <Button
            variant="action"
            className="rounded-full"
            disabled={!dirty || busy}
            onClick={() => void save()}
          >
            {busy ? "Saving…" : "Save"}
          </Button>
        </div>
      </div>
      {ConfirmDialog}
    </SettingsCard>
  );
}

/**
 * A model picker over the agent's live inventory for one provider, with a
 * "not set" first choice. A saved id the inventory no longer lists stays
 * selectable (labelled as saved) so an older choice is visible, not
 * silently replaced.
 */
function ModelSelect({
  id,
  value,
  noneLabel,
  models,
  onChange,
}: {
  id: string;
  value: string;
  noneLabel: string;
  models: HarnessModel[];
  onChange: (next: string) => void;
}) {
  const listed = models.some((model) => model.id === value);
  return (
    <Select
      value={value || NONE}
      onValueChange={(next) => onChange(next === NONE ? "" : next)}
    >
      <SelectTrigger id={id} className="w-full sm:w-72">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={NONE}>{noneLabel}</SelectItem>
        {value && !listed && (
          <SelectItem value={value}>
            {value} (saved, no longer listed)
          </SelectItem>
        )}
        {models.map((model) => (
          <SelectItem key={model.id} value={model.id}>
            {model.displayName}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
