"use client";

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
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import {
  type DaemonOptionsPatch,
  mergeDaemonOptions,
  useDaemonOptions,
} from "@/features/agent/hooks/use-daemon-options";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import type { HarnessDaemonOptions } from "@/lib/harness/daemon-options";
import { readMemoryStores } from "../../../_components/memory-indicator";
import {
  Note,
  RESTART_SENTENCE,
  SettingsCard,
  SettingsRow,
} from "../../_components/settings-card";

/**
 * Settings → Memory → Memory: the two things the agent can remember, as two
 * switches. The saved values come from the same options document every other
 * agent-options card edits, and a save sends the WHOLE merged document and
 * restarts the agent; only the on/off switches are offered here. When the
 * agent is run elsewhere the switches give way to a read-only On/Off line
 * per store, read from what the running agent reports.
 */

/** The two stores, in display order, keyed by the options-document section. */
const STORES = [
  {
    section: "projectMemory",
    id: "project-memory-enabled",
    label: "Project memory",
    description: "Remember things about this project between chats.",
    statusTestId: "memory-status-project",
    capability: "project",
    patch: (enabled: boolean): DaemonOptionsPatch => ({
      projectMemory: { enabled },
    }),
  },
  {
    section: "userModel",
    id: "user-model-enabled",
    label: "Facts about you",
    description: "Remember your preferences across every project.",
    statusTestId: "memory-status-user-model",
    capability: "userModel",
    patch: (enabled: boolean): DaemonOptionsPatch => ({
      userModel: { enabled },
    }),
  },
] as const;

const statusWord = (value: boolean | null) =>
  value === null ? "Not available" : value ? "On" : "Off";

const sameOptions = (a: HarnessDaemonOptions, b: HarnessDaemonOptions) =>
  JSON.stringify(a) === JSON.stringify(b);

export function MemoryStoresCard() {
  const { serverCapabilities } = useRuntimeStatus();
  const { live, manageable, doc, isLoading, busy, error, notice, save } =
    useDaemonOptions();
  const [draft, setDraft] = useState<HarnessDaemonOptions | null>(null);
  const [confirming, setConfirming] = useState(false);

  let body: React.ReactNode;
  if (!live) {
    body = <Note>The agent is offline, so memory can&apos;t be changed.</Note>;
  } else if (!manageable) {
    const stores = readMemoryStores(serverCapabilities);
    body = (
      <div className="space-y-4">
        <Note>
          Memory is set where the agent runs and can&apos;t be changed here.
        </Note>
        <div className="divide-y divide-border/60 rounded-lg border px-4">
          {STORES.map((store) => (
            <div
              key={store.id}
              className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 py-2.5"
            >
              <p className="text-sm">{store.label}</p>
              <span
                className="text-sm text-muted-foreground"
                data-testid={store.statusTestId}
              >
                {statusWord(stores[store.capability])}
              </span>
            </div>
          ))}
        </div>
      </div>
    );
  } else if (!doc) {
    body = (
      <Note>
        {isLoading
          ? "Loading memory settings…"
          : (error ?? "Memory settings couldn't be loaded right now.")}
      </Note>
    );
  } else {
    const shown = draft ?? doc.options;
    const dirty = !sameOptions(shown, doc.options);
    const update = (patch: DaemonOptionsPatch) =>
      setDraft(mergeDaemonOptions(shown, patch));
    const submit = async () => {
      setConfirming(false);
      const ok = await save(shown);
      if (ok) setDraft(null);
    };
    body = (
      <>
        <div className="divide-y divide-border/60">
          {STORES.map((store) => (
            <SettingsRow
              key={store.id}
              label={store.label}
              htmlFor={store.id}
              description={store.description}
            >
              <Switch
                id={store.id}
                checked={shown[store.section].enabled}
                disabled={busy}
                onCheckedChange={(enabled) => update(store.patch(enabled))}
              />
            </SettingsRow>
          ))}
        </div>
        {dirty ? (
          <p
            className="mt-3 text-xs text-muted-foreground"
            data-testid="memory-stores-pending"
          >
            {RESTART_SENTENCE}
          </p>
        ) : null}
        <div className="mt-4 flex flex-wrap items-center gap-2">
          <Button
            type="button"
            size="sm"
            disabled={!dirty || busy}
            onClick={() => setConfirming(true)}
          >
            Save and restart
          </Button>
          {dirty ? (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              disabled={busy}
              onClick={() => setDraft(null)}
            >
              Discard
            </Button>
          ) : null}
        </div>
        {error ? (
          <p role="alert" className="mt-3 text-sm text-destructive">
            {error}
          </p>
        ) : null}
        {notice ? (
          <p role="status" className="mt-3 text-sm text-muted-foreground">
            {notice}
          </p>
        ) : null}
        <AlertDialog open={confirming} onOpenChange={setConfirming}>
          {confirming && (
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>Save memory settings?</AlertDialogTitle>
                <AlertDialogDescription>
                  {RESTART_SENTENCE}
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>Cancel</AlertDialogCancel>
                <AlertDialogAction onClick={() => void submit()}>
                  Save and restart
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          )}
        </AlertDialog>
      </>
    );
  }

  return (
    <SettingsCard title="Memory" description="Choose what the agent remembers.">
      <div data-testid="memory-stores">{body}</div>
    </SettingsCard>
  );
}
