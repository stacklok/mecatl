"use client";

/**
 * The user-templatable status lanes — mecatui's `status_customization`
 * header/footer surfaces, for the browser. Two reserved lanes (the chat
 * header's right segment and the strip above the composer) each render the
 * person's template (Settings → Status line) over the live `StatusFacts`,
 * in the richest of three width variants that fits: the lane is a CSS
 * container, and container queries pick `full` / `compact` / `minimal`
 * exactly as the TUI picks the richest variant that fits its columns.
 *
 * Templates are plain text with `{{fact}}` placeholders and render as React
 * TEXT — there is no markup path, so neither a template nor a substituted
 * daemon value can create an element. The only components a template can
 * mount are the shipped ones: `{{context_meter}}` (the ContextMeter the
 * footer always showed — the default footer template, so nothing changes
 * until customised) and `{{context_bar}}` (just its bar + figures).
 *
 * The lane returns null when the surface's three templates are all empty
 * (the header's default), or when every variant renders to nothing (a meter
 * with nothing counted yet), so an unused lane leaves no gap. `{{clock}}` /
 * `{{date}}` tick on the preference's interval, and ONLY when a variant of
 * this surface uses them — a lane without a clock never re-renders on a
 * timer.
 */

import { useEffect, useMemo, useState } from "react";
import type { StatusFacts } from "@/lib/statusline/facts";
import {
  STATUS_VARIANTS,
  type StatusSurface,
  type StatusVariant,
  useStatusLinePreferences,
} from "@/lib/statusline/preferences";
import {
  parseStatusTemplate,
  type RenderedStatusSegment,
  renderedIsBlank,
  renderStatusSegments,
  templateUsesClock,
} from "@/lib/statusline/template";
import { cn } from "@/lib/utils";
import { ContextMeter, ContextMeterBar } from "./context-meter";

/**
 * Container-query visibility per variant (Tailwind v4 `@container`
 * variants; `md` = 28rem, `xs` = 20rem of the LANE's width, not the
 * viewport's). Exactly one variant is displayed at any width.
 */
export const STATUS_VARIANT_CLASS: Record<StatusVariant, string> = {
  full: "@max-md:hidden",
  compact: "hidden @max-md:inline @max-xs:hidden",
  minimal: "hidden @max-xs:inline",
};

/** The rendered segments of one template as inline content. */
function RenderedSegments({
  segments,
  facts,
}: {
  segments: readonly RenderedStatusSegment[];
  facts: StatusFacts;
}) {
  // Keys count each kind's occurrence — stable for a given template, and
  // not the array index.
  const seen: Record<RenderedStatusSegment["kind"], number> = {
    text: 0,
    meter: 0,
    bar: 0,
  };
  return (
    <>
      {segments.map((segment) => {
        seen[segment.kind] += 1;
        const key = `${segment.kind}-${seen[segment.kind]}`;
        if (segment.kind === "text") {
          return <span key={key}>{segment.text}</span>;
        }
        return (
          <span
            key={key}
            // Inline in the text run, but capped at the lane's width with a
            // shrinkable child so a long model name still truncates.
            className="inline-flex max-w-full align-middle [&>*]:min-w-0"
          >
            {segment.kind === "meter" ? (
              <ContextMeter
                modelLabel={facts.model}
                effort={facts.effort}
                contextWindow={facts.contextWindow}
                occupancyTokens={facts.contextOccupancy}
                usage={{
                  inputTokens: facts.inputTokens,
                  outputTokens: facts.outputTokens,
                  cacheReadTokens: facts.cacheReadTokens,
                  cacheWriteTokens: facts.cacheWriteTokens,
                }}
                className="px-0"
              />
            ) : (
              <ContextMeterBar
                used={facts.contextUsed}
                contextWindow={facts.contextWindow}
              />
            )}
          </span>
        );
      })}
    </>
  );
}

/**
 * One template rendered over `facts` — the Settings page's live preview,
 * and the building block of each lane variant.
 */
export function StatusTemplateText({
  template,
  facts,
  now = null,
  className,
}: {
  template: string;
  facts: StatusFacts;
  /** The clock's instant; clock/date render "" when null. */
  now?: Date | null;
  className?: string;
}) {
  const rendered = useMemo(
    () => renderStatusSegments(parseStatusTemplate(template), facts, { now }),
    [template, facts, now],
  );
  return (
    <span className={className}>
      <RenderedSegments segments={rendered} facts={facts} />
    </span>
  );
}

export function TemplatedStatusLine({
  surface,
  facts,
  className,
}: {
  surface: StatusSurface;
  facts: StatusFacts;
  /** Classes for the lane (its text style, padding and truncation). */
  className?: string;
}) {
  const { prefs } = useStatusLinePreferences();
  const templates = prefs[surface];
  const usesClock = STATUS_VARIANTS.some((variant) =>
    templateUsesClock(templates[variant]),
  );
  const [now, setNow] = useState<Date | null>(null);

  useEffect(() => {
    if (!usesClock) return;
    setNow(new Date());
    const timer = setInterval(
      () => setNow(new Date()),
      prefs.intervalSeconds * 1000,
    );
    return () => clearInterval(timer);
  }, [usesClock, prefs.intervalSeconds]);

  const variants = useMemo(
    () =>
      STATUS_VARIANTS.map((variant) => ({
        variant,
        rendered: renderStatusSegments(
          parseStatusTemplate(templates[variant]),
          facts,
          { now },
        ),
      })),
    [templates, facts, now],
  );

  if (STATUS_VARIANTS.every((variant) => templates[variant].trim() === "")) {
    return null;
  }
  if (variants.every(({ rendered }) => renderedIsBlank(rendered, facts))) {
    return null;
  }

  return (
    <div
      data-testid={`status-line-${surface}`}
      data-status-surface={surface}
      className={cn("@container min-w-0", className)}
    >
      {variants.map(({ variant, rendered }) => (
        <span
          key={variant}
          data-variant={variant}
          className={cn(STATUS_VARIANT_CLASS[variant], "min-w-0")}
        >
          <RenderedSegments segments={rendered} facts={facts} />
        </span>
      ))}
    </div>
  );
}
