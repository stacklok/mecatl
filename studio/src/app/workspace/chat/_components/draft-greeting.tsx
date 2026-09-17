"use client";

import { pageTitleClass } from "@/lib/typography";

/** One-click prompts on the draft state, to seed the first message. */
export const STARTER_PROMPTS = [
  "Summarise what changed in the repo this week",
  "Draft a plan for a new feature",
  "Review my open pull requests",
  "Find and explain a bug in the codebase",
] as const;

const chipClass =
  "rounded-full border border-border bg-background px-3.5 py-1.5 text-sm text-muted-foreground transition-colors hover:border-foreground/20 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

/**
 * The draft chat's greeting: the "What can I help you with?" heading
 * (always) and the starter-prompt chips (hideable — the web analogue of the
 * TUI's `--no-banner`). The caller owns the preference
 * (`useShowStarterPrompts`).
 */
export function DraftGreeting({
  showStarterPrompts,
  onPickSeed,
}: {
  showStarterPrompts: boolean;
  onPickSeed: (text: string) => void;
}) {
  return (
    <>
      <h1 className={pageTitleClass("pb-0 text-center text-3xl leading-tight")}>
        What can I help you with?
      </h1>
      {showStarterPrompts && (
        <div className="flex flex-wrap justify-center gap-2">
          {STARTER_PROMPTS.map((p) => (
            <button
              key={p}
              type="button"
              onClick={() => onPickSeed(p)}
              className={chipClass}
            >
              {p}
            </button>
          ))}
        </div>
      )}
    </>
  );
}
