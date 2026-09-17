import { resolve } from "node:path";
import { describe, expect, it, vi } from "vitest";
import {
  loadOperatorPalettes,
  OPERATOR_PALETTE_FILE_LIMIT,
} from "./operator-palettes";

/** The committed fixtures the hermetic server suite also points Studio at. */
const FIXTURES = resolve(import.meta.dirname, "../../tests/fixtures/palettes");

/**
 * The server half of `--theme-dir`: an unset directory is simply "no operator
 * palettes", a broken file is skipped and logged (never described to the
 * browser), a valid one comes back as a document the browser re-validates.
 */
describe("loadOperatorPalettes", () => {
  it("returns nothing when the variable is unset or blank", async () => {
    const warn = vi.fn();
    expect(await loadOperatorPalettes(undefined, warn)).toEqual([]);
    expect(await loadOperatorPalettes("   ", warn)).toEqual([]);
    expect(warn).not.toHaveBeenCalled();
  });

  it("logs and returns nothing for a directory that cannot be read", async () => {
    const warn = vi.fn();
    expect(
      await loadOperatorPalettes(resolve(FIXTURES, "does-not-exist"), warn),
    ).toEqual([]);
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn.mock.calls[0]?.[0]).toMatch(/cannot read STUDIO_PALETTE_DIR=/);
  });

  it("serves the valid fixture and skips the broken one with a reason", async () => {
    const warn = vi.fn();
    const palettes = await loadOperatorPalettes(FIXTURES, warn);
    expect(palettes.map((p) => p.name)).toEqual(["midnight"]);
    expect(palettes[0]).toMatchObject({
      name: "midnight",
      label: "Midnight",
      palette: { brand: "#4f7cff" },
      dark: { brand: "#7c9cff" },
    });
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn.mock.calls[0]?.[0]).toMatch(
      /^skipped broken\.json: "palette\.brand" is not a supported colour/,
    );
  });

  it("prefixes its default log line so an operator can find it", async () => {
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {});
    try {
      await loadOperatorPalettes(FIXTURES);
      expect(spy).toHaveBeenCalledWith(
        expect.stringMatching(/^\[palettes\] skipped broken\.json/),
      );
    } finally {
      spy.mockRestore();
    }
  });

  it("caps the number of files it reads", () => {
    expect(OPERATOR_PALETTE_FILE_LIMIT).toBe(32);
  });
});
