"use client";

import type { HookNotice } from "@/features/agent";
import {
  HOOK_DECISION_GLYPH,
  HOOK_DECISION_LABEL,
  hookChipClass,
  hookNoticeBody,
  hookOutcomeText,
  hookTextClass,
  parseHookNotice,
} from "@/features/agent/hook-notice";
import { cn } from "@/lib/utils";

const CHIP_CLASS =
  "inline-flex shrink-0 items-center gap-0.5 rounded border px-1 text-[10px] font-medium leading-4";

/** Fires can repeat verbatim on one call; rows get positional ids up front. */
const withIds = (hooks: HookNotice[]) =>
  hooks.map((hook, index) => ({ ...hook, id: `${index}:${hook.decision}` }));

/** The decision chip: glyph + label, tinted per decision. */
function HookChip({ hook }: { hook: HookNotice }) {
  return (
    <span
      className={cn(CHIP_CLASS, hookChipClass(hook.decision))}
      title={hookNoticeBody(hook)}
    >
      <span aria-hidden="true">{HOOK_DECISION_GLYPH[hook.decision]}</span>
      <span>{HOOK_DECISION_LABEL[hook.decision]}</span>
    </span>
  );
}

/**
 * One chip per hook fire on an activity row (✗ Blocked, ✎ Modified,
 * ⚠ Advisory, ℹ Info); the fire's full text is on hover and read aloud.
 * Renders nothing for a call no hook touched.
 */
export function HookChips({ hooks }: { hooks: HookNotice[] | undefined }) {
  if (!hooks?.length) return null;
  return (
    <span
      className="flex shrink-0 items-center gap-1"
      data-testid="tool-hook-chips"
    >
      {withIds(hooks).map((hook) => (
        <span key={hook.id} className="inline-flex">
          <HookChip hook={hook} />
          <span className="sr-only">{hookNoticeBody(hook)}</span>
        </span>
      ))}
    </span>
  );
}

/**
 * The drill-down panel's Hooks section: one row per fire — its decision
 * chip, the phase, and the daemon's message (or what the decision did when
 * the hook sent none). Renders nothing for a call no hook touched.
 */
export function HookSection({ hooks }: { hooks: HookNotice[] | undefined }) {
  if (!hooks?.length) return null;
  return (
    <section aria-label="Hooks">
      <p className="mt-4 mb-1.5 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
        Hooks
      </p>
      <ul className="flex flex-col gap-1">
        {withIds(hooks).map((hook) => (
          <li
            key={hook.id}
            className="flex items-start gap-2 rounded-lg border border-border bg-muted/30 p-2 text-xs"
          >
            <HookChip hook={hook} />
            <span className="min-w-0 break-words text-foreground/80">
              <span className="font-medium text-muted-foreground">
                {hook.phase || "Hook"}
              </span>
              {" · "}
              <span>{hookOutcomeText(hook)}</span>
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

/**
 * A `[hook:<decision>] …` transcript notice (a call-less lifecycle hook):
 * the decision glyph and tone in front of the daemon's words. Renders
 * nothing for an unmarked notice.
 */
export function HookNoticeLine({ text }: { text: string }) {
  const parsed = parseHookNotice(text);
  if (!parsed) return null;
  return (
    <p
      className={cn(
        "flex items-start gap-1.5 text-xs",
        hookTextClass(parsed.decision),
      )}
      data-testid="hook-notice"
    >
      <span aria-hidden="true" className="shrink-0">
        {HOOK_DECISION_GLYPH[parsed.decision]}
      </span>
      <span className="sr-only">
        {HOOK_DECISION_LABEL[parsed.decision]} hook:{" "}
      </span>
      <span className="min-w-0 break-words">{parsed.text}</span>
    </p>
  );
}
