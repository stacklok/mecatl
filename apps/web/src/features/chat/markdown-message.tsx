// SPDX-License-Identifier: Apache-2.0

import type { Components } from "react-markdown";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { HighlightedCode, langForClassName } from "./code-highlight";

const mdComponents: Components = {
  a: ({ children: label, ...props }) => (
    <a
      className="text-info underline underline-offset-2"
      rel="noreferrer"
      target="_blank"
      {...props}
    >
      {label}
    </a>
  ),
  blockquote: ({ children }) => (
    <blockquote className="my-3 border-l-2 border-muted-foreground/30 pl-4 text-muted-foreground italic">
      {children}
    </blockquote>
  ),
  code: ({ children: code, className, ...props }) =>
    className ? (
      <HighlightedCode code={String(code).replace(/\n$/u, "")} lang={langForClassName(className)} />
    ) : (
      <code className="rounded bg-muted px-1 py-0.5 font-mono text-[0.82em]" {...props}>
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
      className="my-3 overflow-x-auto rounded-lg border bg-muted/40 p-4 font-mono text-xs leading-6"
      {...props}
    >
      {code}
    </pre>
  ),
  table: ({ children }) => (
    <div className="my-4 overflow-x-auto rounded-lg border bg-card">
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
    <ReactMarkdown components={mdComponents} remarkPlugins={[remarkGfm]}>
      {children}
    </ReactMarkdown>
  );
}
