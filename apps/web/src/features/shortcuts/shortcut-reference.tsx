// SPDX-License-Identifier: Apache-2.0

import { getRuntimeOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { Keyboard } from "lucide-react";
import { Badge } from "../../components/ui/badge";
import { Kbd } from "../../components/ui/kbd";
import { deriveHelpFeatures } from "./help-features";
import { keycaps, shortcutGroups, shortcutRegistry } from "./shortcut-registry";

const USAGE_LEGEND: ReadonlyArray<{ label: string; note: string }> = [
  { label: "input", note: "Tokens the model read this turn." },
  { label: "output", note: "Tokens the model wrote this turn." },
  {
    label: "cache read",
    note: "Tokens served from the provider's prompt cache — cheaper than fresh input.",
  },
  {
    label: "cache write",
    note: "Tokens written into the prompt cache for later turns to reuse.",
  },
  {
    label: "reasoning",
    note: "Tokens the model spent thinking before it answered, when the provider reports them.",
  },
  { label: "% cached", note: "Cache reads as a share of everything the model read this turn." },
];

export function ShortcutReference() {
  const mac = navigator.platform.includes("Mac");
  const runtime = useQuery(getRuntimeOptions());
  const features = runtime.data ? deriveHelpFeatures(runtime.data.capabilities) : undefined;

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto w-full max-w-4xl px-4 py-7 sm:px-8 sm:py-10">
        <div className="flex items-start gap-4">
          <span className="flex size-11 shrink-0 items-center justify-center rounded-xl bg-brand/10 text-brand">
            <Keyboard aria-hidden="true" className="size-5" />
          </span>
          <div>
            <h1 className="text-3xl font-semibold tracking-tight">Keyboard shortcuts</h1>
            <p className="mt-2 text-sm text-muted-foreground">
              Work faster without leaving the keyboard. Shortcuts without a modifier pause while you
              type in an input or editor.
            </p>
          </div>
        </div>

        <div className="mt-8 grid gap-5 md:grid-cols-2">
          {shortcutGroups.map((group) => (
            <section className="rounded-2xl border bg-card p-5" key={group}>
              <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                {group}
              </h2>
              <ul className="mt-3 divide-y">
                {shortcutRegistry
                  .filter((shortcut) => shortcut.group === group)
                  .map((shortcut) => (
                    <li className="flex items-center justify-between gap-4 py-3" key={shortcut.id}>
                      <span className="text-sm">{shortcut.description}</span>
                      <span className="flex shrink-0 items-center gap-1">
                        {keycaps(shortcut.combo, mac).map((keycap) => (
                          <Kbd key={`${shortcut.id}-${keycap}`}>{keycap}</Kbd>
                        ))}
                      </span>
                    </li>
                  ))}
              </ul>
            </section>
          ))}
        </div>

        <section className="mt-5 rounded-2xl border bg-card p-5">
          <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Features on this agent
          </h2>
          {runtime.isPending ? (
            <p className="mt-3 text-sm text-muted-foreground">Checking what's turned on…</p>
          ) : runtime.error || !features ? (
            <p className="mt-3 text-sm text-muted-foreground">
              Connect to an agent to see which features are turned on.
            </p>
          ) : (
            <ul className="mt-3 grid gap-x-8 gap-y-2.5 sm:grid-cols-2">
              {features.map((row) => (
                <li className={row.enabled ? "" : "text-muted-foreground"} key={row.id}>
                  <div className="flex items-center gap-2">
                    <span className="text-sm font-medium">{row.label}</span>
                    {!row.enabled && <Badge variant="outline">not enabled</Badge>}
                  </div>
                  <p className="text-xs text-muted-foreground">{row.hint}</p>
                </li>
              ))}
            </ul>
          )}
        </section>

        <section className="mt-5 rounded-2xl border bg-card p-5">
          <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Reading the numbers
          </h2>
          <ul className="mt-3 space-y-2">
            {USAGE_LEGEND.map((entry) => (
              <li className="flex items-baseline gap-3" key={entry.label}>
                <span className="w-24 shrink-0 text-sm font-medium tabular-nums">
                  {entry.label}
                </span>
                <span className="text-sm text-muted-foreground">{entry.note}</span>
              </li>
            ))}
          </ul>
          <p className="mt-3 text-xs text-muted-foreground">
            Shown under a response once it uses enough tokens to be worth reporting.
          </p>
        </section>
      </div>
    </div>
  );
}
