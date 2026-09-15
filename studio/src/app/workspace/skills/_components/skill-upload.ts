/**
 * Pure planning for a multi-file skill upload (a .zip or a picked folder).
 * Normalizes the entry paths into what the controller will write under
 * `<skills-dir>/<name>/` and reports the one thing wrong when the upload
 * cannot become a skill. Byte-content handling stays in the dialog — this
 * module only reasons about paths, so it is trivially unit-testable.
 */

/** Mirrors the controller's caps so a doomed upload fails before the POST. */
export const maxSkillUploadFiles = 200;
export const maxSkillUploadFileBytes = 2 * 1024 * 1024;
export const maxSkillUploadTotalBytes = 8 * 1024 * 1024;

export interface PlannedUpload {
  /** Normalized relative path → index into the caller's original entries. */
  files: { path: string; index: number }[];
  error: string | null;
}

/** macOS zip/Finder noise and hidden entries — never part of a skill. */
function junkSegment(segment: string): boolean {
  return segment.startsWith(".") || segment === "__MACOSX";
}

function validSegments(path: string): boolean {
  if (path.includes("\\")) return false;
  return path
    .split("/")
    .every((segment) => segment !== "" && segment !== "." && segment !== "..");
}

/**
 * Plans a skill upload from raw entry paths (zip entry names, or a folder
 * pick's webkitRelativePaths). Directory entries (trailing "/") and junk
 * (hidden segments, __MACOSX) are dropped; when everything left lives under
 * one shared top-level folder — the shape both "zip a folder" and the folder
 * picker produce — that wrapper is stripped. The result must hold a SKILL.md
 * at its root and stay under the file-count cap.
 */
export function planSkillUpload(paths: string[]): PlannedUpload {
  const survivors: { path: string; index: number }[] = [];
  for (const [index, raw] of paths.entries()) {
    if (raw.endsWith("/")) continue; // a directory entry, not a file
    if (!validSegments(raw)) {
      return { files: [], error: `The upload holds an invalid path: ${raw}` };
    }
    if (raw.split("/").some(junkSegment)) continue;
    survivors.push({ path: raw, index });
  }

  if (survivors.length === 0) {
    return { files: [], error: "The upload holds no usable files." };
  }

  // Strip one shared wrapper folder, but only when the SKILL.md actually
  // lives inside it — a root-level SKILL.md means the paths are already
  // skill-relative.
  let files = survivors;
  const hasRootSkillMd = files.some((file) => file.path === "SKILL.md");
  if (!hasRootSkillMd) {
    const first = files[0].path.split("/")[0];
    const shared = files.every(
      (file) =>
        file.path.startsWith(`${first}/`) &&
        file.path.length > first.length + 1,
    );
    if (shared) {
      files = files.map((file) => ({
        path: file.path.slice(first.length + 1),
        index: file.index,
      }));
    }
  }

  if (!files.some((file) => file.path === "SKILL.md")) {
    return {
      files: [],
      error: "The upload needs a SKILL.md at the folder root.",
    };
  }
  if (files.length > maxSkillUploadFiles) {
    return {
      files: [],
      error: `The upload holds ${files.length} files — skills are limited to ${maxSkillUploadFiles}.`,
    };
  }
  const seen = new Set<string>();
  for (const file of files) {
    // macOS's default filesystem is case-insensitive: two paths differing
    // only by case would silently overwrite each other on write.
    const key = file.path.toLowerCase();
    if (seen.has(key)) {
      return {
        files: [],
        error: `The upload holds duplicate paths: ${file.path}`,
      };
    }
    seen.add(key);
  }
  return { files, error: null };
}

/** Uint8Array → base64, chunked so large assets never blow the arg limit. */
export function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return btoa(binary);
}
