import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { remarkStrong } from "../lib/strong";

function safeHref(href?: string) {
  return href && /^https?:\/\//i.test(href) ? href : undefined;
}

const components: Components = {
  p: ({ children }) => <p className="my-2 leading-relaxed first:mt-0 last:mb-0">{children}</p>,
  h1: ({ children }) => (
    <h3 className="mt-4 mb-1.5 text-[15px] font-semibold text-foreground first:mt-0">{children}</h3>
  ),
  h2: ({ children }) => (
    <h4 className="mt-4 mb-1.5 text-[14.5px] font-semibold text-foreground first:mt-0">{children}</h4>
  ),
  h3: ({ children }) => (
    <h5 className="mt-3 mb-1 text-[14px] font-semibold text-foreground first:mt-0">{children}</h5>
  ),
  ul: ({ children }) => (
    <ul className="my-2 flex list-disc flex-col gap-1 pl-5 marker:text-muted-foreground first:mt-0 last:mb-0">
      {children}
    </ul>
  ),
  ol: ({ children }) => (
    <ol className="my-2 flex list-decimal flex-col gap-1 pl-5 marker:text-muted-foreground first:mt-0 last:mb-0">
      {children}
    </ol>
  ),
  strong: ({ children }) => <strong className="font-semibold text-foreground">{children}</strong>,
  a: ({ href, children }) => {
    const safe = safeHref(href);

    if (!safe) return <span>{children}</span>;

    return (
      <a
        href={safe}
        target="_blank"
        rel="noreferrer"
        className="font-medium text-foreground underline decoration-muted-foreground/50 underline-offset-2 hover:decoration-foreground"
      >
        {children}
      </a>
    );
  },
  blockquote: ({ children }) => (
    <blockquote className="my-2 border-l-2 border-border pl-3 text-muted-foreground first:mt-0 last:mb-0">
      {children}
    </blockquote>
  ),
  pre: ({ children }) => (
    <pre className="my-2 overflow-x-auto rounded-control border border-border bg-muted/50 p-3 font-mono text-[12.5px] leading-relaxed first:mt-0 last:mb-0">
      {children}
    </pre>
  ),
  code: ({ className, children }) => {
    if (className) return <code className="font-mono text-[12.5px]">{children}</code>;

    return (
      <code className="rounded-chip bg-muted px-1 py-0.5 font-mono text-[12.5px] text-foreground">
        {children}
      </code>
    );
  },
  table: ({ children }) => (
    <div className="my-2 overflow-x-auto rounded-control border border-border">
      <table className="w-full text-left text-[13px]">{children}</table>
    </div>
  ),
  th: ({ children }) => (
    <th className="border-b border-border bg-muted/50 px-2.5 py-1.5 font-medium">{children}</th>
  ),
  td: ({ children }) => <td className="border-b border-border px-2.5 py-1.5 align-top">{children}</td>,
};

export function MarkdownBody({ content }: { content: string }) {
  return (
    <div className="text-[13.5px] text-foreground [overflow-wrap:anywhere]">
      <ReactMarkdown remarkPlugins={[remarkGfm, remarkStrong]} components={components}>
        {content}
      </ReactMarkdown>
    </div>
  );
}
