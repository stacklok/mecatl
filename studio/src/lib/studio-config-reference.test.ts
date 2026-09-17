import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join, resolve } from "node:path";
import { describe, expect, it } from "vitest";
import {
  configuredEnvNames,
  STUDIO_ENV_REFERENCE,
} from "./studio-config-reference";

/**
 * The configuration reference is Studio's `--help-flags`: complete, honest
 * about secrets, and never a channel for values. The scan below is the
 * drift guard — a `process.env.X` read anywhere in the server tier, the
 * controller or the build must have a row here, the way the keyboard
 * reference is rendered from the shortcut registry rather than by hand.
 */

// vitest runs from studio/ (vitest.config.mts lives there); under jsdom
// `import.meta.url` is not a file: URL, so the root is the working directory.
const studioRoot = process.cwd();

/** Source trees whose `process.env` reads the reference must cover. */
const SCANNED = ["src/lib", "src/app", "src/components", "scripts"];
const SCANNED_FILES = ["next.config.ts"];

/** Read by the framework or inlined by the build, not operator knobs. */
const NOT_A_KNOB = /^(NODE_ENV|NEXT_PUBLIC_[A-Z0-9_]*)$/;

function sourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) {
      out.push(...sourceFiles(path));
      continue;
    }
    if (!/\.(ts|tsx|mjs)$/.test(name)) continue;
    if (/\.test\.(ts|tsx|mjs)$/.test(name)) continue;
    out.push(path);
  }
  return out;
}

function envReads(): Map<string, string[]> {
  const files = [
    ...SCANNED.flatMap((dir) => sourceFiles(resolve(studioRoot, dir))),
    ...SCANNED_FILES.map((file) => resolve(studioRoot, file)),
  ];
  const reads = new Map<string, string[]>();
  for (const file of files) {
    const source = readFileSync(file, "utf8");
    for (const match of source.matchAll(/process\.env\.([A-Z][A-Z0-9_]*)/g)) {
      const name = match[1];
      if (NOT_A_KNOB.test(name)) continue;
      reads.set(name, [...(reads.get(name) ?? []), file]);
    }
  }
  return reads;
}

describe("STUDIO_ENV_REFERENCE", () => {
  it("names every variable once, in UPPER_SNAKE_CASE, with a purpose and a tier", () => {
    const names = STUDIO_ENV_REFERENCE.map((entry) => entry.name);
    expect(new Set(names).size).toBe(names.length);
    for (const entry of STUDIO_ENV_REFERENCE) {
      expect(entry.name).toMatch(/^[A-Z][A-Z0-9_]*$/);
      expect(entry.purpose.trim().length).toBeGreaterThan(20);
      expect(entry.purpose.trim().endsWith(".")).toBe(true);
    }
  });

  it("marks the bearer credential — and only it — as a secret", () => {
    const secrets = STUDIO_ENV_REFERENCE.filter((entry) => entry.secret).map(
      (entry) => entry.name,
    );
    expect(secrets).toEqual(["MECATL_AUTH_TOKEN"]);
  });

  it("covers every process.env read in the server tier, the controller and the build", () => {
    expect(
      existsSync(resolve(studioRoot, "next.config.ts")),
      "run vitest from studio/",
    ).toBe(true);
    const missing = [...envReads()]
      .filter(([name]) => !STUDIO_ENV_REFERENCE.some((e) => e.name === name))
      .map(([name, files]) => `${name} (${files.join(", ")})`);
    expect(missing).toEqual([]);
  });
});

describe("configuredEnvNames", () => {
  it("answers a boolean per reference name and never a value", () => {
    const configured = configuredEnvNames({
      MECATL_AUTH_TOKEN: "sekrit",
      MECATL_BASE_URL: "   ",
      UNRELATED: "1",
    });
    expect(Object.keys(configured).sort()).toEqual(
      STUDIO_ENV_REFERENCE.map((entry) => entry.name).sort(),
    );
    expect(configured.MECATL_AUTH_TOKEN).toBe(true);
    // Blank counts as unset, like every reader's `?.trim()`.
    expect(configured.MECATL_BASE_URL).toBe(false);
    expect(configured.MECATL_OIDC_ISSUER).toBe(false);
    expect("UNRELATED" in configured).toBe(false);
    expect(JSON.stringify(configured)).not.toContain("sekrit");
  });
});
