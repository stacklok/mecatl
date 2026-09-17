/**
 * The status-line template grammar — the web analogue of mecatui's
 * StatusML templates, deliberately narrower: a template is PLAIN TEXT with
 * `{{fact}}` placeholders, and Studio renders it as React text, never as
 * markup, so a substituted value (or the template itself) can never create an
 * element, a link or a style. The only non-text pieces are the two shipped
 * components, `{{context_meter}}` and `{{context_bar}}`.
 *
 * Grammar:
 *   {{key}}            a fact, human-formatted (tokens as "84.0k", percent as "~42%")
 *   {{key|raw}}        the exact value (whole numbers, mode/state ids)
 *   {{clock|HH:mm:ss}} the clock/date with a format (HH mm ss h a · YYYY MM DD MMM ddd)
 *   {{context_meter}}  the shipped meter (model · effort, bar, tokens, facets)
 *   {{context_bar}}    just the context bar with "used / window · %"
 * Anything else is literal text; an unknown key renders as `⟨key?⟩` so a typo
 * is visible where it happens.
 */

import { formatTokens } from "@/lib/formatters";
import { permissionModeLabel } from "@/lib/permission-mode";
import type { SessionPermissionMode } from "@/lib/protocol";
import { effortLabel } from "@/lib/reasoning-effort";
import {
  contextMeterLabel,
  contextPercent,
  isStatusFactKey,
  type StatusAgentState,
  type StatusFacts,
} from "./facts";

export type StatusSegment =
  | { kind: "text"; text: string }
  | { kind: "fact"; key: string; raw: boolean; format?: string }
  | { kind: "meter" }
  | { kind: "bar" };

/** A segment after substitution: text runs (merged) and the two components. */
export type RenderedStatusSegment =
  | { kind: "text"; text: string }
  | { kind: "meter" }
  | { kind: "bar" };

const PLACEHOLDER = /\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*(?:\|([^}]*?))?\s*\}\}/g;

/** Split a template into literal text and placeholders. Pure; never throws. */
export function parseStatusTemplate(text: string): StatusSegment[] {
  const segments: StatusSegment[] = [];
  let last = 0;
  for (const match of text.matchAll(PLACEHOLDER)) {
    const start = match.index ?? 0;
    if (start > last)
      segments.push({ kind: "text", text: text.slice(last, start) });
    const key = match[1].toLowerCase();
    const modifier = (match[2] ?? "").trim();
    if (key === "context_meter") segments.push({ kind: "meter" });
    else if (key === "context_bar") segments.push({ kind: "bar" });
    else if (modifier === "raw")
      segments.push({ kind: "fact", key, raw: true });
    else if (modifier) {
      segments.push({ kind: "fact", key, raw: false, format: modifier });
    } else segments.push({ kind: "fact", key, raw: false });
    last = start + match[0].length;
  }
  if (last < text.length)
    segments.push({ kind: "text", text: text.slice(last) });
  return segments;
}

/** True when the template renders the clock or the date (needs a tick). */
export function templateUsesClock(text: string): boolean {
  return parseStatusTemplate(text).some(
    (segment) =>
      segment.kind === "fact" &&
      (segment.key === "clock" || segment.key === "date"),
  );
}

export interface RenderStatusOptions {
  /** The clock's instant; clock/date render "" when absent (SSR frame). */
  now?: Date | null;
}

const MONTHS = [
  "Jan",
  "Feb",
  "Mar",
  "Apr",
  "May",
  "Jun",
  "Jul",
  "Aug",
  "Sep",
  "Oct",
  "Nov",
  "Dec",
];
const DAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

const pad2 = (n: number) => String(n).padStart(2, "0");

/**
 * A tiny strftime: the tokens are replaced longest-first, everything else is
 * literal. Deliberately small — it is a status line, not a date library.
 */
export function formatClock(now: Date, format: string): string {
  return format.replace(/YYYY|MMM|ddd|MM|DD|HH|hh|mm|ss|H|h|a/g, (token) => {
    switch (token) {
      case "YYYY":
        return String(now.getFullYear());
      case "MMM":
        return MONTHS[now.getMonth()];
      case "ddd":
        return DAYS[now.getDay()];
      case "MM":
        return pad2(now.getMonth() + 1);
      case "DD":
        return pad2(now.getDate());
      case "HH":
        return pad2(now.getHours());
      case "H":
        return String(now.getHours());
      case "hh":
        return pad2(now.getHours() % 12 || 12);
      case "h":
        return String(now.getHours() % 12 || 12);
      case "mm":
        return pad2(now.getMinutes());
      case "ss":
        return pad2(now.getSeconds());
      case "a":
        return now.getHours() < 12 ? "AM" : "PM";
      default:
        return token;
    }
  });
}

const AGENT_STATE_LABEL: Record<StatusAgentState, string> = {
  idle: "idle",
  streaming: "working",
  awaiting: "awaiting approval",
};

const SERVER_LABEL: Record<string, string> = {
  managed: "managed daemon",
  external: "external daemon",
};

function tokens(n: number, raw: boolean): string {
  return raw
    ? String(Math.max(0, Math.round(n)))
    : formatTokens(Math.max(0, n));
}

/** The literal a template shows for a key the grammar does not know. */
function unknownFactLiteral(key: string): string {
  return `⟨${key}?⟩`;
}

/**
 * One fact as text. Human formatting by default; `raw` gives the exact
 * value. A fact that cannot be stated honestly (unknown window, nothing
 * counted, no effort echoed) renders "" rather than a fake zero.
 */
function formatFact(
  key: string,
  facts: StatusFacts,
  options: { raw?: boolean; format?: string; now?: Date | null } = {},
): string {
  const raw = options.raw === true;
  switch (key) {
    case "model":
      return facts.model;
    case "effort":
      return raw || !facts.effort ? facts.effort : effortLabel(facts.effort);
    case "provider":
      return facts.provider;
    case "route":
      return facts.route;
    case "session_title":
      return facts.sessionTitle;
    case "session_handle":
      return facts.sessionHandle;
    case "session_state":
      return facts.sessionState;
    case "mode":
      return raw || !facts.mode
        ? facts.mode
        : permissionModeLabel(facts.mode as SessionPermissionMode);
    case "agent_state":
      return raw ? facts.agentState : AGENT_STATE_LABEL[facts.agentState];
    case "context_percent": {
      const percent = contextPercent(facts);
      if (percent === null) return "";
      return raw ? String(percent) : `~${percent}%`;
    }
    case "context_used":
      return facts.contextUsed > 0 ? tokens(facts.contextUsed, raw) : "";
    case "context_window":
      return facts.contextWindow > 0 ? tokens(facts.contextWindow, raw) : "";
    case "input_tokens":
      return tokens(facts.inputTokens, raw);
    case "output_tokens":
      return tokens(facts.outputTokens, raw);
    case "total_tokens":
      return tokens(facts.inputTokens + facts.outputTokens, raw);
    case "cache_read_tokens":
      return tokens(facts.cacheReadTokens, raw);
    case "cached_percent": {
      if (facts.inputTokens <= 0) return "";
      const percent = Math.round(
        Math.min(1, facts.cacheReadTokens / facts.inputTokens) * 100,
      );
      return raw ? String(percent) : `${percent}%`;
    }
    case "queued":
      return String(facts.queued);
    case "server":
      return raw ? facts.server : (SERVER_LABEL[facts.server] ?? facts.server);
    case "deployment":
      return facts.deployment;
    case "workspace":
      return facts.workspace;
    case "posture":
      return facts.posture;
    case "clock":
      return options.now
        ? formatClock(options.now, options.format || "HH:mm")
        : "";
    case "date":
      return options.now
        ? formatClock(options.now, options.format || "YYYY-MM-DD")
        : "";
    default:
      return unknownFactLiteral(key);
  }
}

/**
 * Substitute every placeholder. Adjacent text runs are merged so a rendered
 * template is the fewest React text nodes possible; the two components stay
 * as their own segments for the lane to mount.
 */
export function renderStatusSegments(
  segments: readonly StatusSegment[],
  facts: StatusFacts,
  options: RenderStatusOptions = {},
): RenderedStatusSegment[] {
  const out: RenderedStatusSegment[] = [];
  const pushText = (text: string) => {
    if (text === "") return;
    const last = out.at(-1);
    if (last?.kind === "text") last.text += text;
    else out.push({ kind: "text", text });
  };
  for (const segment of segments) {
    switch (segment.kind) {
      case "text":
        pushText(segment.text);
        break;
      case "fact":
        pushText(
          isStatusFactKey(segment.key)
            ? formatFact(segment.key, facts, {
                raw: segment.raw,
                format: segment.format,
                now: options.now,
              })
            : unknownFactLiteral(segment.key),
        );
        break;
      case "meter":
      case "bar":
        out.push({ kind: segment.kind });
        break;
    }
  }
  return out;
}

/**
 * A template as ONE string: the text runs, with each meter/bar component
 * standing in as its text label (`84.0k / 200.0k · 42%`). What a preview's
 * tooltip or a test compares against.
 */
export function renderStatusText(
  text: string,
  facts: StatusFacts,
  options: RenderStatusOptions = {},
): string {
  return renderStatusSegments(parseStatusTemplate(text), facts, options)
    .map((segment) =>
      segment.kind === "text"
        ? segment.text
        : contextMeterLabel(facts.contextUsed, facts.contextWindow),
    )
    .join("");
}

/**
 * True when a rendered template shows nothing: every text run is blank and
 * any meter/bar would hide itself (nothing counted). Lets the lane collapse
 * entirely instead of leaving an empty row's gap.
 */
export function renderedIsBlank(
  segments: readonly RenderedStatusSegment[],
  facts: StatusFacts,
): boolean {
  return segments.every((segment) =>
    segment.kind === "text"
      ? segment.text.trim() === ""
      : facts.contextUsed <= 0,
  );
}
