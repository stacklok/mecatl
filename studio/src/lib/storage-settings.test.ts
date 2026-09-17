import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  BUILTIN_STORAGE_DEFAULTS,
  DEFAULT_STORE_DIR,
  normalizeStorageSettings,
  PERSISTENCE_MODES,
  resolveStoreDir,
  storageArgs,
  storageDefaultsFromEnv,
  storageStatus,
} from "./storage-settings.mjs";

/**
 * The controller's session-store grammar: what a saved document or POST
 * /storage body normalises to, where a relative location lands, and — the
 * half that decides whether chats survive — exactly which mecated flag a
 * document becomes: `--store-dir <absolute>` for a durable store, NOTHING
 * for in-memory (mecated has no --no-store; an empty --store-dir IS the
 * in-memory store).
 */

const workspace = "/srv/checkout/mecatl";

const thrown = (
  fn: () => unknown,
): { message: string; statusCode?: number } => {
  try {
    fn();
  } catch (error) {
    return error as { message: string; statusCode?: number };
  }
  throw new Error("expected a throw");
};

describe("normalizeStorageSettings", () => {
  it("defaults to a durable store at the historical location", () => {
    expect(PERSISTENCE_MODES).toEqual(["durable", "memory"]);
    expect(DEFAULT_STORE_DIR).toBe(".scratch/studio-sessions");
    for (const input of [undefined, null, {}, [], "durable", 42]) {
      expect(normalizeStorageSettings(input)).toEqual({
        persistence: "durable",
        storeDir: ".scratch/studio-sessions",
      });
    }
  });

  it("accepts both modes, case- and whitespace-insensitively", () => {
    expect(normalizeStorageSettings({ persistence: " MEMORY " })).toEqual({
      persistence: "memory",
      storeDir: DEFAULT_STORE_DIR,
    });
    expect(
      normalizeStorageSettings({ persistence: "Durable" }).persistence,
    ).toBe("durable");
  });

  it("rejects an unknown persistence with a 400 instead of quietly picking one", () => {
    for (const persistence of ["disk", "none", "", "1", true, 0]) {
      const error = thrown(() => normalizeStorageSettings({ persistence }));
      expect(error.message).toMatch(/Unknown persistence/);
      expect(error.statusCode).toBe(400);
    }
  });

  it("trims the location and keeps an absolute or relative path verbatim otherwise", () => {
    expect(
      normalizeStorageSettings({ storeDir: "  /var/lib/mecatl/sessions " })
        .storeDir,
    ).toBe("/var/lib/mecatl/sessions");
    expect(
      normalizeStorageSettings({ storeDir: "state/sessions" }).storeDir,
    ).toBe("state/sessions");
  });

  it("rejects an empty, whitespace-only, non-string or NUL-bearing location (400)", () => {
    for (const storeDir of ["", "   ", "\t\n", 7, true, {}, "/tmp/a\0b"]) {
      const error = thrown(() => normalizeStorageSettings({ storeDir }));
      expect(error.statusCode, String(storeDir)).toBe(400);
      expect(error.message).toMatch(/Store location/);
    }
    expect(
      thrown(() => normalizeStorageSettings({ storeDir: "x".repeat(5000) }))
        .message,
    ).toMatch(/longer than/);
  });

  it("falls back to the GIVEN defaults only for an absent location", () => {
    const defaults = { persistence: "memory", storeDir: "/data/sessions" };
    expect(normalizeStorageSettings({}, defaults)).toEqual(defaults);
    expect(normalizeStorageSettings({ storeDir: null }, defaults)).toEqual(
      defaults,
    );
    expect(
      normalizeStorageSettings({ persistence: "durable" }, defaults),
    ).toEqual({ persistence: "durable", storeDir: "/data/sessions" });
  });
});

describe("resolveStoreDir", () => {
  it("resolves the historical default under the WORKSPACE (the spawn cwd), not under studio/", () => {
    // Every chat saved before the setting existed lives here; changing the
    // base would empty the sidebar after the upgrade.
    expect(
      resolveStoreDir({ storeDir: ".scratch/studio-sessions" }, workspace),
    ).toBe(resolve(workspace, ".scratch/studio-sessions"));
    expect(
      resolveStoreDir({ storeDir: ".scratch/studio-sessions" }, workspace),
    ).toBe("/srv/checkout/mecatl/.scratch/studio-sessions");
  });

  it("keeps an absolute location as given", () => {
    expect(resolveStoreDir({ storeDir: "/var/lib/mecatl" }, workspace)).toBe(
      "/var/lib/mecatl",
    );
  });
});

describe("storageArgs", () => {
  it("passes --store-dir with the RESOLVED path for a durable store", () => {
    expect(storageArgs(normalizeStorageSettings({}), workspace)).toEqual([
      "--store-dir",
      "/srv/checkout/mecatl/.scratch/studio-sessions",
    ]);
    expect(
      storageArgs(
        normalizeStorageSettings({ storeDir: "/var/lib/mecatl/sessions" }),
        workspace,
      ),
    ).toEqual(["--store-dir", "/var/lib/mecatl/sessions"]);
  });

  it("passes NOTHING for in-memory — never an empty --store-dir, never a --no-store", () => {
    expect(
      storageArgs(
        normalizeStorageSettings({ persistence: "memory" }),
        workspace,
      ),
    ).toEqual([]);
  });
});

describe("storageDefaultsFromEnv", () => {
  it("is the built-in default with nothing set", () => {
    expect(storageDefaultsFromEnv({})).toEqual(BUILTIN_STORAGE_DEFAULTS);
    expect(
      storageDefaultsFromEnv({
        MECATL_STUDIO_STORE_DIR: "  ",
        MECATL_STUDIO_NO_STORE: "",
      }),
    ).toEqual(BUILTIN_STORAGE_DEFAULTS);
  });

  it("reads MECATL_STUDIO_STORE_DIR (absolute or workspace-relative) and MECATL_STUDIO_NO_STORE=1", () => {
    expect(
      storageDefaultsFromEnv({ MECATL_STUDIO_STORE_DIR: " /data/sessions " }),
    ).toEqual({ persistence: "durable", storeDir: "/data/sessions" });
    expect(
      storageDefaultsFromEnv({ MECATL_STUDIO_STORE_DIR: "state/sessions" }),
    ).toEqual({ persistence: "durable", storeDir: "state/sessions" });
    expect(storageDefaultsFromEnv({ MECATL_STUDIO_NO_STORE: "1" })).toEqual({
      persistence: "memory",
      storeDir: DEFAULT_STORE_DIR,
    });
    expect(
      storageDefaultsFromEnv({ MECATL_STUDIO_NO_STORE: "0" }).persistence,
    ).toBe("durable");
  });

  it("throws on a value nobody meant, like a bad MECATL_STUDIO_PROVIDER does", () => {
    expect(
      thrown(() => storageDefaultsFromEnv({ MECATL_STUDIO_NO_STORE: "yes" }))
        .message,
    ).toMatch(/MECATL_STUDIO_NO_STORE/);
    expect(
      thrown(() =>
        storageDefaultsFromEnv({ MECATL_STUDIO_STORE_DIR: "/tmp/a\0b" }),
      ).message,
    ).toMatch(/MECATL_STUDIO_STORE_DIR/);
  });

  it("layers: saved state file > environment > built-in default", () => {
    const env = storageDefaultsFromEnv({
      MECATL_STUDIO_STORE_DIR: "/data/sessions",
    });
    // No state file yet: the env default applies.
    expect(normalizeStorageSettings(null, env)).toEqual({
      persistence: "durable",
      storeDir: "/data/sessions",
    });
    // A saved document wins over the env, field by field.
    expect(normalizeStorageSettings({ persistence: "memory" }, env)).toEqual({
      persistence: "memory",
      storeDir: "/data/sessions",
    });
    expect(
      normalizeStorageSettings({ storeDir: "/elsewhere" }, env).storeDir,
    ).toBe("/elsewhere");
    // Nothing anywhere: the built-in default.
    expect(normalizeStorageSettings(null, storageDefaultsFromEnv({}))).toEqual(
      BUILTIN_STORAGE_DEFAULTS,
    );
  });
});

describe("storageStatus", () => {
  it("reports the resolved dir for a durable store and an empty dir for in-memory, plus the resolved default", () => {
    const defaults = storageDefaultsFromEnv({});
    expect(
      storageStatus(normalizeStorageSettings({}), workspace, defaults),
    ).toEqual({
      persistence: "durable",
      dir: "/srv/checkout/mecatl/.scratch/studio-sessions",
      storeDir: ".scratch/studio-sessions",
      defaultDir: "/srv/checkout/mecatl/.scratch/studio-sessions",
      defaultPersistence: "durable",
      managedBy: "studio",
    });
    expect(
      storageStatus(
        normalizeStorageSettings({
          persistence: "memory",
          storeDir: "keep/me",
        }),
        workspace,
        defaults,
      ),
    ).toMatchObject({ persistence: "memory", dir: "", storeDir: "keep/me" });
  });
});

/**
 * The spawn-side contract: the store flag must come from the helper (never
 * a literal), the durable directory must exist before the spawn (mecated
 * does not create it), and the saved document must be loaded before the
 * first start.
 */
describe("local-controller spawn", () => {
  const source = readFileSync(
    resolve(
      dirname(fileURLToPath(import.meta.url)),
      "../../scripts/local-controller.mjs",
    ),
    "utf8",
  );

  it("no longer hard-codes the store directory", () => {
    expect(source).not.toMatch(/["'`]--store-dir["'`]/);
    expect(source).not.toMatch(/["'`]\.scratch\/studio-sessions["'`]/);
  });

  it("spreads the per-root storage args over the effective document into the mecated args, right after --workspace", () => {
    // storageArgsFor (src/lib/workspace-config.mjs) wraps storageArgs'
    // grammar with the per-root store directory: byte-identical for the
    // default root, `-<rootKey>`-suffixed for a chosen one.
    expect(source).toMatch(
      /"--workspace",\s*workspace,\s*\.\.\.storageArgsFor\(storage, workspace, defaultWorkspace\),/,
    );
  });

  it("creates the durable directory before the spawn and loads the saved document before the first start", () => {
    expect(source).toMatch(
      /if \(storage\.persistence === "durable"\)\s*await mkdir\(storeDirFor\(storage, workspace, defaultWorkspace\), \{\s*recursive: true,?\s*\}\)/,
    );
    expect(source).toMatch(/storageSettings = await loadStorageSettings\(\);/);
    expect(source).toMatch(/requestURL\.pathname === "\/storage"/);
  });
});
