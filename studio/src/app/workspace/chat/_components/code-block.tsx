"use client";

import { type CSSProperties, useEffect, useState } from "react";
import { cn } from "@/lib/utils";
import { type HLine, highlightCode } from "./code-highlighter";

/**
 * Theme-aware, line-numbered code viewer for the file-preview panel.
 *
 * Tokenizing runs through Shiki (real TextMate grammars, JS engine — see
 * `code-highlighter.ts`), which is async: the first paint renders the raw lines
 * synchronously in the exact same gutter/table layout, then the highlighted
 * tokens swap in with no layout shift once the grammar has loaded. Each token
 * carries a light and a dark colour, selected via a CSS `.dark` variant.
 */

/** Wrap a single raw line of text as one uncoloured (inherit) token run. */
function plainLine(text: string): HLine {
  return text
    ? [
        {
          content: text,
          light: "inherit",
          dark: "inherit",
          italic: false,
          bold: false,
        },
      ]
    : [];
}

export function CodeBlock({
  code,
  lang = "text",
}: {
  code: string;
  /** Shiki language id (see `langForExtension`); defaults to plain text. */
  lang?: string;
}) {
  // Drop a single trailing newline so we don't render a phantom last line.
  const source = code.replace(/\n$/, "");
  const [highlighted, setHighlighted] = useState<HLine[] | null>(null);

  useEffect(() => {
    let active = true;
    setHighlighted(null);
    highlightCode(source, lang)
      .then((lines) => {
        if (active) setHighlighted(lines);
      })
      .catch(() => {
        // Leave the plain fallback in place; highlighting is best-effort.
      });
    return () => {
      active = false;
    };
  }, [source, lang]);

  const lines: HLine[] = highlighted ?? source.split("\n").map(plainLine);
  const gutterWidth = `${String(lines.length).length + 1}ch`;

  return (
    <div
      className="overflow-x-auto font-mono text-xs leading-relaxed"
      data-highlighted={highlighted !== null}
    >
      <table className="w-full border-collapse">
        <tbody>
          {lines.map((line, i) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: lines are positional
            <tr key={i} className="align-top">
              <td
                className="select-none pr-4 pl-4 lg:pl-6 text-right tabular-nums text-muted-foreground/40"
                style={{ width: gutterWidth }}
              >
                {i + 1}
              </td>
              <td className="pr-4 lg:pr-6 whitespace-pre">
                {line.length === 0
                  ? " "
                  : line.map((tok, j) => (
                      <span
                        // biome-ignore lint/suspicious/noArrayIndexKey: tokens are positional
                        key={j}
                        style={
                          {
                            "--sl": tok.light,
                            "--sd": tok.dark,
                          } as CSSProperties
                        }
                        className={cn(
                          "text-[color:var(--sl)] dark:text-[color:var(--sd)]",
                          tok.italic && "italic",
                          tok.bold && "font-semibold",
                        )}
                      >
                        {tok.content}
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
