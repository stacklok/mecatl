import { randomBytes } from "node:crypto";
import {
  existsSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  createFileTokenPersistence,
  decryptSnapshot,
  encryptSnapshot,
  keyId,
  parseTokenStoreKey,
  resolveTokenStore,
  TOKEN_STORE_KEY_BYTES,
} from "./file-token-store";
import { OidcTokenStore, type StoredTokens } from "./token-store";

/**
 * The encrypted file store (`--credential-store file`): AES-256-GCM round
 * trip, a foreign or rotated key reads as signed-out (never a throw),
 * tampering is rejected, sign-out removes the file, the key is validated,
 * and the token store adopts the mirror at construction.
 */

const KEY = randomBytes(TOKEN_STORE_KEY_BYTES);
const KEY_B64 = KEY.toString("base64");

const tokens = (over: Partial<StoredTokens> = {}): StoredTokens => ({
  accessToken: "access-1",
  refreshToken: "refresh-1",
  expiresAt: 1_700_000_000_000 + 3_600_000,
  claims: { sub: "user-1", email: "op@example.com" },
  ...over,
});

let dir: string;
beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), "mecatl-oidc-tokens-"));
});
afterEach(() => {
  rmSync(dir, { recursive: true, force: true });
});

describe("parseTokenStoreKey", () => {
  it("accepts base64 (standard or url-safe) of exactly 32 bytes", () => {
    expect(parseTokenStoreKey(KEY_B64).equals(KEY)).toBe(true);
    expect(
      parseTokenStoreKey(` ${KEY.toString("base64url")} `).equals(KEY),
    ).toBe(true);
  });

  it("refuses an unset, non-base64 or wrong-length key without echoing it", () => {
    expect(() => parseTokenStoreKey(undefined)).toThrow(/not set/);
    expect(() => parseTokenStoreKey("not base64!!")).toThrow(/not base64/);
    const short = randomBytes(16).toString("base64");
    let message = "";
    try {
      parseTokenStoreKey(short);
    } catch (error) {
      message = (error as Error).message;
    }
    expect(message).toMatch(/32 bytes \(got 16\)/);
    expect(message).not.toContain(short);
  });
});

describe("encryptSnapshot / decryptSnapshot", () => {
  it("round-trips under the same key with a fresh IV each time", () => {
    const a = encryptSnapshot("hello", KEY);
    const b = encryptSnapshot("hello", KEY);
    expect(a.kid).toBe(keyId(KEY));
    expect(a.iv).not.toBe(b.iv);
    expect(a.ciphertext).not.toBe(b.ciphertext);
    expect(decryptSnapshot(a, KEY)).toBe("hello");
  });

  it("returns null for another key, a tampered ciphertext or a malformed envelope", () => {
    const envelope = encryptSnapshot("hello", KEY);
    expect(decryptSnapshot(envelope, randomBytes(32))).toBeNull();
    const bytes = Buffer.from(envelope.ciphertext, "base64");
    bytes[0] ^= 0xff;
    expect(
      decryptSnapshot(
        { ...envelope, ciphertext: bytes.toString("base64") },
        KEY,
      ),
    ).toBeNull();
    expect(decryptSnapshot({ ...envelope, tag: envelope.iv }, KEY)).toBeNull();
    expect(decryptSnapshot({ ...envelope, version: 2 }, KEY)).toBeNull();
    expect(decryptSnapshot("garbage", KEY)).toBeNull();
    expect(decryptSnapshot(null, KEY)).toBeNull();
  });
});

describe("createFileTokenPersistence", () => {
  it("writes an encrypted 0600 file the same key loads back, and clear removes it", () => {
    const path = join(dir, "nested", "oidc-tokens.json");
    const warn = vi.fn();
    const persistence = createFileTokenPersistence({ path, key: KEY, warn });
    expect(persistence.load()).toBeNull(); // ENOENT is silent
    persistence.save({ tokens: tokens(), binding: "hash-1" });
    expect(existsSync(path)).toBe(true);
    if (process.platform !== "win32")
      expect(statSync(path).mode & 0o777).toBe(0o600);
    const raw = readFileSync(path, "utf8");
    expect(raw).not.toContain("access-1");
    expect(raw).not.toContain("refresh-1");
    expect(persistence.load()).toEqual({ tokens: tokens(), binding: "hash-1" });
    expect(warn).not.toHaveBeenCalled();

    persistence.clear();
    expect(existsSync(path)).toBe(false);
    persistence.clear(); // idempotent
    expect(warn).not.toHaveBeenCalled();
  });

  it("reads as signed-out (with a warning, no throw) under a rotated key or a tampered file", () => {
    const path = join(dir, "oidc-tokens.json");
    createFileTokenPersistence({ path, key: KEY, warn: () => {} }).save({
      tokens: tokens(),
      binding: "",
    });
    const rotatedWarn = vi.fn();
    const rotated = createFileTokenPersistence({
      path,
      key: randomBytes(32),
      warn: rotatedWarn,
    });
    expect(rotated.load()).toBeNull();
    expect(rotatedWarn).toHaveBeenCalledWith(
      expect.stringMatching(/rotated key or tampered/),
    );

    writeFileSync(path, "not json");
    const garbageWarn = vi.fn();
    expect(
      createFileTokenPersistence({ path, key: KEY, warn: garbageWarn }).load(),
    ).toBeNull();
    expect(garbageWarn).toHaveBeenCalledWith(
      expect.stringMatching(/not valid JSON/),
    );

    // A well-encrypted envelope whose plaintext is not a snapshot.
    writeFileSync(
      path,
      JSON.stringify(encryptSnapshot('{"tokens":{"nope":1}}', KEY)),
    );
    const shapeWarn = vi.fn();
    expect(
      createFileTokenPersistence({ path, key: KEY, warn: shapeWarn }).load(),
    ).toBeNull();
    expect(shapeWarn).toHaveBeenCalledWith(
      expect.stringMatching(/unexpected shape/),
    );
  });

  it("is adopted by the token store at construction and mirrored on every change", () => {
    const path = join(dir, "oidc-tokens.json");
    const persistence = createFileTokenPersistence({
      path,
      key: KEY,
      warn: () => {},
    });
    const first = new OidcTokenStore(() => 1_700_000_000_000, persistence);
    first.setTokens(tokens(), "profile-a");
    expect(first.binding()).toBe("profile-a");

    const second = new OidcTokenStore(() => 1_700_000_000_000, persistence);
    expect(second.current()).toEqual(tokens());
    expect(second.binding()).toBe("profile-a");
    expect(second.snapshot()).toMatchObject({ state: "signed-in" });

    second.clear("signed-out");
    expect(existsSync(path)).toBe(false);
    expect(second.binding()).toBe("");
    const third = new OidcTokenStore(() => 1_700_000_000_000, persistence);
    expect(third.current()).toBeNull();
  });
});

describe("resolveTokenStore", () => {
  it("defaults to memory and reports a problem for a bad request instead of failing", () => {
    expect(resolveTokenStore({}, dir)).toEqual({ kind: "memory" });
    expect(
      resolveTokenStore({ MECATL_OIDC_TOKEN_STORE: "memory" }, dir),
    ).toEqual({
      kind: "memory",
    });
    expect(
      resolveTokenStore({ MECATL_OIDC_TOKEN_STORE: "keyring" }, dir),
    ).toMatchObject({
      kind: "memory",
      problem: expect.stringMatching(/"memory" or "file"/),
    });
    expect(
      resolveTokenStore({ MECATL_OIDC_TOKEN_STORE: "file" }, dir),
    ).toMatchObject({
      kind: "memory",
      problem: expect.stringMatching(/MECATL_OIDC_TOKEN_STORE_KEY is not set/),
    });
    expect(
      resolveTokenStore(
        {
          MECATL_OIDC_TOKEN_STORE: "file",
          MECATL_OIDC_TOKEN_STORE_KEY: randomBytes(8).toString("base64"),
        },
        dir,
      ),
    ).toMatchObject({
      kind: "memory",
      problem: expect.stringMatching(/32 bytes/),
    });
  });

  it("selects the file store under the cwd-relative default path", () => {
    const selection = resolveTokenStore(
      { MECATL_OIDC_TOKEN_STORE: "file", MECATL_OIDC_TOKEN_STORE_KEY: KEY_B64 },
      dir,
      () => {},
    );
    expect(selection.kind).toBe("file");
    selection.persistence?.save({ tokens: tokens(), binding: "" });
    expect(existsSync(join(dir, ".scratch", "oidc-tokens.json"))).toBe(true);
  });
});
