import { validSkillName } from "@/lib/controller-security.mjs";

/**
 * Coerces arbitrary text toward the daemon's skill-name grammar (lowercase
 * letters/digits/hyphen/underscore, 1-64 chars, starting alphanumeric):
 * lowercase, whitespace runs become hyphens, invalid characters are stripped.
 * Returns "" when nothing grammar-valid survives — the caller falls back or
 * leaves the field for the user.
 */
export function sanitizeSkillName(raw: string): string {
  const cleaned = raw
    .toLowerCase()
    .replace(/\s+/g, "-")
    .replace(/[^a-z0-9_-]/g, "")
    .replace(/^[_-]+/, "")
    .slice(0, 64);
  return validSkillName(cleaned) ? cleaned : "";
}

/**
 * One-line `name:` scan of a SKILL.md frontmatter block — a line scan, never
 * a YAML parse, mirroring the controller's `skillDescription`. Best-effort:
 * no frontmatter, no `name:`, or a value that sanitizes to nothing all yield
 * "".
 */
function frontmatterName(markdown: string): string {
  const lines = markdown.split("\n");
  if (lines[0]?.trim() !== "---") return "";
  for (const line of lines.slice(1)) {
    if (line.trim() === "---") break;
    const match = line.match(/^name:\s*(.+)$/);
    if (!match) continue;
    return sanitizeSkillName(match[1].trim().replace(/^["']|["']$/g, ""));
  }
  return "";
}

/**
 * The name an uploaded SKILL.md suggests: the frontmatter `name:` when it
 * yields a grammar-valid slug, else the filename (extension dropped) pushed
 * through the same sanitizer. "" means the file suggested nothing usable and
 * the user must type one.
 */
export function deriveSkillName(fileName: string, content: string): string {
  return (
    frontmatterName(content) ||
    sanitizeSkillName(fileName.replace(/\.[^.]*$/, ""))
  );
}
