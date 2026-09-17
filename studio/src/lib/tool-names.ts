/**
 * Humanized MCP tool names — the TUI's `Server · Tool` head, ported from
 * `cmd/mecatui/ui/render.go` (`parseMCPName`, `mcpServerNames`,
 * `mcpToolNames`, `mcpTitle`, `humanizeMCPServer`, `humanizeMCPTool`,
 * `titleWord`). `mcp__github__issue_write` reads "GitHub · Issue write" on
 * the activity row, the drill-down panel and the approval badge.
 *
 * This is DISPLAY ONLY. The reducers, mappers and approval verdicts keep
 * the raw daemon name byte-exact; every render site that shows the friendly
 * form keeps the exact id reachable (a hover title or a mono line beneath).
 */

const MCP_PREFIX = "mcp__";

/**
 * Splits `mcp__<server>__<tool>` into its halves. The tool half may itself
 * contain `__`, so the split is on the FIRST `__` after the prefix. Returns
 * null for any non-MCP name and when either half is empty (`mcp__x__`,
 * `mcp____tool`), so a core tool (Read, Shell, …) keeps its plain name.
 */
export function parseMcpToolName(
  name: string,
): { server: string; tool: string } | null {
  if (!name.startsWith(MCP_PREFIX)) return null;
  const rest = name.slice(MCP_PREFIX.length);
  const i = rest.indexOf("__");
  if (i <= 0 || i + 2 >= rest.length) return null;
  return { server: rest.slice(0, i), tool: rest.slice(i + 2) };
}

/** Well-known MCP server tokens → display name; an unlisted server is title-cased. */
const MCP_SERVER_NAMES: Readonly<Record<string, string>> = {
  github: "GitHub",
  slack: "Slack",
  fetch: "Fetch",
};

/**
 * High-traffic MCP tool tokens → a polished verb phrase; an unlisted tool is
 * humanized (underscores → spaces, first word capitalized).
 */
const MCP_TOOL_NAMES: Readonly<Record<string, string>> = {
  issue_write: "Issue write",
  issue_read: "Issue read",
  create_pull_request: "Create pull request",
  search_code: "Search code",
  get_file_contents: "Get file contents",
};

/**
 * The code-point budget above which a server token is shown raw rather than
 * title-cased — a long namespaced token reads worse capitalized.
 */
const MAX_TITLE_CASE_SERVER = 20;

/** Unicode format characters: bidi overrides, zero-width joiners, BOM. */
const FORMAT_CHAR = /\p{Cf}/u;

/**
 * C0 and C1 controls, DEL, and Unicode format characters — the TUI's
 * `sanitizeTerminal` set. A tool name is one line, so newline and tab go
 * too. Written as a code-point test (the `chat-seed.ts` idiom) rather than
 * a control-character regex literal.
 */
function isControlChar(ch: string): boolean {
  const code = ch.codePointAt(0) ?? 0;
  return code < 0x20 || (code >= 0x7f && code <= 0x9f) || FORMAT_CHAR.test(ch);
}

/**
 * Removes every control/format character from a server-derived token. Both
 * halves are scrubbed before they reach a title (defence in depth: React
 * escapes markup, but a bidi override still reorders the rendered line).
 */
export function stripControlChars(text: string): string {
  let out = "";
  for (const ch of text) {
    if (!isControlChar(ch)) out += ch;
  }
  return out;
}

/**
 * Upper-cases the first code point, leaving the rest untouched — a light
 * title-case that never lower-cases an already-capped word. Like the Go
 * original it keeps only the first code point of a multi-character
 * upper-casing ("ß" → "S").
 */
function titleWord(word: string): string {
  const [first, ...rest] = [...word];
  if (first === undefined) return "";
  return [...first.toUpperCase()][0] + rest.join("");
}

/**
 * Renders an MCP server token: the known display name, else the RAW token
 * when title-casing would mangle it (a hyphenated namespace such as
 * `io-github-stacklok-playwright`, or one over 20 code points), else the
 * token with its first letter upper-cased.
 */
export function humanizeMcpServer(server: string): string {
  const token = stripControlChars(server);
  const known = MCP_SERVER_NAMES[token];
  if (known !== undefined) return known;
  if (token.includes("-") || [...token].length > MAX_TITLE_CASE_SERVER) {
    return token;
  }
  return titleWord(token);
}

/**
 * Renders an MCP tool token: the known verb phrase, else underscores turned
 * to spaces with the first word capitalized (`bar_baz` → "Bar baz"). Split
 * mirrors the TUI's `strings.Split(tool, "_")`: consecutive underscores
 * become consecutive spaces (HTML collapses them on render).
 */
export function humanizeMcpTool(tool: string): string {
  const token = stripControlChars(tool);
  const known = MCP_TOOL_NAMES[token];
  if (known !== undefined) return known;
  const words = token.split("_");
  words[0] = titleWord(words[0] ?? "");
  return words.join(" ");
}

/**
 * The friendly `<Server> · <Tool>` title for an MCP tool name
 * (`mcp__github__issue_write` → "GitHub · Issue write"); null for any other
 * name, and for an MCP name whose halves are empty once controls are
 * stripped, so the caller falls back to the raw name.
 */
export function mcpToolTitle(name: string): string | null {
  const parsed = parseMcpToolName(name);
  if (!parsed) return null;
  const server = humanizeMcpServer(parsed.server);
  const tool = humanizeMcpTool(parsed.tool);
  if (server.trim() === "" || tool.trim() === "") return null;
  return `${server} · ${tool}`;
}

/** The name a surface shows: the MCP title when there is one, else the raw name. */
export function toolDisplayName(name: string): string {
  return mcpToolTitle(name) ?? name;
}

export interface ToolDisplayParts {
  /** What to show: "GitHub · Issue write" for an MCP tool, else the raw name. */
  title: string;
  /** The exact tool name, present only when it differs from the title. */
  subtitle?: string;
  /** True when the name parsed as an MCP tool (the row swaps its glyph). */
  mcp: boolean;
  /** The raw `<server>` half of an MCP name. */
  server?: string;
  /** The raw `<tool>` half of an MCP name. */
  tool?: string;
}

/**
 * Title + optional raw-id subtitle for a tool name, so a render site can show
 * the friendly form and keep the exact id discoverable in one call.
 */
export function toolDisplayParts(name: string): ToolDisplayParts {
  const parsed = parseMcpToolName(name);
  const title = mcpToolTitle(name);
  if (parsed && title !== null) {
    return {
      title,
      subtitle: name,
      mcp: true,
      server: parsed.server,
      tool: parsed.tool,
    };
  }
  return { title: name, mcp: false };
}
