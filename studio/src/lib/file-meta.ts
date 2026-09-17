import {
  FileCode2,
  FileText,
  Image as ImageIcon,
  type LucideIcon,
  Paperclip,
} from "lucide-react";

/**
 * File-kind classification shared by every surface that shows a file chip
 * (composer attachment pills, message attachment chips, file cards): one
 * icon + label per kind, so the surfaces agree on what a file "is".
 */
export interface FileKindMeta {
  icon: LucideIcon;
  label: string;
}

/**
 * Source-file extensions the app treats as code. Owned here so the
 * file-preview code viewer and the file-kind icons share ONE list (the
 * preview panel previously kept its own copy).
 */
export const CODE_FILE_EXTENSIONS: ReadonlySet<string> = new Set([
  "ts",
  "tsx",
  "js",
  "jsx",
  "mjs",
  "cjs",
  "json",
  "py",
  "sh",
  "bash",
  "zsh",
  "go",
  "rs",
  "java",
  "rb",
  "php",
  "c",
  "cpp",
  "h",
  "hpp",
  "cs",
  "kt",
  "swift",
  "yml",
  "yaml",
  "toml",
  "sql",
  "css",
  "scss",
  "html",
  "xml",
  "graphql",
  "prisma",
]);

const IMAGE_EXTENSIONS: ReadonlySet<string> = new Set([
  "png",
  "jpg",
  "jpeg",
  "gif",
  "webp",
  "svg",
  "heic",
  "heif",
  "avif",
  "bmp",
]);

/** Lower-cased extension of a file name, "" when it has none. */
export function extensionOf(name: string): string {
  const dot = name.lastIndexOf(".");
  return dot === -1 ? "" : name.slice(dot + 1).toLowerCase();
}

/**
 * Icon + label for a file, from its MIME type when available and its
 * extension otherwise. Images → image glyph, PDFs and Markdown → document
 * glyph, recognised source files → code glyph, everything else keeps the
 * paperclip fallback.
 */
export function fileKindMeta(name: string, mime?: string): FileKindMeta {
  const ext = extensionOf(name);
  if (mime?.startsWith("image/") || IMAGE_EXTENSIONS.has(ext)) {
    return { icon: ImageIcon, label: "Image" };
  }
  if (mime === "application/pdf" || ext === "pdf") {
    return { icon: FileText, label: "PDF" };
  }
  if (ext === "md" || ext === "markdown") {
    return { icon: FileText, label: "Markdown" };
  }
  if (CODE_FILE_EXTENSIONS.has(ext)) {
    return { icon: FileCode2, label: "Code" };
  }
  return { icon: Paperclip, label: "File" };
}

/** A file a tool call produced, when the call's args carry one. */
export interface ToolCallFile {
  path: string;
  name: string;
  content?: string;
}

/**
 * Derives the produced file from a tool call's RAW args JSON. Write carries
 * the full body (previewable); Edit names the path but not the final
 * content, so it is deliberately skipped — a card that can't preview is a
 * dead end.
 */
export function fileFromToolCall(
  tool: string,
  rawArgs: string | undefined,
): ToolCallFile | undefined {
  if (tool !== "Write" || !rawArgs) return undefined;
  try {
    const args = JSON.parse(rawArgs) as {
      // The daemon's Write takes "path"; "file_path" is accepted for
      // robustness across tool-schema variants.
      path?: unknown;
      file_path?: unknown;
      content?: unknown;
    };
    const path =
      typeof args.path === "string" && args.path
        ? args.path
        : typeof args.file_path === "string" && args.file_path
          ? args.file_path
          : undefined;
    if (!path) return undefined;
    const name = path.split("/").filter(Boolean).pop() ?? "file";
    return {
      path,
      name,
      content: typeof args.content === "string" ? args.content : undefined,
    };
  } catch {
    return undefined;
  }
}
