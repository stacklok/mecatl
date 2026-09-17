import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  ALLOW_ALL_POSTURES,
  normalizePermissions,
  POSTURES,
  permissionArgs,
  postureImpliesTrust,
  postureRank,
} from "./controller-permissions.mjs";

/**
 * The controller's permissions grammar: what a saved document normalises
 * to, and — the security-relevant half — exactly which mecated flags a
 * document becomes. `--posture` is ALWAYS passed (strict included) so
 * Studio's saved tier out-ranks an imported operator settings.yaml
 * `posture:` key in BOTH directions; `--trust-project` rides the saved
 * switch or the trust-once grant; `--no-shell` is the shell-less mode.
 */

describe("normalizePermissions", () => {
  it("defaults to mecated's own defaults: strict, untrusted, shell on", () => {
    for (const input of [undefined, null, {}, [], "strict", 42]) {
      expect(normalizePermissions(input)).toEqual({
        posture: "strict",
        trustProject: false,
        noShell: false,
        trustAnchor: "",
      });
    }
  });

  it("accepts every ladder tier, case- and whitespace-insensitively", () => {
    expect(POSTURES).toEqual(["strict", "trusted", "auto", "yolo"]);
    for (const posture of POSTURES) {
      expect(normalizePermissions({ posture }).posture).toBe(posture);
      expect(
        normalizePermissions({ posture: ` ${posture.toUpperCase()} ` }).posture,
      ).toBe(posture);
    }
  });

  it("rejects an unknown posture instead of quietly downgrading it", () => {
    for (const posture of ["paranoid", "YOLO!", "trust", "0"]) {
      expect(() => normalizePermissions({ posture })).toThrow(
        /Unknown posture/,
      );
    }
    // The controller answers the thrown grammar error as a 400, not a 500.
    let caught: { statusCode?: number } | null = null;
    try {
      normalizePermissions({ posture: "paranoid" });
    } catch (error) {
      caught = error as { statusCode?: number };
    }
    expect(caught?.statusCode).toBe(400);
  });

  it("only honours real booleans for the switches", () => {
    expect(normalizePermissions({ trustProject: true }).trustProject).toBe(
      true,
    );
    expect(normalizePermissions({ noShell: true }).noShell).toBe(true);
    for (const truthy of ["true", 1, "yes", {}]) {
      expect(
        normalizePermissions({ trustProject: truthy, noShell: truthy }),
      ).toMatchObject({ trustProject: false, noShell: false });
    }
  });

  it("keeps a bounded trust anchor and drops anything else", () => {
    expect(
      normalizePermissions({ trustAnchor: "sha256:abc" }).trustAnchor,
    ).toBe("sha256:abc");
    expect(
      normalizePermissions({ trustAnchor: "x".repeat(600) }).trustAnchor,
    ).toHaveLength(512);
    expect(normalizePermissions({ trustAnchor: 7 }).trustAnchor).toBe("");
  });
});

describe("permissionArgs", () => {
  it("always passes --posture, strict included, so Studio's tier out-ranks an imported posture: key", () => {
    expect(permissionArgs(normalizePermissions({}))).toEqual([
      "--posture",
      "strict",
    ]);
  });

  it("passes the saved tier verbatim", () => {
    expect(permissionArgs(normalizePermissions({ posture: "auto" }))).toEqual([
      "--posture",
      "auto",
    ]);
    expect(permissionArgs(normalizePermissions({ posture: "yolo" }))).toEqual([
      "--posture",
      "yolo",
    ]);
  });

  it("adds --trust-project for the saved switch OR the trust-once grant", () => {
    expect(
      permissionArgs(normalizePermissions({ trustProject: true })),
    ).toEqual(["--posture", "strict", "--trust-project"]);
    expect(
      permissionArgs(normalizePermissions({}), { trustOnce: true }),
    ).toEqual(["--posture", "strict", "--trust-project"]);
    // Both set is still ONE flag.
    expect(
      permissionArgs(normalizePermissions({ trustProject: true }), {
        trustOnce: true,
      }),
    ).toEqual(["--posture", "strict", "--trust-project"]);
  });

  it("withholds --trust-project from a DRIFTED saved grant (the daemon's own fail-safe arm), while trust-once still grants", () => {
    expect(
      permissionArgs(normalizePermissions({ trustProject: true }), {
        trustDrifted: true,
      }),
    ).toEqual(["--posture", "strict"]);
    expect(
      permissionArgs(normalizePermissions({ trustProject: true }), {
        trustDrifted: true,
        trustOnce: true,
      }),
    ).toEqual(["--posture", "strict", "--trust-project"]);
    // Drift without a saved grant changes nothing (there is nothing to withhold).
    expect(
      permissionArgs(normalizePermissions({}), { trustDrifted: true }),
    ).toEqual(["--posture", "strict"]);
  });

  it("adds --no-shell for shell-less mode", () => {
    expect(
      permissionArgs(
        normalizePermissions({
          posture: "trusted",
          trustProject: true,
          noShell: true,
        }),
      ),
    ).toEqual(["--posture", "trusted", "--trust-project", "--no-shell"]);
  });

  it("never emits any other flag — in particular never --headless or --yolo", () => {
    for (const posture of POSTURES) {
      for (const trustProject of [false, true]) {
        for (const noShell of [false, true]) {
          const args = permissionArgs(
            normalizePermissions({ posture, trustProject, noShell }),
            { trustOnce: trustProject },
          );
          const flags = args.filter((arg) => arg.startsWith("--"));
          expect(
            flags.every((flag) =>
              ["--posture", "--trust-project", "--no-shell"].includes(flag),
            ),
            args.join(" "),
          ).toBe(true);
        }
      }
    }
  });
});

describe("posture ladder helpers", () => {
  it("ranks the ladder strict < trusted < auto < yolo, unknown below strict", () => {
    expect(POSTURES.map(postureRank)).toEqual([0, 1, 2, 3]);
    expect(postureRank("paranoid")).toBe(-1);
    expect(postureRank("AUTO")).toBe(2);
  });

  it("names trusted and above as trust-implying, auto and yolo as allow-all", () => {
    expect(POSTURES.filter(postureImpliesTrust)).toEqual([
      "trusted",
      "auto",
      "yolo",
    ]);
    expect(ALLOW_ALL_POSTURES).toEqual(["auto", "yolo"]);
  });
});

/**
 * The spawn-side contract the Permissions page relies on. mecated raises the
 * project-trust floor for trusted/auto/yolo on INTERACTIVE roots only
 * (internal/app/posture.go applyPosture), so the page's "Trust this project
 * is implied by the posture" claim holds only while the controller never
 * spawns `--headless`; and the flag list must reach the spawn at all.
 */
describe("local-controller spawn", () => {
  const source = readFileSync(
    resolve(
      dirname(fileURLToPath(import.meta.url)),
      "../../scripts/local-controller.mjs",
    ),
    "utf8",
  );

  it("never passes --headless", () => {
    // A quoted flag literal is how an arg reaches `args.push`; prose in a
    // comment may name the flag, an argument may not.
    expect(source).not.toMatch(/["'`]--headless["'`]/);
  });

  it("spreads permissionArgs over the saved document, the trust-once grant and the drift verdict into the mecated args", () => {
    expect(source).toMatch(
      /args\.push\(\s*\.\.\.permissionArgs\(permissionsConfig, \{ trustOnce, trustDrifted \}\),?\s*\)/,
    );
  });

  it("never writes a posture key into a settings file — flags only", () => {
    // The only YAML the controller renders is built from string literals
    // (`"models:"`, …); a `posture:` line inside one would be a settings key.
    // The /status JSON object key `posture:` is unquoted and not YAML.
    expect(source).not.toMatch(/["'`][^"'`\n]*\bposture:/);
    expect(source).not.toMatch(/trustedWorkspaces/);
  });
});
