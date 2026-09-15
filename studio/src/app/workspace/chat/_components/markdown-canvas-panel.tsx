"use client";

import { DOMParser as PMDOMParser } from "@tiptap/pm/model";
import { EditorContent, useEditor } from "@tiptap/react";
import StarterKit from "@tiptap/starter-kit";
import {
  Bold,
  Code,
  Code2,
  FileCode2,
  FileSpreadsheet,
  FileText,
  Heading1,
  Heading2,
  Heading3,
  Image as ImageIcon,
  Italic,
  List,
  ListOrdered,
  Quote,
} from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { flushSync } from "react-dom";
import { Markdown } from "tiptap-markdown";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import type { Artifact } from "@/features/agent";
import { cn } from "@/lib/utils";
import { FilePreview } from "./file-preview";
import { SidePanel } from "./side-panel";

const CANVAS_ICON_MAP: Record<string, typeof FileText> = {
  spreadsheet: FileSpreadsheet,
  document: FileText,
  code: FileCode2,
  image: ImageIcon,
  pdf: FileText,
  markdown: FileText,
};

// ── Raw-mode toolbar ─────────────────────────────────────────────────────────

interface RawToolbarButton {
  icon: typeof Bold;
  label: string;
  action: (
    content: string,
    selStart: number,
    selEnd: number,
  ) => { text: string; cursor: number };
}

function wrapSelection(
  content: string,
  selStart: number,
  selEnd: number,
  prefix: string,
  suffix: string,
  placeholder = "text",
): { text: string; cursor: number } {
  const selected = content.slice(selStart, selEnd) || placeholder;
  const before = content.slice(0, selStart);
  const after = content.slice(selEnd);
  const text = `${before}${prefix}${selected}${suffix}${after}`;
  return {
    text,
    cursor: selStart + prefix.length + selected.length + suffix.length,
  };
}

const HEADING_RE = /^#{1,6} /;

function prependLine(
  content: string,
  selStart: number,
  prefix: string,
): { text: string; cursor: number } {
  const lineStart = content.lastIndexOf("\n", selStart - 1) + 1;
  const lineContent = content.slice(lineStart);
  const before = content.slice(0, lineStart);
  const isHeadingPrefix = /^#{1,6} $/.test(prefix);
  const lineBody = isHeadingPrefix
    ? lineContent.replace(HEADING_RE, "")
    : lineContent;
  const text = `${before}${prefix}${lineBody}`;
  return { text, cursor: lineStart + prefix.length };
}

const RAW_TOOLBAR: RawToolbarButton[] = [
  {
    icon: Bold,
    label: "Bold",
    action: (c, s, e) => wrapSelection(c, s, e, "**", "**"),
  },
  {
    icon: Italic,
    label: "Italic",
    action: (c, s, e) => wrapSelection(c, s, e, "_", "_"),
  },
  {
    icon: Heading1,
    label: "Heading 1",
    action: (c, s) => prependLine(c, s, "# "),
  },
  {
    icon: Heading2,
    label: "Heading 2",
    action: (c, s) => prependLine(c, s, "## "),
  },
  {
    icon: Heading3,
    label: "Heading 3",
    action: (c, s) => prependLine(c, s, "### "),
  },
  {
    icon: Code,
    label: "Inline code",
    action: (c, s, e) => wrapSelection(c, s, e, "`", "`", "code"),
  },
  {
    icon: Code2,
    label: "Code block",
    action: (c, s, e) => wrapSelection(c, s, e, "```\n", "\n```", "code"),
  },
  {
    icon: List,
    label: "Unordered list",
    action: (c, s) => prependLine(c, s, "- "),
  },
  {
    icon: ListOrdered,
    label: "Ordered list",
    action: (c, s) => prependLine(c, s, "1. "),
  },
  {
    icon: Quote,
    label: "Blockquote",
    action: (c, s) => prependLine(c, s, "> "),
  },
];

// ── Styled-mode toolbar ──────────────────────────────────────────────────────

interface StyledToolbarButton {
  icon: typeof Bold;
  label: string;
  action: (editor: ReturnType<typeof useEditor>) => void;
}

const STYLED_TOOLBAR: StyledToolbarButton[] = [
  {
    icon: Bold,
    label: "Bold",
    action: (e) => e?.chain().focus().toggleBold().run(),
  },
  {
    icon: Italic,
    label: "Italic",
    action: (e) => e?.chain().focus().toggleItalic().run(),
  },
  {
    icon: Heading1,
    label: "Heading 1",
    action: (e) => e?.chain().focus().toggleHeading({ level: 1 }).run(),
  },
  {
    icon: Heading2,
    label: "Heading 2",
    action: (e) => e?.chain().focus().toggleHeading({ level: 2 }).run(),
  },
  {
    icon: Heading3,
    label: "Heading 3",
    action: (e) => e?.chain().focus().toggleHeading({ level: 3 }).run(),
  },
  {
    icon: Code,
    label: "Inline code",
    action: (e) => e?.chain().focus().toggleCode().run(),
  },
  {
    icon: Code2,
    label: "Code block",
    action: (e) => e?.chain().focus().toggleCodeBlock().run(),
  },
  {
    icon: List,
    label: "Unordered list",
    action: (e) => e?.chain().focus().toggleBulletList().run(),
  },
  {
    icon: ListOrdered,
    label: "Ordered list",
    action: (e) => e?.chain().focus().toggleOrderedList().run(),
  },
  {
    icon: Quote,
    label: "Blockquote",
    action: (e) => e?.chain().focus().toggleBlockquote().run(),
  },
];

// ── Markdown storage helpers (outside component for stable references) ───────

type MdStorage = {
  getMarkdown: () => string;
  parser: { parse: (md: string) => unknown };
};

function getMdStorage(e: ReturnType<typeof useEditor>) {
  return (e?.storage as unknown as Record<string, MdStorage>).markdown;
}

function setEditorMarkdown(e: ReturnType<typeof useEditor>, md: string) {
  if (!e || e.isDestroyed) return;
  // Use tiptap-markdown's parser to get HTML, then parse with ProseMirror's
  // DOMParser directly — bypasses any command-override resolution issues.
  const storage = (
    e.storage as unknown as Record<
      string,
      { parser: { parse: (s: string) => string } }
    >
  ).markdown;
  if (!storage?.parser) return;
  const html = storage.parser.parse(md);
  const container = document.createElement("div");
  container.innerHTML = html;
  const doc = PMDOMParser.fromSchema(e.schema).parse(container);
  const tr = e.state.tr.replace(0, e.state.doc.content.size, doc.slice(0));
  e.view.dispatch(tr);
}

// ── Component ────────────────────────────────────────────────────────────────

export function MarkdownCanvasPanel({
  artifact,
  onClose,
  maximized,
  onToggleMaximize,
  windowControls,
}: {
  artifact: Artifact;
  onClose: () => void;
  maximized: boolean;
  onToggleMaximize: () => void;
  windowControls?: boolean;
}) {
  const isDocument = artifact.type === "document";
  const [mode, setMode] = useState<"raw" | "styled">("styled");
  const [editedContent, setEditedContent] = useState(artifact.content ?? "");
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const editedContentRef = useRef(editedContent);

  const editor = useEditor({
    extensions: [StarterKit, Markdown.configure({ transformPastedText: true })],
    content: artifact.content ?? "",
    editorProps: {
      attributes: { spellcheck: "true" },
    },
    onUpdate: ({ editor: e }) => {
      const md = getMdStorage(e).getMarkdown();
      editedContentRef.current = md;
      setEditedContent(md);
    },
  });

  // Reset when a new artifact is opened. Plain setState is correct here:
  // setEditorMarkdown takes the content directly (it never reads the state),
  // and flushSync is illegal inside an effect that fires during mount — the
  // exact path a PDF/image artifact takes when the panel first opens.
  useEffect(() => {
    const content = artifact.content ?? "";
    editedContentRef.current = content;
    setEditedContent(content);
    setMode("styled");
    setEditorMarkdown(editor, content);
  }, [artifact, editor]);

  const applyRawAction = useCallback(
    (btn: RawToolbarButton) => {
      const el = textareaRef.current;
      if (!el) return;
      const selStart = el.selectionStart;
      const selEnd = el.selectionEnd;
      const hadSelection = selEnd > selStart;
      const { text, cursor } = btn.action(editedContent, selStart, selEnd);
      setEditedContent(text);
      requestAnimationFrame(() => {
        el.focus();
        if (hadSelection) {
          el.setSelectionRange(selStart, selStart + (cursor - selStart));
        } else {
          el.setSelectionRange(cursor, cursor);
        }
      });
    },
    [editedContent],
  );

  const Icon = CANVAS_ICON_MAP[artifact.type] ?? FileText;

  return (
    <SidePanel
      icon={Icon}
      title={artifact.name}
      closeLabel="Close canvas"
      maximized={maximized}
      onToggleMaximize={onToggleMaximize}
      onClose={onClose}
      headerExtra={
        isDocument ? (
          <div className="flex items-center rounded-full bg-muted p-1 gap-0.5 shrink-0">
            <button
              type="button"
              onClick={() => {
                const content = editedContentRef.current;
                // Make editor visible first so ProseMirror can lay out correctly,
                // then set content while it's visible
                flushSync(() => setMode("styled"));
                setEditorMarkdown(editor, content);
              }}
              className={cn(
                "text-xs px-3 py-1 rounded-full transition-all",
                mode === "styled"
                  ? "bg-background text-foreground font-medium shadow-sm"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              Styled
            </button>
            <button
              type="button"
              onClick={() => {
                // Sync Tiptap content into raw textarea before switching
                if (editor && !editor.isDestroyed) {
                  const md = getMdStorage(editor).getMarkdown();
                  editedContentRef.current = md;
                  setEditedContent(md);
                }
                setMode("raw");
              }}
              className={cn(
                "text-xs px-3 py-1 rounded-full transition-all",
                mode === "raw"
                  ? "bg-background text-foreground font-medium shadow-sm"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              Raw
            </button>
          </div>
        ) : undefined
      }
      toolbar={
        isDocument ? (
          <div className="flex items-center gap-0.5 border-b border-border px-3 py-1.5 flex-wrap">
            {mode === "styled"
              ? STYLED_TOOLBAR.map((btn) => (
                  <Button
                    key={btn.label}
                    variant="ghost"
                    size="icon"
                    className={cn(
                      "size-7 text-muted-foreground hover:text-foreground",
                      editor?.isActive(
                        btn.label.startsWith("Heading")
                          ? "heading"
                          : btn.label.toLowerCase().replace(" ", ""),
                        btn.label === "Heading 1"
                          ? { level: 1 }
                          : btn.label === "Heading 2"
                            ? { level: 2 }
                            : btn.label === "Heading 3"
                              ? { level: 3 }
                              : undefined,
                      ) && "bg-muted text-foreground",
                    )}
                    aria-label={btn.label}
                    title={btn.label}
                    onMouseDown={(e) => {
                      e.preventDefault();
                      btn.action(editor);
                    }}
                  >
                    <btn.icon className="size-3.5" />
                  </Button>
                ))
              : RAW_TOOLBAR.map((btn) => (
                  <Button
                    key={btn.label}
                    variant="ghost"
                    size="icon"
                    className="size-7 text-muted-foreground hover:text-foreground"
                    aria-label={btn.label}
                    title={btn.label}
                    onMouseDown={(e) => {
                      e.preventDefault();
                      applyRawAction(btn);
                    }}
                  >
                    <btn.icon className="size-3.5" />
                  </Button>
                ))}
          </div>
        ) : undefined
      }
      windowControls={windowControls}
    >
      {/* Content — both editor and textarea always mounted; visibility toggled via CSS */}
      <div className="flex-1 overflow-hidden relative">
        {isDocument ? (
          <>
            {/* Styled (Tiptap) — always mounted so editor is never destroyed */}
            <div
              className={cn(
                "tiptap-editor absolute inset-0 overflow-y-auto",
                mode !== "styled" && "hidden",
              )}
            >
              <EditorContent editor={editor} className="h-full" />
            </div>
            {/* Raw (textarea) — always mounted so ref is always valid */}
            <Textarea
              ref={textareaRef}
              value={editedContent}
              onChange={(e) => {
                editedContentRef.current = e.target.value;
                setEditedContent(e.target.value);
              }}
              className={cn(
                "absolute inset-0 h-full resize-none rounded-none border-0 p-4 lg:p-6 font-mono text-sm focus-visible:ring-0 focus-visible:ring-offset-0",
                mode !== "raw" && "hidden",
              )}
              placeholder="Start writing markdown..."
              spellCheck={false}
            />
          </>
        ) : (
          <div className="h-full overflow-y-auto">
            <FilePreview
              name={artifact.name}
              content={editedContent}
              url={artifact.url}
            />
          </div>
        )}
      </div>
    </SidePanel>
  );
}
