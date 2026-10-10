// SPDX-License-Identifier: Apache-2.0

import { FileCode2, FileText, Image as ImageIcon, type LucideIcon, Paperclip } from "lucide-react";
import { localFileKind } from "../features/chat/local-file-preview";

/**
 * File-kind classification for a file chip (the composer's attachment pills):
 * one icon and label per kind, so the surfaces agree on what a file "is".
 * Ported from the prototype (`stack-08`), on top of Studio's own
 * `localFileKind`, so the chip and the file preview share one extension list.
 */
export interface FileKindMeta {
  icon: LucideIcon;
  label: string;
}

const imageExtensions: ReadonlySet<string> = new Set([
  "avif",
  "bmp",
  "gif",
  "heic",
  "heif",
  "jpeg",
  "jpg",
  "png",
  "svg",
  "webp",
]);

/** Lower-cased extension of a file name, "" when it has none. */
export function extensionOf(name: string): string {
  const dot = name.lastIndexOf(".");
  return dot === -1 ? "" : name.slice(dot + 1).toLowerCase();
}

/**
 * Icon and label for a file, from its MIME type when available and its
 * extension otherwise: images get the image glyph, PDFs, Markdown, and plain
 * text the document glyph, source files the code glyph, and anything else the
 * paperclip.
 */
export function fileKindMeta(name: string, mime = ""): FileKindMeta {
  if (imageExtensions.has(extensionOf(name))) return { icon: ImageIcon, label: "Image" };
  switch (localFileKind(name, mime)) {
    case "image":
      return { icon: ImageIcon, label: "Image" };
    case "pdf":
      return { icon: FileText, label: "PDF" };
    case "markdown":
      return { icon: FileText, label: "Markdown" };
    case "code":
      return { icon: FileCode2, label: "Code" };
    case "text":
      return { icon: FileText, label: "Text" };
    default:
      return { icon: Paperclip, label: "File" };
  }
}
