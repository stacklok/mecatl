import { createHighlighterCore, type HighlighterCore } from "shiki/core";
import { createJavaScriptRegexEngine } from "shiki/engine/javascript";
import { bundledLanguages } from "shiki/langs";
import { bundledThemes } from "shiki/themes";

/**
 * Shiki-backed syntax highlighting for the file-preview code viewer.
 *
 * Kept self-contained and CSP-safe: the JavaScript RegExp engine (no
 * WebAssembly, no `eval`) tokenizes with real TextMate grammars, and both
 * grammars and themes are loaded from Shiki's own lazy `import()` thunks — one
 * grammar chunk per language, fetched only the first time that language is
 * previewed. A single core highlighter is memoized for the session.
 *
 * The two GitHub themes are tokenized together (`defaultColor: false`) so each
 * token carries both a light and a dark colour; the component picks between
 * them with a CSS `.dark` variant, matching the app's class-based theme switch.
 */

const LIGHT_THEME = "github-light-default";
const DARK_THEME = "github-dark-default";

/** File extension → Shiki language id. Unknowns fall back to plain text. */
const EXT_TO_LANG: Record<string, string> = {
  ts: "typescript",
  tsx: "tsx",
  js: "javascript",
  jsx: "jsx",
  mjs: "javascript",
  cjs: "javascript",
  json: "json",
  py: "python",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  go: "go",
  rs: "rust",
  java: "java",
  rb: "ruby",
  php: "php",
  c: "c",
  cpp: "cpp",
  h: "c",
  hpp: "cpp",
  cs: "csharp",
  kt: "kotlin",
  swift: "swift",
  yml: "yaml",
  yaml: "yaml",
  toml: "toml",
  sql: "sql",
  css: "css",
  scss: "scss",
  html: "html",
  xml: "xml",
  graphql: "graphql",
  prisma: "prisma",
};

/** Resolve a file extension to a Shiki language id (`"text"` when unknown). */
export function langForExtension(ext: string): string {
  return EXT_TO_LANG[ext] ?? "text";
}

let highlighterPromise: Promise<HighlighterCore> | null = null;

function getHighlighter(): Promise<HighlighterCore> {
  if (!highlighterPromise) {
    highlighterPromise = createHighlighterCore({
      themes: [bundledThemes[LIGHT_THEME], bundledThemes[DARK_THEME]],
      langs: [],
      engine: createJavaScriptRegexEngine(),
    });
  }
  return highlighterPromise;
}

interface HToken {
  readonly content: string;
  /** Colour under the light theme (or `"inherit"` for plain text). */
  readonly light: string;
  /** Colour under the dark theme. */
  readonly dark: string;
  readonly italic: boolean;
  readonly bold: boolean;
}

export type HLine = readonly HToken[];

/**
 * Shape of a token when `codeToTokens` runs with multiple themes and
 * `defaultColor: false`: instead of a single resolved colour, each token gets an
 * `htmlStyle` map holding one CSS custom property per theme (`--shiki-light`,
 * `--shiki-dark`) plus any `font-style` / `font-weight`.
 */
interface MultiThemeToken {
  readonly content: string;
  readonly htmlStyle?: Record<string, string>;
}

/**
 * Tokenize `code` into per-line coloured runs. Unknown/`"text"` languages (or a
 * grammar that fails to load) come back as one plain token per line so the
 * caller always renders something.
 */
export async function highlightCode(
  code: string,
  lang: string,
): Promise<HLine[]> {
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
    lang: resolvedLang,
    themes: { light: LIGHT_THEME, dark: DARK_THEME },
    defaultColor: false,
  });

  return (tokens as unknown as MultiThemeToken[][]).map((line) =>
    line.map((token) => {
      const style = token.htmlStyle ?? {};
      return {
        content: token.content,
        light: style["--shiki-light"] ?? "inherit",
        dark: style["--shiki-dark"] ?? "inherit",
        italic: style["font-style"] === "italic",
        bold: style["font-weight"] === "bold",
      };
    }),
  );
}
