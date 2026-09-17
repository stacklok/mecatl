import { readdir, readFile, stat } from "node:fs/promises";
import { join } from "node:path";
import {
  type CustomPalette,
  PALETTE_DOCUMENT_MAX_BYTES,
  type PaletteDocument,
  parsePaletteDocument,
  toPaletteDocument,
} from "@/lib/palette-schema";

/** At most this many `*.json` files are read from the directory. */
export const OPERATOR_PALETTE_FILE_LIMIT = 32;
/** The variable the route reads; named here for the server log lines. */
const OPERATOR_PALETTE_DIR_ENV = "STUDIO_PALETTE_DIR";

/**
 * The operator's palette directory — mecatui's `--theme-dir` analogue,
 * read SERVER-side by `/api/palettes`: `STUDIO_PALETTE_DIR` names a
 * directory of `{name, palette}` JSON files, each validated by the shared
 * `parsePaletteDocument`. A broken file is skipped with a server log line
 * (the browser never learns the path or the reason — operator files are the
 * operator's to fix), a later file with a repeated name wins, and the
 * directory is re-read on every request so a change is picked up without a
 * rebuild or restart.
 */
export async function loadOperatorPalettes(
  dir: string | undefined,
  warn: (message: string) => void = (message) =>
    console.warn(`[palettes] ${message}`),
): Promise<PaletteDocument[]> {
  const root = (dir ?? "").trim();
  if (!root) return [];

  let names: string[];
  try {
    const entries = await readdir(root, { withFileTypes: true });
    names = entries
      .filter((entry) => entry.isFile() && entry.name.endsWith(".json"))
      .map((entry) => entry.name)
      .sort();
  } catch (error) {
    warn(
      `cannot read ${OPERATOR_PALETTE_DIR_ENV}=${root}: ${error instanceof Error ? error.message : String(error)}`,
    );
    return [];
  }
  if (names.length > OPERATOR_PALETTE_FILE_LIMIT) {
    warn(
      `${names.length} palette files in ${root}; only the first ${OPERATOR_PALETTE_FILE_LIMIT} (by name) are read`,
    );
    names = names.slice(0, OPERATOR_PALETTE_FILE_LIMIT);
  }

  const byName = new Map<string, CustomPalette>();
  for (const name of names) {
    const path = join(root, name);
    try {
      const info = await stat(path);
      if (info.size > PALETTE_DOCUMENT_MAX_BYTES) {
        warn(
          `skipped ${name}: larger than ${PALETTE_DOCUMENT_MAX_BYTES / 1024} KiB`,
        );
        continue;
      }
      const result = parsePaletteDocument(
        await readFile(path, "utf8"),
        "operator",
      );
      if (!result.ok) {
        warn(`skipped ${name}: ${result.error}`);
        continue;
      }
      byName.set(result.palette.name, result.palette);
    } catch (error) {
      warn(
        `skipped ${name}: ${error instanceof Error ? error.message : String(error)}`,
      );
    }
  }
  return [...byName.values()].map(toPaletteDocument);
}
