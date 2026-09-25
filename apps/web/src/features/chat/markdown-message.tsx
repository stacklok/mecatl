// SPDX-License-Identifier: Apache-2.0

import type { Components } from "react-markdown";
import ReactMarkdown, { defaultUrlTransform } from "react-markdown";
import remarkGfm from "remark-gfm";
import { HighlightedCode, langForClassName } from "./code-highlight";

const mdComponents: Components = {
  a: ({ children: label, ...props }) => (
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
  code: ({ children: code, className, ...props }) =>
    className ? (
      <HighlightedCode code={String(code).replace(/\n$/u, "")} lang={langForClassName(className)} />
    ) : (
      <code className="break-all rounded bg-muted px-1 py-0.5 font-mono text-[0.82em]" {...props}>
        {code}
      </code>
    ),
  h1: ({ children }) => <h1 className="mt-6 mb-2 text-xl font-bold">{children}</h1>,
  h2: ({ children }) => <h2 className="mt-6 mb-2 text-lg font-bold">{children}</h2>,
  h3: ({ children }) => <h3 className="mt-5 mb-1.5 text-base font-semibold">{children}</h3>,
  h4: ({ children }) => <h4 className="mt-4 mb-1 text-sm font-semibold">{children}</h4>,
  hr: () => <hr className="my-4 border-t border-border" />,
  pre: ({ children: code, ...props }) => (
    <pre
      className="my-3 max-w-full overflow-x-auto rounded-lg border bg-muted/40 p-4 font-mono text-xs leading-6"
      {...props}
    >
      {code}
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
