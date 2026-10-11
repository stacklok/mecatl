// SPDX-License-Identifier: Apache-2.0

import { type ComponentProps, createContext, useContext } from "react";
import type { Components, ExtraProps } from "react-markdown";
import ReactMarkdown, { defaultUrlTransform } from "react-markdown";
import remarkGfm from "remark-gfm";
import { cn } from "@/lib/utils";
import { HighlightedCode, langForClassName } from "./code-highlight";

/** True inside a fenced or indented code block's `<pre>` panel. */
const InCodePanel = createContext(false);

/**
 * A `<code>` element: a code block on the dark panel (labelled or not, and
 * indented blocks too, which carry no language class), or an inline chip.
 */
function MarkdownCode({
  children: code,
  className,
  node: _node,
  ...props
}: ComponentProps<"code"> & ExtraProps) {
  const inPanel = useContext(InCodePanel);
  if (inPanel || className) {
    return (
      <HighlightedCode
        code={String(code).replace(/\n$/u, "")}
        lang={langForClassName(className)}
        tone="dark"
      />
    );
  }
  return (
    <code
      className="rounded bg-zinc-100 px-1.5 py-0.5 font-mono text-[0.87em] [overflow-wrap:anywhere] dark:bg-zinc-800"
      {...props}
    >
      {code}
    </code>
  );
}

const mdComponents: Components = {
  a: ({ children: label, node: _node, ...props }) => (
    <a
      className="break-all text-info underline underline-offset-2"
      rel="noreferrer"
      target="_blank"
      {...props}
    >
      {label}
    </a>
  ),
  img: ({ alt, src }) => {
    if (src?.startsWith("data:image/") || src?.startsWith("blob:")) {
      return <img alt={alt ?? ""} className="max-w-full" src={src} />;
    }
    if (src && /^https?:\/\//u.test(src)) {
      return (
        <a className="break-all text-info underline" href={src} rel="noreferrer" target="_blank">
          {alt || src} (external image)
        </a>
      );
    }
    return <span>{alt || "Image"}</span>;
  },
  blockquote: ({ children }) => (
    <blockquote className="my-3 border-l-2 border-muted-foreground/30 pl-4 text-muted-foreground italic">
      {children}
    </blockquote>
  ),
  code: MarkdownCode,
  h1: ({ children }) => <h1 className="mt-6 mb-2 text-xl font-bold">{children}</h1>,
  h2: ({ children }) => <h2 className="mt-6 mb-2 text-lg font-bold">{children}</h2>,
  h3: ({ children }) => <h3 className="mt-5 mb-1.5 text-base font-semibold">{children}</h3>,
  h4: ({ children }) => <h4 className="mt-4 mb-1 text-sm font-semibold">{children}</h4>,
  hr: () => <hr className="my-4 border-t border-border" />,
  // Lists keep react-markdown's attributes: an ordered list's `start`, and GFM's
  // task-list classes, whose items carry their own checkbox instead of a bullet.
  li: ({ className, node: _node, ...props }) => (
    <li
      className={cn("ml-1", className?.includes("task-list-item") && "list-none", className)}
      {...props}
    />
  ),
  ol: ({ className, node: _node, ...props }) => (
    <ol className={cn("my-2 ml-4 list-decimal space-y-0.5", className)} {...props} />
  ),
  p: ({ children }) => <p className="my-0.5">{children}</p>,
  // The prototype's code panel: dark in both themes, so its tokens take the
  // dark theme's colours (HighlightedCode's "dark" tone).
  pre: ({ children: code, node: _node, ...props }) => (
    <pre
      className="my-3 max-w-full overflow-x-auto rounded-lg border bg-zinc-950 p-4 font-mono text-[13px] leading-relaxed text-zinc-100 dark:bg-zinc-900"
      {...props}
    >
      <InCodePanel.Provider value>{code}</InCodePanel.Provider>
    </pre>
  ),
  table: ({ children }) => (
    <div className="my-4 max-w-full overflow-x-auto rounded-lg border bg-card">
      <table className="w-full text-sm">{children}</table>
    </div>
  ),
  tbody: ({ children }) => <tbody>{children}</tbody>,
  td: ({ children }) => <td className="px-4 py-2.5">{children}</td>,
  th: ({ children }) => (
    <th className="whitespace-nowrap bg-muted px-4 py-2.5 text-left font-semibold text-foreground">
      {children}
    </th>
  ),
  thead: ({ children }) => <thead>{children}</thead>,
  tr: ({ children }) => <tr className="border-b last:border-0 hover:bg-muted/50">{children}</tr>,
  ul: ({ className, node: _node, ...props }) => (
    <ul
      className={cn(
        "my-2 ml-4 space-y-0.5",
        className?.includes("contains-task-list") ? "list-none" : "list-disc",
        className,
      )}
      {...props}
    />
  ),
};

export function MarkdownMessage({ children }: { children: string }) {
  return (
    <div className="min-w-0 max-w-full break-words">
      <ReactMarkdown
        components={mdComponents}
        remarkPlugins={[remarkGfm]}
        urlTransform={(url, key, node) =>
          key === "src" &&
          node.tagName === "img" &&
          (url.startsWith("data:image/") || url.startsWith("blob:"))
            ? url
            : defaultUrlTransform(url)
        }
      >
        {children}
      </ReactMarkdown>
    </div>
  );
}
