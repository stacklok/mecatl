import { describe, expect, it } from "vitest";
import {
  humanizeMcpServer,
  humanizeMcpTool,
  mcpToolTitle,
  parseMcpToolName,
  stripControlChars,
  toolDisplayName,
  toolDisplayParts,
} from "./tool-names";

// Control and format characters are built from code points so the source
// never carries a literal control byte or a bidi override (biome rewrites
// `\uXXXX` escapes in string literals into the raw character).
const cp = (...codes: number[]) => String.fromCodePoint(...codes);
const BEL = cp(0x07);
const ESC = cp(0x1b);
const SOH = cp(0x01);
const CSI = cp(0x9b); // C1: single-byte escape-sequence opener
const RLO = cp(0x202e); // Cf: right-to-left override
const ZWSP = cp(0x200b); // Cf: zero-width space

/**
 * The table mirrors cmd/mecatui/ui/render.go (`parseMCPName`, `mcpTitle`,
 * `humanizeMCPServer`, `humanizeMCPTool`): Studio and the TUI must title the
 * same daemon tool name the same way.
 */
describe("parseMcpToolName", () => {
  it("splits on the FIRST double underscore after the prefix", () => {
    expect(parseMcpToolName("mcp__github__issue_write")).toEqual({
      server: "github",
      tool: "issue_write",
    });
    expect(parseMcpToolName("mcp__fetch__fetch_url__v2")).toEqual({
      server: "fetch",
      tool: "fetch_url__v2",
    });
  });

  it("returns null for a core tool and for an empty half", () => {
    expect(parseMcpToolName("Read")).toBeNull();
    expect(parseMcpToolName("Shell")).toBeNull();
    expect(parseMcpToolName("mcp__x__")).toBeNull();
    expect(parseMcpToolName("mcp____tool")).toBeNull();
    expect(parseMcpToolName("mcp__")).toBeNull();
    expect(parseMcpToolName("mcp__nosplit")).toBeNull();
  });
});

describe("mcpToolTitle", () => {
  it.each([
    ["mcp__github__issue_write", "GitHub · Issue write"],
    ["mcp__github__issue_read", "GitHub · Issue read"],
    ["mcp__github__create_pull_request", "GitHub · Create pull request"],
    ["mcp__github__search_code", "GitHub · Search code"],
    ["mcp__github__get_file_contents", "GitHub · Get file contents"],
    ["mcp__slack__post_message", "Slack · Post message"],
    ["mcp__fetch__fetch", "Fetch · Fetch"],
    ["mcp__foo__bar_baz", "Foo · Bar baz"],
    ["mcp__github__delete_branch", "GitHub · Delete branch"],
  ])("%s → %s", (name, title) => {
    expect(mcpToolTitle(name)).toBe(title);
  });

  it("keeps a hyphenated server token raw instead of half-capitalizing it", () => {
    expect(mcpToolTitle("mcp__io-github-stacklok-playwright__click")).toBe(
      "io-github-stacklok-playwright · Click",
    );
  });

  it("keeps a server token over 20 code points raw", () => {
    const long = "abcdefghijklmnopqrstu"; // 21
    expect(mcpToolTitle(`mcp__${long}__run`)).toBe(`${long} · Run`);
    const exact = "abcdefghijklmnopqrst"; // 20 — still title-cased
    expect(mcpToolTitle(`mcp__${exact}__run`)).toBe(
      "Abcdefghijklmnopqrst · Run",
    );
  });

  it("returns null for a non-MCP name and a malformed MCP name", () => {
    expect(mcpToolTitle("Read")).toBeNull();
    expect(mcpToolTitle("Shell")).toBeNull();
    expect(mcpToolTitle("mcp__x__")).toBeNull();
    expect(mcpToolTitle("mcp____tool")).toBeNull();
  });

  it("strips C0/C1 controls and format characters from both halves", () => {
    expect(mcpToolTitle(`mcp__git${BEL}hub__issue_${ESC}write`)).toBe(
      "GitHub · Issue write",
    );
    expect(mcpToolTitle(`mcp__slack${CSI}__post${RLO}_message`)).toBe(
      "Slack · Post message",
    );
    expect(mcpToolTitle("mcp__foo\n__bar\tbaz")).toBe("Foo · Barbaz");
  });

  it("falls back to null when a half is nothing but controls", () => {
    expect(mcpToolTitle(`mcp__${SOH}__tool`)).toBeNull();
    expect(mcpToolTitle(`mcp__server__${SOH}`)).toBeNull();
  });
});

describe("humanizeMcpServer / humanizeMcpTool", () => {
  it("title-cases only the first code point and never lower-cases the rest", () => {
    expect(humanizeMcpServer("linear")).toBe("Linear");
    expect(humanizeMcpServer("myAPI")).toBe("MyAPI");
    expect(humanizeMcpServer("émile")).toBe("Émile");
  });

  it("prefers the known tables", () => {
    expect(humanizeMcpServer("github")).toBe("GitHub");
    expect(humanizeMcpTool("create_pull_request")).toBe("Create pull request");
  });

  it("turns underscores into spaces and capitalizes the first word only", () => {
    expect(humanizeMcpTool("list_open_issues")).toBe("List open issues");
    expect(humanizeMcpTool("getRepo")).toBe("GetRepo");
    // Mirrors strings.Split: consecutive underscores → consecutive spaces.
    expect(humanizeMcpTool("fetch_url__v2")).toBe("Fetch url  v2");
  });
});

describe("stripControlChars", () => {
  it("removes C0, DEL, C1 and Cf but keeps ordinary letters and punctuation", () => {
    const dirty = `a${cp(0x00)}b${cp(0x1f)}c${cp(0x7f)}d${cp(0x80)}e${cp(0x9f)}f${ZWSP}g`;
    expect(stripControlChars(dirty)).toBe("abcdefg");
    expect(stripControlChars("io-github_x.y:z")).toBe("io-github_x.y:z");
  });
});

describe("toolDisplayName / toolDisplayParts", () => {
  it("shows the MCP title and keeps the exact id as the subtitle", () => {
    expect(toolDisplayName("mcp__github__issue_write")).toBe(
      "GitHub · Issue write",
    );
    expect(toolDisplayParts("mcp__github__issue_write")).toEqual({
      title: "GitHub · Issue write",
      subtitle: "mcp__github__issue_write",
      mcp: true,
      server: "github",
      tool: "issue_write",
    });
  });

  it("passes a core tool through unchanged with no subtitle", () => {
    expect(toolDisplayName("Read")).toBe("Read");
    expect(toolDisplayName("Shell")).toBe("Shell");
    expect(toolDisplayParts("Edit")).toEqual({ title: "Edit", mcp: false });
  });

  it("treats a malformed MCP name as a plain name", () => {
    expect(toolDisplayName("mcp__x__")).toBe("mcp__x__");
    expect(toolDisplayParts("mcp____tool")).toEqual({
      title: "mcp____tool",
      mcp: false,
    });
  });
});
