/**
 * The durable OIDC credential store for Studio's server tier — the
 * `mecatui login --credential-store file` analogue (ADR 0277's encrypted
 * store; an OS keyring is out of scope for a headless server process).
 *
 * Opt-in only: `MECATL_OIDC_TOKEN_STORE=file` plus a 32-byte base64
 * `MECATL_OIDC_TOKEN_STORE_KEY`. The token-store snapshot is written
 * AES-256-GCM-encrypted (random 96-bit IV, the key's SHA-256 prefix as the
 * key id, the envelope version as additional authenticated data) to
 * `.scratch/oidc-tokens.json` (0600, temp file + rename) on every change and
 * loaded once at startup. A file the current key cannot open — a rotated key,
 * a tampered ciphertext, a foreign envelope — reads as SIGNED-OUT, never as
 * a throw: losing a session is recoverable, refusing to start is not.
 * Sign-out deletes the file. Tokens still never reach the browser (rule 3).
 */
import {
  createCipheriv,
  createDecipheriv,
  createHash,
  randomBytes,
} from "node:crypto";
import {
  mkdirSync,
  readFileSync,
  renameSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { dirname, resolve } from "node:path";
import type {
  PersistedTokens,
  StoredTokens,
  TokenPersistence,
} from "./token-store";

export const TOKEN_STORE_KEY_BYTES = 32;
const TOKEN_FILE_ENVELOPE_VERSION = 1;
const ENVELOPE_AAD = Buffer.from("mecatl-studio-oidc-tokens/1");
const DEFAULT_TOKEN_FILE = ".scratch/oidc-tokens.json";

export type TokenFileEnvelope = {
  version: typeof TOKEN_FILE_ENVELOPE_VERSION;
  kid: string;
  iv: string;
  ciphertext: string;
  tag: string;
};

/** Decode and validate MECATL_OIDC_TOKEN_STORE_KEY: base64 of exactly 32
 * bytes. Throws a harness-authored message (the value is never echoed). */
export function parseTokenStoreKey(raw: string | undefined): Buffer {
  const trimmed = (raw ?? "").trim();
  if (!trimmed) throw new Error("MECATL_OIDC_TOKEN_STORE_KEY is not set.");
  if (!/^[A-Za-z0-9+/_-]+={0,2}$/.test(trimmed))
    throw new Error("MECATL_OIDC_TOKEN_STORE_KEY is not base64.");
  const key = Buffer.from(
    trimmed.replaceAll("-", "+").replaceAll("_", "/"),
    "base64",
  );
  if (key.length !== TOKEN_STORE_KEY_BYTES)
    throw new Error(
      `MECATL_OIDC_TOKEN_STORE_KEY must decode to ${TOKEN_STORE_KEY_BYTES} bytes (got ${key.length}).`,
    );
  return key;
}

/** Key id: the first 16 hex characters of SHA-256(key). Identifies which key
 * wrote a file without revealing the key. */
export function keyId(key: Buffer): string {
  return createHash("sha256").update(key).digest("hex").slice(0, 16);
}

export function encryptSnapshot(
  plaintext: string,
  key: Buffer,
): TokenFileEnvelope {
  const iv = randomBytes(12);
  const cipher = createCipheriv("aes-256-gcm", key, iv);
  cipher.setAAD(ENVELOPE_AAD);
  const ciphertext = Buffer.concat([
    cipher.update(plaintext, "utf8"),
    cipher.final(),
  ]);
  return {
    version: TOKEN_FILE_ENVELOPE_VERSION,
    kid: keyId(key),
    iv: iv.toString("base64"),
    ciphertext: ciphertext.toString("base64"),
    tag: cipher.getAuthTag().toString("base64"),
  };
}

/** Null on ANY problem — wrong key id, tampered bytes, malformed envelope. */
export function decryptSnapshot(envelope: unknown, key: Buffer): string | null {
  if (typeof envelope !== "object" || envelope === null) return null;
  const e = envelope as Partial<TokenFileEnvelope>;
  if (
    e.version !== TOKEN_FILE_ENVELOPE_VERSION ||
    typeof e.kid !== "string" ||
    typeof e.iv !== "string" ||
    typeof e.ciphertext !== "string" ||
    typeof e.tag !== "string"
  )
    return null;
  if (e.kid !== keyId(key)) return null;
  try {
    const decipher = createDecipheriv(
      "aes-256-gcm",
      key,
      Buffer.from(e.iv, "base64"),
    );
    decipher.setAAD(ENVELOPE_AAD);
    decipher.setAuthTag(Buffer.from(e.tag, "base64"));
    return Buffer.concat([
      decipher.update(Buffer.from(e.ciphertext, "base64")),
      decipher.final(),
    ]).toString("utf8");
  } catch {
    return null;
  }
}

function isStoredTokens(value: unknown): value is StoredTokens {
  if (typeof value !== "object" || value === null) return false;
  const t = value as Record<string, unknown>;
  return (
    typeof t.accessToken === "string" &&
    typeof t.refreshToken === "string" &&
    typeof t.expiresAt === "number" &&
    Number.isFinite(t.expiresAt) &&
    typeof t.claims === "object" &&
    t.claims !== null
  );
}

function parsePersisted(plaintext: string): PersistedTokens | null {
  try {
    const value = JSON.parse(plaintext) as Record<string, unknown>;
    if (!isStoredTokens(value.tokens)) return null;
    return {
      tokens: value.tokens,
      binding: typeof value.binding === "string" ? value.binding : "",
    };
  } catch {
    return null;
  }
}

export type FileTokenPersistenceOptions = {
  path: string;
  key: Buffer;
  /** Operator-facing warnings (a file the key cannot open, an I/O failure);
   * defaults to stderr. */
  warn?: (message: string) => void;
};

/**
 * The file-backed `TokenPersistence`. Every I/O failure is caught: a save
 * failure warns (the in-memory session still works for this process), a
 * load failure reads as signed-out.
 */
export function createFileTokenPersistence(
  options: FileTokenPersistenceOptions,
): TokenPersistence {
  const path = resolve(options.path);
  const warn =
    options.warn ??
    ((message: string) => console.warn(`[mecatl-studio] ${message}`));
  return {
    load() {
      let raw: string;
      try {
        raw = readFileSync(path, "utf8");
      } catch (error) {
        if ((error as NodeJS.ErrnoException)?.code !== "ENOENT")
          warn("The OIDC token file could not be read; starting signed out.");
        return null;
      }
      let envelope: unknown;
      try {
        envelope = JSON.parse(raw);
      } catch {
        warn("The OIDC token file is not valid JSON; starting signed out.");
        return null;
      }
      const plaintext = decryptSnapshot(envelope, options.key);
      if (plaintext === null) {
        warn(
          "The OIDC token file could not be opened with MECATL_OIDC_TOKEN_STORE_KEY (rotated key or tampered file); starting signed out.",
        );
        return null;
      }
      const persisted = parsePersisted(plaintext);
      if (!persisted)
        warn(
          "The OIDC token file has an unexpected shape; starting signed out.",
        );
      return persisted;
    },
    save(persisted) {
      try {
        mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
        const temp = `${path}.${process.pid}.${randomBytes(4).toString("hex")}.tmp`;
        writeFileSync(
          temp,
          JSON.stringify(
            encryptSnapshot(JSON.stringify(persisted), options.key),
          ),
          { mode: 0o600 },
        );
        renameSync(temp, path);
      } catch {
        warn(
          "The OIDC token file could not be written; this sign-in will not survive a restart.",
        );
      }
    },
    clear() {
      try {
        unlinkSync(path);
      } catch (error) {
        if ((error as NodeJS.ErrnoException)?.code !== "ENOENT")
          warn("The OIDC token file could not be removed on sign-out.");
      }
    },
  };
}

/** The env slice read here (`process.env`-shaped so it can be passed as is):
 * MECATL_OIDC_TOKEN_STORE, MECATL_OIDC_TOKEN_STORE_KEY,
 * MECATL_OIDC_TOKEN_STORE_PATH. */
export type TokenStoreEnv = Record<string, string | undefined>;

export type TokenStoreSelection = {
  kind: "memory" | "file";
  persistence?: TokenPersistence;
  /** Set when `file` was requested but could not be honoured; the store
   * then stays in memory so a typo never bricks sign-in. */
  problem?: string;
};

/** Resolve the `MECATL_OIDC_TOKEN_STORE` family (default: memory). */
export function resolveTokenStore(
  env: TokenStoreEnv,
  cwd: string,
  warn?: (message: string) => void,
): TokenStoreSelection {
  const requested = (env.MECATL_OIDC_TOKEN_STORE ?? "").trim().toLowerCase();
  if (!requested || requested === "memory") return { kind: "memory" };
  if (requested !== "file")
    return {
      kind: "memory",
      problem: `MECATL_OIDC_TOKEN_STORE must be "memory" or "file"; tokens stay in memory.`,
    };
  let key: Buffer;
  try {
    key = parseTokenStoreKey(env.MECATL_OIDC_TOKEN_STORE_KEY);
  } catch (error) {
    return {
      kind: "memory",
      problem: `${error instanceof Error ? error.message : "Invalid key."} Tokens stay in memory.`,
    };
  }
  const path = resolve(
    cwd,
    (env.MECATL_OIDC_TOKEN_STORE_PATH ?? "").trim() || DEFAULT_TOKEN_FILE,
  );
  return {
    kind: "file",
    persistence: createFileTokenPersistence({ path, key, warn }),
  };
}
