// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from "react";

/** Rune cap isn't needed here — the caller passes a short, fixed label. */
const DOTS = [0, 1, 2];

/**
 * Bottom-of-transcript activity line while a turn is streaming: three
 * staggered pulsing dots, a phase label ("Running Read", or "Thinking" for a
 * generic wait when the caller has nothing more specific to say), and a live
 * elapsed-time counter measured from when this indicator mounted.
 *
 * Tailwind's built-in `animate-bounce` moves 25% of the element's own
 * height, which is invisible on a dot this small, so the bounce is a scoped
 * keyframe declared inline (no external animation dependency, no shared
 * stylesheet to keep in sync with this file).
 */
export function StreamingIndicator({ phaseLabel }: { phaseLabel?: string }) {
  const [elapsedSeconds, setElapsedSeconds] = useState(0);

  useEffect(() => {
    const startedAt = Date.now();
    const tick = () => setElapsedSeconds(Math.max(0, Math.round((Date.now() - startedAt) / 1000)));
    tick();
    const timer = setInterval(tick, 1000);
    return () => clearInterval(timer);
  }, []);

  const time =
    elapsedSeconds >= 60
      ? `${Math.floor(elapsedSeconds / 60)}m ${elapsedSeconds % 60}s`
      : `${elapsedSeconds}s`;

  return (
    <div className="flex items-center gap-2 py-2">
      <style>{`
        @keyframes streaming-indicator-bounce {
          0%, 100% {
            transform: translateY(0);
            animation-timing-function: cubic-bezier(0.8, 0, 1, 1);
          }
          50% {
            transform: translateY(-5px);
            animation-timing-function: cubic-bezier(0, 0, 0.2, 1);
          }
        }
      `}</style>
      <span aria-hidden="true" className="flex w-7 shrink-0 items-center justify-center gap-0.5">
        {DOTS.map((dot) => (
          <span
            className="size-1 rounded-full bg-brand"
            key={dot}
            style={{
              animation: "streaming-indicator-bounce 0.9s infinite",
              animationDelay: `${dot * 160}ms`,
            }}
          />
        ))}
      </span>
      <span className="text-sm text-muted-foreground">
        {phaseLabel ?? "Thinking"}
        <span className="mx-1.5 text-muted-foreground/50">·</span>
        <span className="tabular-nums text-muted-foreground/70">{time}</span>
      </span>
    </div>
  );
}
