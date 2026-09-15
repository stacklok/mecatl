import type { Components } from "react-markdown";

export const mdComponents: Components = {
  h1: ({ children }) => (
    <h1 className="text-xl font-bold mt-6 mb-2">{children}</h1>
  ),
  h2: ({ children }) => (
    <h2 className="text-lg font-bold mt-6 mb-2">{children}</h2>
  ),
  h3: ({ children }) => (
    <h3 className="text-base font-semibold mt-5 mb-1.5">{children}</h3>
  ),
  h4: ({ children }) => (
    <h4 className="text-sm font-semibold mt-4 mb-1">{children}</h4>
  ),
  p: ({ children }) => <p className="my-0.5">{children}</p>,
  ul: ({ children }) => (
    <ul className="my-2 ml-4 list-disc space-y-0.5 text-sm leading-[1.75] lg:text-[15px]">
      {children}
    </ul>
  ),
  ol: ({ children }) => (
    <ol className="my-2 ml-4 list-decimal space-y-0.5 text-sm leading-[1.75] lg:text-[15px]">
      {children}
    </ol>
  ),
  li: ({ children }) => <li className="ml-1">{children}</li>,
  blockquote: ({ children }) => (
    <blockquote className="my-3 border-l-3 border-muted-foreground/30 pl-4 text-muted-foreground italic">
      {children}
    </blockquote>
  ),
  hr: () => <hr className="my-4 border-border" />,
  a: ({ href, children }) => (
    <a
      href={href}
      className="text-link underline underline-offset-2 hover:text-link/80"
      target="_blank"
      rel="noopener noreferrer"
    >
      {children}
    </a>
  ),
  code: ({ className, children }) => {
    const isBlock = className?.includes("language-");
    if (isBlock) {
      return <code>{children}</code>;
    }
    return (
      <code className="bg-zinc-100 dark:bg-zinc-800 px-1.5 py-0.5 rounded text-[13px] font-mono">
        {children}
      </code>
    );
  },
  pre: ({ children }) => (
    <pre className="my-3 overflow-x-auto rounded-lg border bg-zinc-950 dark:bg-zinc-900 p-4 text-sm leading-relaxed font-mono text-zinc-100">
      {children}
    </pre>
  ),
  strong: ({ children }) => <strong>{children}</strong>,
  em: ({ children }) => <em>{children}</em>,
  table: ({ children }) => (
    <div className="my-4 overflow-x-auto rounded-lg border bg-white dark:bg-zinc-900">
      <table className="w-full text-sm">{children}</table>
    </div>
  ),
  thead: ({ children }) => <thead>{children}</thead>,
  tbody: ({ children }) => <tbody>{children}</tbody>,
  tr: ({ children }) => (
    <tr className="border-b last:border-0 hover:bg-zinc-50 dark:hover:bg-zinc-800/50">
      {children}
    </tr>
  ),
  th: ({ children }) => (
    <th className="px-4 py-2.5 text-left font-semibold text-foreground whitespace-nowrap bg-zinc-50 dark:bg-zinc-800">
      {children}
    </th>
  ),
  td: ({ children }) => <td className="px-4 py-2.5">{children}</td>,
};
