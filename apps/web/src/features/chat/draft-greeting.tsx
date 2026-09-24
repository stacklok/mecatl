// SPDX-License-Identifier: Apache-2.0

import { pageTitleClass } from "../../lib/typography";

/** One-click prompts on the draft chat state, to seed the first message. */
export const STARTER_PROMPTS = [
  "Summarise what changed in the repo this week",
  "Draft a plan for a new feature",
  "Review my open pull requests",
  "Find and explain a bug in the codebase",
] as const;

/** Shared by the starter-prompt chips and ContinueLatestChip. */
export const chipClass =
  "rounded-full border border-border bg-background px-3.5 py-1.5 text-sm text-muted-foreground transition-colors hover:border-foreground/20 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

/** The draft chat's greeting: the serif "What can I help you with?" heading plus starter-prompt chips. */
export function DraftGreeting({ onPickSeed }: { onPickSeed: (text: string) => void }) {
  return (
    <div className="m-auto max-w-md text-center">
      <h2 className={pageTitleClass("text-center")}>What can I help you with?</h2>
      <div className="mt-5 flex flex-wrap justify-center gap-2">
        {STARTER_PROMPTS.map((prompt) => (
          <button
            className={chipClass}
            key={prompt}
            onClick={() => onPickSeed(prompt)}
            type="button"
          >
            {prompt}
          </button>
        ))}
      </div>
    </div>
  );
}
