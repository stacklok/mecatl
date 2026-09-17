import type { AgentMessage, AgentSession } from "./types";

/**
 * The Labs "mock features" tour: a single synthetic, clearly-labeled chat
 * demonstrating file cards, canvas previews, and attachment chips.
 * Everything in this module is browser-local demo content — the session id
 * must NEVER reach the daemon (the chat workspace nulls the hook id for it),
 * and the tour renders only while the Labs toggle is on. This is the one
 * sanctioned mock-content exception to the daemon-only rule (CLAUDE.md rule
 * 1): explicit, default-off, and labeled — never a fallback for an
 * unreachable daemon.
 */

export const MOCK_TOUR_SESSION_ID = "mock-feature-tour";

export function isMockTourSession(id: string): boolean {
  return id === MOCK_TOUR_SESSION_ID;
}

// Stable within a page load; the tour only renders client-side (the Labs
// preference hydrates after mount), so no SSR/hydration mismatch.
const NOW = Date.now();
const minutesAgo = (m: number) => NOW - m * 60_000;

const LOGGER_TS = `import { inspect } from "node:util";

export type LogLevel = "debug" | "info" | "warn" | "error";

const LEVEL_WEIGHT: Record<LogLevel, number> = {
  debug: 10,
  info: 20,
  warn: 30,
  error: 40,
};

export interface LoggerOptions {
  /** Minimum level that gets written. Defaults to "info". */
  level?: LogLevel;
  /** Static fields stamped onto every line (service, version, ...). */
  fields?: Record<string, unknown>;
  /** Sink for the rendered line; defaults to process.stdout. */
  write?: (line: string) => void;
}

export class Logger {
  private readonly threshold: number;
  private readonly fields: Record<string, unknown>;
  private readonly write: (line: string) => void;

  constructor(options: LoggerOptions = {}) {
    this.threshold = LEVEL_WEIGHT[options.level ?? "info"];
    this.fields = options.fields ?? {};
    this.write =
      options.write ?? ((line) => process.stdout.write(\`\${line}\\n\`));
  }

  /** A child logger with extra fields bound onto every line. */
  child(fields: Record<string, unknown>): Logger {
    return new Logger({
      fields: { ...this.fields, ...fields },
      write: this.write,
    });
  }

  log(level: LogLevel, message: string, extra?: Record<string, unknown>) {
    if (LEVEL_WEIGHT[level] < this.threshold) return;
    const record = {
      ts: new Date().toISOString(),
      level,
      message,
      ...this.fields,
      ...extra,
    };
    this.write(inspect(record, { breakLength: Infinity }));
  }

  debug = (msg: string, extra?: Record<string, unknown>) =>
    this.log("debug", msg, extra);
  info = (msg: string, extra?: Record<string, unknown>) =>
    this.log("info", msg, extra);
  warn = (msg: string, extra?: Record<string, unknown>) =>
    this.log("warn", msg, extra);
  error = (msg: string, extra?: Record<string, unknown>) =>
    this.log("error", msg, extra);
}
`;

const MIGRATION_NOTES_MD = `# Auth migration notes

Moving token verification from \`jsonwebtoken\` to \`jose\`. This is the
working plan — flag anything that looks off.

## Why

- \`jose\` is ESM-native and runs on the edge runtime.
- First-class \`KeyLike\` handling removes the PEM string juggling.
- Built-in JWKS caching (\`createRemoteJWKSet\`) replaces our hand-rolled one.

## Steps

1. Introduce \`verifyToken()\` behind the existing interface.
2. Dual-run both verifiers in staging, log disagreements.
3. Flip the default, keep the old path behind a flag for one release.
4. Delete \`jsonwebtoken\` and the flag.

## API mapping

| jsonwebtoken            | jose                        |
| ----------------------- | --------------------------- |
| \`jwt.verify(t, key)\`    | \`jwtVerify(t, key, opts)\`   |
| \`jwt.sign(payload)\`     | \`new SignJWT(payload)\`      |
| \`jwt.decode(t)\`         | \`decodeJwt(t)\`              |

## Open questions

- Do we still need HS256 anywhere, or is RS256 everywhere now?
- Clock tolerance: keep the 5s skew allowance?
`;

const JOSE_GUIDE_MD = `# jose migration guide

A condensed guide for swapping \`jsonwebtoken\` out for \`jose\`.

## Verify

\`\`\`ts
import { jwtVerify, createRemoteJWKSet } from "jose";

const jwks = createRemoteJWKSet(new URL("https://issuer/.well-known/jwks.json"));
const { payload } = await jwtVerify(token, jwks, {
  issuer: "https://issuer",
  audience: "studio",
});
\`\`\`

## Sign

\`\`\`ts
import { SignJWT } from "jose";

const jwt = await new SignJWT({ sub: user.id })
  .setProtectedHeader({ alg: "RS256" })
  .setIssuedAt()
  .setExpirationTime("2h")
  .sign(privateKey);
\`\`\`

Errors are typed: catch \`errors.JWTExpired\` instead of string-matching.
`;

const AUTH_MIDDLEWARE_TS = `import type { NextFunction, Request, Response } from "express";
import jwt from "jsonwebtoken";

const PUBLIC_KEY = process.env.AUTH_PUBLIC_KEY ?? "";

export interface AuthedRequest extends Request {
  user?: { id: string; roles: string[] };
}

/** Rejects requests without a valid bearer token. */
export function requireAuth(
  req: AuthedRequest,
  res: Response,
  next: NextFunction,
) {
  const header = req.headers.authorization;
  if (!header?.startsWith("Bearer ")) {
    res.status(401).json({ error: "missing bearer token" });
    return;
  }
  try {
    const payload = jwt.verify(header.slice(7), PUBLIC_KEY, {
      algorithms: ["RS256"],
      clockTolerance: 5,
    }) as { sub: string; roles?: string[] };
    req.user = { id: payload.sub, roles: payload.roles ?? [] };
    next();
  } catch {
    res.status(401).json({ error: "invalid token" });
  }
}
`;

/** A small bar chart as an inline SVG data URI (fixed palette — it's an image). */
function chartDataUri(): string {
  const bars = [
    { label: "Apr", value: 62 },
    { label: "May", value: 78 },
    { label: "Jun", value: 71 },
    { label: "Jul", value: 94 },
    { label: "Aug", value: 118 },
  ];
  const max = 120;
  const body = bars
    .map((bar, i) => {
      const h = Math.round((bar.value / max) * 170);
      const x = 60 + i * 78;
      return (
        `<rect x="${x}" y="${220 - h}" width="46" height="${h}" rx="6" fill="#6366f1"/>` +
        `<text x="${x + 23}" y="242" text-anchor="middle" font-size="13" fill="#64748b">${bar.label}</text>` +
        `<text x="${x + 23}" y="${212 - h}" text-anchor="middle" font-size="12" fill="#334155">${bar.value}k</text>`
      );
    })
    .join("");
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" width="480" height="270" viewBox="0 0 480 270">` +
    `<rect width="480" height="270" rx="12" fill="#f8fafc"/>` +
    `<text x="24" y="34" font-size="16" font-weight="600" fill="#0f172a" font-family="system-ui, sans-serif">Token verifications / month</text>` +
    `<g font-family="system-ui, sans-serif">${body}</g>` +
    `<line x1="48" y1="220" x2="452" y2="220" stroke="#cbd5e1" stroke-width="1"/>` +
    `</svg>`;
  return `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`;
}

/** A tiny valid one-page PDF (738 bytes), embedded as a base64 data URI. */
const PDF_DATA_URI =
  "data:application/pdf;base64," +
  "JVBERi0xLjQKMSAwIG9iago8PCAvVHlwZSAvQ2F0YWxvZyAvUGFnZXMgMiAwIFIgPj4KZW5kb2JqCjIgMCBvYmoKPDwgL1R5cGUgL1BhZ2VzIC9LaWRzIFszIDAgUl0gL0NvdW50IDEgPj4KZW5kb2JqCjMgMCBvYmoKPDwgL1R5cGUgL1BhZ2UgL1BhcmVudCAyIDAgUiAvTWVkaWFCb3ggWzAgMCA2MTIgNzkyXSAvUmVzb3VyY2VzIDw8IC9Gb250IDw8IC9GMSA1IDAgUiA+PiA+PiAvQ29udGVudHMgNCAwIFIgPj4KZW5kb2JqCjQgMCBvYmoKPDwgL0xlbmd0aCAxOTMgPj4Kc3RyZWFtCkJUIC9GMSAyNCBUZiA3MiA3MDAgVGQgKE1vY2sgUERGKSBUaiBFVApCVCAvRjEgMTIgVGYgNzIgNjcwIFRkIChBIHRpbnkgb25lLXBhZ2UgUERGIHJlbmRlcmVkIGJ5IHRoZSBTdHVkaW8gZmVhdHVyZSB0b3VyLikgVGogRVQKQlQgL0YxIDEyIFRmIDcyIDY1MCBUZCAoTm90aGluZyBoZXJlIGNhbWUgZnJvbSB0aGUgZGFlbW9uLikgVGogRVQKZW5kc3RyZWFtCmVuZG9iago1IDAgb2JqCjw8IC9UeXBlIC9Gb250IC9TdWJ0eXBlIC9UeXBlMSAvQmFzZUZvbnQgL0hlbHZldGljYSA+PgplbmRvYmoKeHJlZgowIDYKMDAwMDAwMDAwMCA2NTUzNSBmIAowMDAwMDAwMDA5IDAwMDAwIG4gCjAwMDAwMDAwNTggMDAwMDAgbiAKMDAwMDAwMDExNSAwMDAwMCBuIAowMDAwMDAwMjQxIDAwMDAwIG4gCjAwMDAwMDA0ODUgMDAwMDAgbiAKdHJhaWxlcgo8PCAvU2l6ZSA2IC9Sb290IDEgMCBSID4+CnN0YXJ0eHJlZgo1NTUKJSVFT0YK";

/** The tour transcript. Read-only; the composer is disabled for this chat. */
export const MOCK_TOUR_MESSAGES: AgentMessage[] = [
  {
    id: "mock-1",
    role: "user",
    content:
      "Can you plan our auth migration off jsonwebtoken to jose? I've attached the migration guide, our current middleware, and the usage chart.",
    timestamp: minutesAgo(42),
    attachments: [
      {
        name: "jose-migration-guide.md",
        type: "text/markdown",
        content: JOSE_GUIDE_MD,
      },
      {
        name: "auth-middleware.ts",
        type: "text/typescript",
        content: AUTH_MIDDLEWARE_TS,
      },
      {
        name: "token-usage.svg",
        type: "image/svg+xml",
        url: chartDataUri(),
      },
    ],
  },
  {
    id: "mock-2",
    role: "assistant",
    content:
      "Happy to. First, a structured logger so we can trace both verifiers while they dual-run — every disagreement gets a correlated line:",
    timestamp: minutesAgo(40),
    artifact: {
      name: "logger.ts",
      type: "code",
      content: LOGGER_TS,
    },
  },
  {
    id: "mock-3",
    role: "assistant",
    content:
      "Here's the migration plan as a working doc — the API mapping table covers every call site I found:",
    timestamp: minutesAgo(36),
    artifact: {
      name: "MIGRATION-NOTES.md",
      type: "markdown",
      content: MIGRATION_NOTES_MD,
    },
  },
  {
    id: "mock-4",
    role: "assistant",
    content:
      "Verification volume is climbing, so the JWKS cache matters — here's the trend:",
    timestamp: minutesAgo(30),
    artifact: {
      name: "token-verifications.svg",
      type: "image",
      url: chartDataUri(),
    },
  },
  {
    id: "mock-5",
    role: "assistant",
    content:
      "And a one-page summary you can circulate — it renders right in the preview panel:",
    timestamp: minutesAgo(24),
    artifact: {
      name: "migration-summary.pdf",
      type: "pdf",
      url: PDF_DATA_URI,
    },
  },
  {
    id: "mock-6",
    role: "assistant",
    content:
      "That's the whole tour: file cards open in the canvas panel and attachment chips preview in place. Threads are the real thing — start one from any message's reply action.",
    timestamp: minutesAgo(20),
  },
];

/** The sidebar stand-in row for the tour. canRename/canDelete stay omitted —
 *  the row menus self-gate on capabilities, so mock rows offer no actions. */
export const MOCK_TOUR_SESSION: AgentSession = {
  id: MOCK_TOUR_SESSION_ID,
  title: "Feature tour",
  projectId: null,
  model: "",
  createdAt: minutesAgo(42),
  updatedAt: minutesAgo(15),
  pinned: true,
  archived: false,
  messageCount: MOCK_TOUR_MESSAGES.length,
  isStreaming: false,
  inputTokens: 0,
  outputTokens: 0,
  unread: false,
  estimatedCost: null,
  contextLength: null,
  lastPromptTokens: null,
  thresholdTokens: null,
};
