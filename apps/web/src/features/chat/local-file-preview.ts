// SPDX-License-Identifier: Apache-2.0

export type LocalFileKind = "code" | "image" | "markdown" | "pdf" | "text" | "unsupported";

export interface LocalFilePreview {
  content?: string;
  dataUrl?: string;
  kind: LocalFileKind;
  name: string;
  sent?: boolean;
  size: number;
  type: string;
}

export interface ChatImage {
  data?: string;
  id?: string;
  mimeType: string;
  name: string;
  size?: number;
  url?: string;
}

export interface ImageAttachment extends ChatImage {
  data: string;
  id: string;
  size: number;
}

export const maxImageAttachmentBytes = 10 * 1024 * 1024;
export const maxImageAttachmentCount = 16;
export const maxImagePromptBytes = 20 * 1024 * 1024;

const codeExtensions = new Set([
  "c",
  "css",
  "go",
  "html",
  "java",
  "js",
  "json",
  "jsx",
  "py",
  "rs",
  "sh",
  "sql",
  "ts",
  "tsx",
  "yaml",
  "yml",
]);
const textExtensions = new Set(["csv", "log", "txt", "xml"]);
const maxPreviewBytes = 5 * 1024 * 1024;

export function localFileKind(name: string, type: string): LocalFileKind {
  const extension = name.split(".").at(-1)?.toLowerCase() ?? "";
  if (type.startsWith("image/")) return "image";
  if (type === "application/pdf" || extension === "pdf") return "pdf";
  if (extension === "md" || extension === "markdown") return "markdown";
  if (codeExtensions.has(extension)) return "code";
  if (type.startsWith("text/") || textExtensions.has(extension)) return "text";
  return "unsupported";
}

export async function readLocalFile(file: File): Promise<LocalFilePreview> {
  const kind = localFileKind(file.name, file.type);
  const base = { kind, name: file.name, size: file.size, type: file.type };
  if (file.size > maxPreviewBytes) return { ...base, kind: "unsupported" };
  if (kind === "image" || kind === "pdf") return { ...base, dataUrl: await readDataUrl(file) };
  if (kind !== "unsupported") return { ...base, content: await file.text() };
  return base;
}

export async function readImageAttachment(file: File): Promise<ImageAttachment> {
  if (!file.type.toLowerCase().startsWith("image/")) {
    throw new Error(`${file.name} is not an image.`);
  }
  if (file.size === 0) {
    throw new Error(`${file.name} is empty.`);
  }
  if (file.size > maxImageAttachmentBytes) {
    throw new Error(`${file.name} is larger than the 10 MB image limit.`);
  }
  const dataUrl = await readDataUrl(file);
  const separator = dataUrl.indexOf(",");
  if (separator === -1 || dataUrl.slice(separator + 1).length === 0) {
    throw new Error(`${file.name} could not be read.`);
  }
  return {
    data: dataUrl.slice(separator + 1),
    id: crypto.randomUUID(),
    mimeType: file.type,
    name: file.name,
    size: file.size,
  };
}

export function imageSource(image: ChatImage): string {
  if (image.data) return `data:${image.mimeType};base64,${image.data}`;
  return image.url ?? "";
}

/**
 * How one transcript image renders. The page loads images only from inline
 * sources, so an image persisted as a URL alone is offered as a link that
 * opens it in a new tab rather than an `<img>` that could never load. A URL
 * that is not an absolute http(s) address renders as its name alone.
 */
export type ChatImageDisplay =
  | { kind: "inline"; src: string }
  | { href: string; kind: "link" }
  | { kind: "name" };

export function chatImageDisplay(image: ChatImage): ChatImageDisplay {
  if (image.data) return { kind: "inline", src: imageSource(image) };
  if (image.url && isWebUrl(image.url)) return { href: image.url, kind: "link" };
  return { kind: "name" };
}

function isWebUrl(value: string): boolean {
  try {
    const { protocol } = new URL(value);
    return protocol === "https:" || protocol === "http:";
  } catch {
    return false;
  }
}

export function imagePreview(image: ChatImage, sent: boolean): LocalFilePreview {
  return {
    dataUrl: imageSource(image),
    kind: "image",
    name: image.name,
    sent,
    size: image.size ?? decodedBase64Size(image.data ?? ""),
    type: image.mimeType,
  };
}

function decodedBase64Size(data: string): number {
  if (!data) return 0;
  const padding = data.endsWith("==") ? 2 : data.endsWith("=") ? 1 : 0;
  return Math.max(0, Math.floor((data.length * 3) / 4) - padding);
}

function readDataUrl(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(reader.error ?? new Error("Could not read this file."));
    reader.onload = () => resolve(String(reader.result));
    reader.readAsDataURL(file);
  });
}
