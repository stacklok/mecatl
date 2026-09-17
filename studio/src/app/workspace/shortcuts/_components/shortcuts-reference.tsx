"use client";

import { Badge } from "@/components/ui/badge";
import { Kbd } from "@/components/ui/kbd";
import { STUDIO_BUILTIN_COMMANDS } from "@/features/agent/composer-capabilities";
import { deriveHelpFeatures } from "@/features/agent/help-features";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import { useEnterSendBehavior } from "@/lib/profile-preferences";
import { useShortcutBindings } from "@/lib/shortcuts/keymap";
import {
  describeShortcut,
  keycaps,
  SHORTCUT_GROUPS,
} from "@/lib/shortcuts/registry";
import { cn } from "@/lib/utils";

const CARD_CLASS = "rounded-xl border bg-card p-5";
const CARD_HEADING_CLASS =
  "mb-3 text-sm font-semibold text-muted-foreground uppercase tracking-wide";

/**
 * The legend for the token figures the chat ··· menu shows (its "Token
 * usage" rows) and the context meter builds on. The labels are the menu's
 * own words, so a reader can match them line for line.
 */
const USAGE_LEGEND: readonly { label: string; note: string }[] = [
  { label: "input", note: "Tokens the model read this visit." },
  { label: "output", note: "Tokens the model wrote this visit." },
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
  {
    label: "cache hit rate",
    note: "Cache reads as a share of everything the model read.",
  },
];

/**
 * The body of the help reference — Studio's analogue of the TUI help overlay
 * (cmd/mecatui/ui/help.go): every key binding from the shortcut registry
 * (so the list cannot drift from what fires), the Studio-local slash
 * commands, the features the CONNECTED daemon enables (disabled rows carry a
 * "not enabled" tag, the `notEnabledTag` analogue), and the usage legend.
 *
 * Client component: the Enter rows read the live queue-vs-steer preference
 * and the features section reads the runtime-status context. The page shell
 * around it stays a server component so the route keeps its metadata.
 */
export function ShortcutsReference() {
  const { state, serverCapabilities, features, deployment } =
    useRuntimeStatus();
  const { behavior: enterBehavior } = useEnterSendBehavior();
  // EFFECTIVE bindings: the registry defaults with this browser's keymap
  // overrides applied, so a remapped key is documented as it actually fires.
  const { bindings } = useShortcutBindings();
  const featureRows = deriveHelpFeatures(serverCapabilities, features);

  return (
    <div className="space-y-6">
      <div className="grid gap-6 sm:grid-cols-2">
        {SHORTCUT_GROUPS.map((group) => (
          <section key={group} className={CARD_CLASS}>
            <h2 className={CARD_HEADING_CLASS}>{group}</h2>
            <ul className="space-y-2.5">
              {bindings
                .filter((s) => s.group === group)
                .map((s) => (
                  <li
                    key={s.id}
                    className="flex items-center justify-between gap-4"
                  >
                    <span className="text-sm text-foreground">
                      {describeShortcut(s, enterBehavior)}
                    </span>
                    <span className="flex shrink-0 items-center gap-1">
                      {s.custom ? (
                        <Badge variant="outline">custom</Badge>
                      ) : null}
                      {keycaps(s.effectiveCombo).map((k, i) => (
                        // biome-ignore lint/suspicious/noArrayIndexKey: positional keycaps
                        <Kbd key={i}>{k}</Kbd>
                      ))}
                    </span>
                  </li>
                ))}
            </ul>
          </section>
        ))}

        <section className={CARD_CLASS} aria-labelledby="studio-commands">
          <h2 id="studio-commands" className={CARD_HEADING_CLASS}>
            Studio commands
          </h2>
          <ul className="space-y-2.5">
            {STUDIO_BUILTIN_COMMANDS.map((command) => (
              <li
                key={command.name}
                className="flex items-center justify-between gap-4"
              >
                <span className="text-sm text-foreground">
                  {command.name === "help"
                    ? "Open this page from the composer"
                    : command.description}
                </span>
                <span className="flex shrink-0 items-center gap-1">
                  <Kbd>/{command.name}</Kbd>
                </span>
              </li>
            ))}
          </ul>
          <p className="mt-3 text-xs text-muted-foreground">
            Typed at the start of a message and answered by Studio — never sent
            to the agent.
          </p>
        </section>
      </div>

      <section className={CARD_CLASS} aria-labelledby="daemon-features">
        <h2 id="daemon-features" className={CARD_HEADING_CLASS}>
          Features on this daemon
        </h2>
        {state === "connected" ? (
          <>
            <ul className="grid gap-x-8 gap-y-2.5 sm:grid-cols-2">
              {featureRows.map((row) => (
                <li
                  key={row.id}
                  data-feature={row.id}
                  data-enabled={row.enabled ? "true" : "false"}
                  className={cn(
                    "space-y-0.5",
                    !row.enabled && "text-muted-foreground",
                  )}
                >
                  <div className="flex items-center gap-2">
                    <span className="text-sm font-medium">{row.label}</span>
                    {!row.enabled && (
                      <Badge variant="outline">not enabled</Badge>
                    )}
                  </div>
                  <p className="text-xs text-muted-foreground">{row.hint}</p>
                </li>
              ))}
            </ul>
            <p className="mt-4 text-xs text-muted-foreground">
              Rows describe the deployment as a whole; the open chat&rsquo;s
              model may still decline a media kind the deployment allows.
              {deployment ? ` Agent: ${deployment}.` : ""}
            </p>
          </>
        ) : (
          <p className="text-sm text-muted-foreground">
            {state === "connecting"
              ? "Checking which features are turned on…"
              : "Connect to an agent to see which features are turned on."}
          </p>
        )}
      </section>

      <section className={CARD_CLASS} aria-labelledby="usage-legend">
        <h2 id="usage-legend" className={CARD_HEADING_CLASS}>
          Reading the numbers
        </h2>
        <ul className="space-y-2">
          {USAGE_LEGEND.map((entry) => (
            <li key={entry.label} className="flex items-baseline gap-3">
              <span className="w-32 shrink-0 text-sm font-medium tabular-nums">
                {entry.label}
              </span>
              <span className="text-sm text-muted-foreground">
                {entry.note}
              </span>
            </li>
          ))}
        </ul>
        <p className="mt-3 text-xs text-muted-foreground">
          Figures appear in the chat ··· menu once the daemon reports them; a
          provider without prompt caching shows input and output only. The
          context meter is approximate: input + output counted this visit over
          the model&rsquo;s window.
        </p>
      </section>
    </div>
  );
}
