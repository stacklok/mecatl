/**
 * The `/diagnostics` report — Studio's rendering of the TUI's
 * `diagnosticsReport` (cmd/mecatui/ui/diagnostics.go). It is the ONE
 * built-in whose output reaches the model, so every field is sanitized
 * before it is written: tokens are bounded and character-restricted,
 * endpoints keep only scheme + host + clean path. Anything that fails the
 * check reads "unavailable" — never the raw value.
 *
 * Pure functions; the caller gathers the inputs from runtime status, the
 * server-info probe and the session's resolved model.
 */

import { effortTier } from "@/lib/reasoning-effort";

const UNAVAILABLE = "unavailable";

/**
 * How the identity probe went — the TUI's `client.SafeInfoFailure`
 * vocabulary (cmd/mecatui/client/serverinfo.go), which the report prints on
 * its `server lookup:` line and the About card renders in place of the
 * daemon rows. A closed set: the report is model-visible, so a failure is
 * named by class, never by its message. Defined here (a pure module) and
 * consumed by server-info.ts, so a test that mocks the probe module never
 * takes the vocabulary with it.
 */
export type ServerInfoLookup =
  | "ok"
  /** The daemon predates GET /v1/info (ADR 0245). */
  | "not-supported"
  /** Nothing answered: a transport failure, or the proxy's 502/503/504
   *  for a daemon it could not reach. */
  | "unreachable"
  /** Something answered, but not a server-info document Studio accepts
   *  (a refusal, a decode failure, an unknown status). */
  | "invalid-response";

const SERVER_INFO_LOOKUPS: readonly ServerInfoLookup[] = [
  "ok",
  "not-supported",
  "unreachable",
  "invalid-response",
];

/**
 * A short opaque identifier (build id, provider id, model id, mode): at most
 * 128 characters of `[A-Za-z0-9._/+-]`, else "unavailable". Mirrors the
 * TUI's `diagnosticToken` byte for byte.
 */
export function diagnosticToken(value: string | undefined | null): string {
  const trimmed = (value ?? "").trim();
  if (trimmed === "" || trimmed.length > 128) return UNAVAILABLE;
  return /^[A-Za-z0-9._/+-]+$/.test(trimmed) ? trimmed : UNAVAILABLE;
}

/** Collapses `.`/`..` segments and repeated slashes the way `path.Clean` does. */
function cleanPath(pathname: string): string {
  const out: string[] = [];
  for (const segment of pathname.split("/")) {
    if (segment === "" || segment === ".") continue;
    if (segment === "..") {
      out.pop();
      continue;
    }
    out.push(segment);
  }
  return `/${out.join("/")}`;
}

/**
 * An endpoint reduced to `scheme://host/clean-path`: userinfo, query and
 * fragment are dropped, control characters or an unparsable/hostless URL
 * read "unavailable". Mirrors the TUI's `diagnosticEndpoint`.
 */
export function diagnosticEndpoint(value: string | undefined | null): string {
  const raw = value ?? "";
  if (raw === "" || raw.length > 2048) return UNAVAILABLE;
  for (const char of raw) {
    const code = char.codePointAt(0) ?? 0;
    if (code < 0x20 || (code >= 0x7f && code <= 0x9f)) return UNAVAILABLE;
  }
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return UNAVAILABLE;
  }
  // `new URL` accepts scheme-only forms (`mailto:x`); the report wants a
  // network host, as the TUI's `u.Host == ""` check demands.
  if (!url.protocol || !url.host || !url.hostname) return UNAVAILABLE;
  const scheme = url.protocol.replace(/:$/, "").toLowerCase();
  return `${scheme}://${url.host}${cleanPath(url.pathname)}`;
}

export interface DiagnosticsReportInput {
  /** `studioPlatform()` (lib/studio-build.ts): `<os>/<browser-major>`, else
   *  "browser". */
  platform: string;
  /** `studioBuild()` — the stamp inlined at `next build`, else "dev". */
  clientBuild: string;
  /** How Studio reaches the daemon (runtime status). */
  mode: "managed" | "external";
  /** GET /v1/info (ADR 0245); null when the lookup was not "ok". */
  serverInfo: {
    buildId: string;
    serverImplementation: string;
    providerEndpoint: string;
  } | null;
  /** The endpoint the BROWSER reaches the daemon through — Studio's own
   *  same-origin `/api/mecatl` proxy (the daemon's address never enters the
   *  browser, ADR 0344), the analogue of the TUI's display server endpoint. */
  serverEndpoint: string;
  /** How the identity probe went (the TUI's `client.SafeInfoFailure`
   *  vocabulary). Anything outside the closed set prints "invalid-response". */
  lookup: ServerInfoLookup;
  /** Operator-set deployment label ("" when unset). */
  deployment: string;
  /** The daemon-reported EFFECTIVE posture tier (`capabilities.posture`;
   *  "" against an older daemon). */
  posture: string;
  /** The session's resolved provider/model (GET-session echo); null when
   *  the daemon reports none or there is no session yet. */
  resolvedModel: { providerId: string; modelId: string } | null;
  /** The session's effective reasoning-effort tier ("" = the operator's
   *  default, printed as "auto"); the TUI has no such line — Studio picks
   *  effort per session, so a bug report wants it. */
  reasoningEffort: string;
  /** Studio's permission-mode vocabulary. */
  permissionMode: string;
}

/** The lookup token is a closed vocabulary: an unknown value is itself an
 *  invalid response, never printed raw. */
function lookupToken(value: string): ServerInfoLookup {
  return (SERVER_INFO_LOOKUPS as readonly string[]).includes(value)
    ? (value as ServerInfoLookup)
    : "invalid-response";
}

/**
 * The report's documented line set, in order — the TUI's
 * (cmd/mecatui/ui/diagnostics.go) with Studio's additions marked:
 *
 *   Mecatl diagnostics (current client state only):
 *   platform: …
 *   client build: …
 *   server mode: managed|external      (the TUI's embedded|remote)
 *   server build: …
 *   server implementation: …
 *   server endpoint: …                 (Studio's same-origin proxy)
 *   LLM provider endpoint: …
 *   server lookup: …                   (always: Studio is always remote)
 *   deployment: …                      (Studio addition)
 *   posture: …                         (Studio addition)
 *   active provider: …
 *   active model: …
 *   reasoning effort: …                (Studio addition)
 *   permission mode: …
 */
export function buildDiagnosticsReport(input: DiagnosticsReportInput): string {
  return [
    "Mecatl diagnostics (current client state only):",
    `platform: ${diagnosticToken(input.platform)}`,
    `client build: ${diagnosticToken(input.clientBuild)}`,
    `server mode: ${input.mode === "external" ? "external" : "managed"}`,
    `server build: ${diagnosticToken(input.serverInfo?.buildId)}`,
    `server implementation: ${diagnosticToken(input.serverInfo?.serverImplementation)}`,
    `server endpoint: ${diagnosticEndpoint(input.serverEndpoint)}`,
    `LLM provider endpoint: ${diagnosticEndpoint(input.serverInfo?.providerEndpoint)}`,
    `server lookup: ${lookupToken(input.lookup)}`,
    `deployment: ${diagnosticToken(input.deployment)}`,
    `posture: ${diagnosticToken(input.posture)}`,
    `active provider: ${diagnosticToken(input.resolvedModel?.providerId)}`,
    `active model: ${diagnosticToken(input.resolvedModel?.modelId)}`,
    `reasoning effort: ${diagnosticToken(effortTier(input.reasoningEffort))}`,
    `permission mode: ${diagnosticToken(input.permissionMode)}`,
  ].join("\n");
}

/**
 * The fence the TUI's debugger initial prompt puts the report in
 * (`debuggerInitialPrompt`, cmd/mecatui/ui/diagnostics.go) — byte for byte,
 * so a debug session opened from Studio reads like one opened from the TUI.
 * The model is told this is runtime context about the CLIENT, never
 * evidence about the debugged target.
 */
export function wrapAsDebuggerRuntimeContext(report: string): string {
  return `<<<CURRENT_DEBUGGER_RUNTIME_CONTEXT (not target evidence)\n${report}\nCURRENT_DEBUGGER_RUNTIME_CONTEXT>>>`;
}

/**
 * The TUI's complete debugger opening prompt for a diagnosis, with the
 * report fenced as runtime context at the end — the same text mecatui
 * submits when it opens a debug chat, so a Studio-opened debug session can
 * carry it verbatim.
 */
export function debuggerInitialPrompt(
  report: string,
  diagnosis: string,
): string {
  return (
    `Objective\n${diagnosis}` +
    "\n\nRequired workflow\n" +
    "1. Call InspectSession with view=status first.\n" +
    "2. Read the authoritative transcript next; paginate until scan_complete=true when needed. Root/target views must omit scope_handle; only opaque handles returned by related evidence select descendants.\n" +
    "3. Based on symptoms, call activity for tool/lifecycle clues, performance for turn timing/usage, and network for retry/provider/transport clues.\n" +
    "4. Use the runtime context below only for debugger compatibility/transport context, never as evidence about the target.\n" +
    "\nExpected report\n" +
    "- Observed facts, each naming its evidence source\n" +
    "- Likely root cause and confidence\n" +
    "- Missing or unavailable evidence\n" +
    "- Recommended checks or corrective action\n" +
    `\n${wrapAsDebuggerRuntimeContext(report)}`
  );
}
