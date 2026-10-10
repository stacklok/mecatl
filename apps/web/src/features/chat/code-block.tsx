// SPDX-License-Identifier: Apache-2.0

import { type CSSProperties, useEffect, useState } from "react";
import { cn } from "../../lib/utils";
import { type HighlightLine, highlightCode } from "./code-highlight";

/**
 * The file preview's line-numbered code viewer, ported from the prototype's
 * `code-block.tsx`. It follows the page theme (chat messages keep their own
 * dark panel). The first paint is the raw lines in the same gutter and
 * table layout; Shiki's tokens swap in, with no layout shift, once the
 * grammar loads.
 */

/** Wraps a raw line as one uncoloured (inherit) token run. */
function plainLine(text: string): HighlightLine {
  return text
    ? [{ bold: false, content: text, dark: "inherit", italic: false, light: "inherit" }]
    : [];
}

export function CodeBlock({
  code,
  lang = "text",
}: {
  code: string;
  /** A Shiki language id (see `langForClassName`); plain text by default. */
  lang?: string;
}) {
  // Drop one trailing newline so there is no phantom last line.
  const source = code.replace(/\n$/, "");
  const [highlighted, setHighlighted] = useState<HighlightLine[] | null>(null);

  useEffect(() => {
    let active = true;
    setHighlighted(null);
    highlightCode(source, lang)
      .then((lines) => {
        if (active) setHighlighted(lines);
      })
      .catch(() => {
        // Leave the plain lines in place; highlighting is best-effort.
      });
    return () => {
      active = false;
    };
  }, [source, lang]);

  const lines = highlighted ?? source.split("\n").map(plainLine);
  const gutterWidth = `${String(lines.length).length + 1}ch`;

  return (
    <div
      className="overflow-x-auto py-3 font-mono text-xs leading-relaxed"
      data-highlighted={highlighted !== null}
    >
      <table className="w-full border-collapse">
        <tbody>
          {lines.map((line, lineIndex) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: lines are positional
            <tr className="align-top" key={lineIndex}>
              <td
                className="select-none pr-4 pl-4 text-right tabular-nums text-muted-foreground/50 lg:pl-6"
                style={{ width: gutterWidth }}
              >
                {lineIndex + 1}
              </td>
              <td className="whitespace-pre pr-4 lg:pr-6">
                {line.length === 0
                  ? " "
                  : line.map((token, tokenIndex) => (
                      <span
                        className={cn(
                          "text-[color:var(--sl)] dark:text-[color:var(--sd)]",
                          token.italic && "italic",
                          token.bold && "font-semibold",
                        )}
                        // biome-ignore lint/suspicious/noArrayIndexKey: tokens are positional
                        key={tokenIndex}
                        style={{ "--sd": token.dark, "--sl": token.light } as CSSProperties}
                      >
                        {token.content}
                      </span>
                    ))}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
