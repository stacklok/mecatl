// SPDX-License-Identifier: Apache-2.0

import { type CSSProperties, useEffect, useState } from "react";
import { createHighlighterCore, type HighlighterCore } from "shiki/core";
import { createJavaScriptRegexEngine } from "shiki/engine/javascript";
import { bundledLanguages } from "shiki/langs";
import { bundledThemes } from "shiki/themes";
import { cn } from "../../lib/utils";

/**
 * Shiki-backed syntax highlighting for fenced code blocks in chat messages.
 *
 * The JavaScript RegExp engine (no WebAssembly, no `eval`) tokenizes with
 * real TextMate grammars, and both grammars and themes load from Shiki's own
 * lazy `import()` thunks — one grammar chunk per language, fetched only the
 * first time that language is highlighted. A single core highlighter is
 * memoized for the session.
 *
 * The two GitHub themes are tokenized together (`defaultColor: false`) so
 * each token carries both a light and a dark colour; the component picks
 * between them with a CSS `.dark` variant, matching this app's class-based
 * theme switch (`../../lib/theme.ts` toggles `document.documentElement`'s
 * `dark` class).
 */

const LIGHT_THEME = "github-light-default";
const DARK_THEME = "github-dark-default";

let highlighterPromise: Promise<HighlighterCore> | null = null;

function getHighlighter(): Promise<HighlighterCore> {
  if (!highlighterPromise) {
    highlighterPromise = createHighlighterCore({
      engine: createJavaScriptRegexEngine(),
      langs: [],
      themes: [bundledThemes[LIGHT_THEME], bundledThemes[DARK_THEME]],
    });
  }
  return highlighterPromise;
}

/**
 * A markdown fence info string (e.g. `"language-tsx"`, as react-markdown
 * passes it on the `<code>` element) mapped to a Shiki language id. Falls
 * back to `"text"` when there is no fence, or the language isn't bundled.
 */
export function langForClassName(className?: string): string {
  const match = /language-(\S+)/u.exec(className ?? "");
  const lang = match?.[1]?.toLowerCase();
  return lang && lang in bundledLanguages ? lang : "text";
}

export interface HighlightToken {
  readonly content: string;
  /** Colour under the light theme (or `"inherit"` for plain text). */
  readonly light: string;
  /** Colour under the dark theme. */
  readonly dark: string;
  readonly italic: boolean;
  readonly bold: boolean;
}

export type HighlightLine = readonly HighlightToken[];

/**
 * Shape of a token when `codeToTokens` runs with multiple themes and
 * `defaultColor: false`: instead of a single resolved colour, each token
 * gets an `htmlStyle` map holding one CSS custom property per theme
 * (`--shiki-light`, `--shiki-dark`) plus any `font-style` / `font-weight`.
 */
interface MultiThemeToken {
  readonly content: string;
  readonly htmlStyle?: Record<string, string>;
}

/**
 * Tokenize `code` into per-line coloured runs. Unknown/`"text"` languages
 * (or a grammar that fails to load) come back as one plain token per line so
 * the caller always renders something.
 */
export async function highlightCode(code: string, lang: string): Promise<HighlightLine[]> {
  const highlighter = await getHighlighter();

  let resolvedLang = lang;
  if (resolvedLang !== "text" && resolvedLang in bundledLanguages) {
    if (!highlighter.getLoadedLanguages().includes(resolvedLang)) {
      try {
        await highlighter.loadLanguage(
          bundledLanguages[resolvedLang as keyof typeof bundledLanguages],
        );
      } catch {
        resolvedLang = "text";
      }
    }
  } else {
    resolvedLang = "text";
  }

  const { tokens } = highlighter.codeToTokens(code, {
    defaultColor: false,
    lang: resolvedLang,
    themes: { dark: DARK_THEME, light: LIGHT_THEME },
  });

  return (tokens as unknown as MultiThemeToken[][]).map((line) =>
    line.map((token) => {
      const style = token.htmlStyle ?? {};
      return {
        bold: style["font-weight"] === "bold",
        content: token.content,
        dark: style["--shiki-dark"] ?? "inherit",
        italic: style["font-style"] === "italic",
        light: style["--shiki-light"] ?? "inherit",
      };
    }),
  );
}

/** Wrap a single raw line of text as one uncoloured (inherit) token run. */
function plainLine(text: string): HighlightLine {
  return text
    ? [{ bold: false, content: text, dark: "inherit", italic: false, light: "inherit" }]
    : [];
}

/**
 * Renders a fenced code block's contents. The first paint is the raw text
 * (no flash of unstyled/mis-styled content, and no layout shift once tokens
 * arrive — same element structure either way); Shiki's tokens swap in once
 * the grammar has loaded.
 */
export function HighlightedCode({ code, lang = "text" }: { code: string; lang?: string }) {
  const [highlighted, setHighlighted] = useState<HighlightLine[] | null>(null);

  useEffect(() => {
    let active = true;
    setHighlighted(null);
    highlightCode(code, lang)
      .then((lines) => {
        if (active) setHighlighted(lines);
      })
      .catch(() => {
        // Leave the plain fallback in place; highlighting is best-effort.
      });
    return () => {
      active = false;
    };
  }, [code, lang]);

  const lines = highlighted ?? code.split("\n").map(plainLine);

  return (
    <code className="font-mono text-xs" data-highlighted={highlighted !== null}>
      {lines.map((line, lineIndex) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: lines are positional
        <span key={lineIndex}>
          {lineIndex > 0 && "\n"}
          {line.map((token, tokenIndex) => (
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
        </span>
      ))}
    </code>
  );
}
