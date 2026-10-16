import { useEffect, useState, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { IconArrowCornerDownRight } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconArrowCornerDownRight";
import { IconArrowUpRight } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconArrowUpRight";
import { IconClock } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconClock";
import { IconFileText } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconFileText";
import type { Attachment, Item, Retry, ToolItem } from "../../api/generated/types.gen";
import { formatSize, isImage } from "../../lib/attachments";
import { blocks, describeTool, seconds, stepTitle, type ContentBlock } from "../../lib/tools";
import { retryMessage } from "../../lib/chat-state";
import { Chip } from "../atoms/Chip";
import { StreamText } from "../atoms/StreamText";
import { Button } from "../atoms/Button";
import CodeBlock from "../atoms/CodeBlock";
import { Spinner } from "../feedback";
import { cn } from "../../lib/utils";

const markdown: Components = {
  p: ({ children }) => <p className="my-2 first:mt-0 last:mb-0">{children}</p>,
  h1: ({ children }) => (
    <h3 className="mt-4 mb-2 text-[15px] font-semibold text-ink first:mt-0">{children}</h3>
  ),
  h2: ({ children }) => (
    <h3 className="mt-4 mb-2 text-[14px] font-semibold text-ink first:mt-0">{children}</h3>
  ),
  h3: ({ children }) => (
    <h4 className="mt-3 mb-1.5 text-[13.5px] font-semibold text-ink first:mt-0">{children}</h4>
  ),
  ul: ({ children }) => (
    <ul className="my-2 flex list-disc flex-col gap-1 pl-5 marker:text-ink-3">{children}</ul>
  ),
  ol: ({ children }) => (
    <ol className="my-2 flex list-decimal flex-col gap-1 pl-5 marker:text-ink-3">{children}</ol>
  ),
  strong: ({ children }) => <strong className="font-semibold text-ink">{children}</strong>,
  a: ({ href, children }) => (
    <a href={href} className="font-medium text-accent-ink underline">
      {children}
    </a>
  ),
  blockquote: ({ children }) => (
    <blockquote className="my-2 border-l-2 border-line-strong pl-3 text-ink-2">{children}</blockquote>
  ),
  table: ({ children }) => (
    <div className="my-2 overflow-x-auto rounded-card shadow-hairline">
      <table className="w-full text-left text-[12.5px]">{children}</table>
    </div>
  ),
  th: ({ children }) => <th className="border-b border-line bg-inset px-3 py-1.5 font-medium">{children}</th>,
  td: ({ children }) => <td className="border-b border-line px-3 py-1.5 align-top">{children}</td>,
  pre: ({ children }) => <div className="my-2">{children}</div>,
  // Fenced code gets a code block. Inline code is a chip.
  code: ({ className, children }) => {
    const text = String(children).replace(/\n$/, "");

    return className || text.includes("\n") ? (
      <CodeBlock
        filename={className?.replace("language-", "") || "Code"}
        lines={text.split("\n")}
        code={text}
      />
    ) : (
      <Chip>{children}</Chip>
    );
  },
};

function Chevron({ open }: { open: boolean }) {
  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      className={cn("shrink-0 transition-transform duration-300", open && "rotate-180")}
    >
      <path d="M6 9l6 6 6-6" />
    </svg>
  );
}

function Sparkle({ working }: { working: boolean }) {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill={working ? "var(--ink-2)" : "var(--ink-3)"}
      aria-hidden
      className="shrink-0"
    >
      <path d="M12 2l2.4 7.2L22 12l-7.6 2.8L12 22l-2.4-7.2L2 12l7.6-2.8z" />
    </svg>
  );
}

function Shimmer({ children }: { children: ReactNode }) {
  return (
    <span className="animate-[shimmer-text_1.4s_linear_infinite] truncate bg-[linear-gradient(90deg,var(--ink-3)_35%,var(--ink)_50%,var(--ink-3)_65%)] bg-size-[200%_100%] bg-clip-text text-[13px] font-medium text-transparent">
      {children}
    </span>
  );
}

/** The header of a step row. Look from upstream primitives/ThinkingState.tsx. */
function StepHeader({
  working,
  active,
  done,
  failed = false,
  open,
  onToggle,
}: {
  working: boolean;
  active: string;
  done: string;
  failed?: boolean;
  open: boolean;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      aria-expanded={open}
      onClick={onToggle}
      className="-mx-1.5 flex w-fit max-w-full items-center gap-2 rounded-control px-1.5 py-1 transition-colors duration-100 hover:bg-hover-2"
    >
      <Sparkle working={working} />
      {working ? (
        <Shimmer>{active}</Shimmer>
      ) : (
        <span className={cn("truncate text-[13px] font-medium", failed ? "text-red" : "text-ink-2")}>
          {done}
        </span>
      )}
      <span className="text-ink-3">
        <Chevron open={open} />
      </span>
    </button>
  );
}

function Trace({ children }: { children: ReactNode }) {
  return (
    <div className="relative mt-1 ml-[5px] animate-[fade-in_200ms_ease-out_both] pl-4">
      <span aria-hidden className="absolute top-0 bottom-1 left-[3px] w-px bg-line" />
      <div className="flex flex-col gap-1 py-1">{children}</div>
    </div>
  );
}

function Output({ tool }: { tool: ToolItem }) {
  // The command or the path is the target of the row. The other arguments show here.
  const extra = Object.entries(tool.args).filter(([key]) => key !== "command" && key !== "path");

  return (
    <div className="mb-1 ml-6 flex animate-[fade-up_240ms_var(--ease-out-strong)_both] flex-col gap-2">
      {extra.length ? (
        <pre className="max-h-48 overflow-auto rounded-card bg-inset px-3 py-2 font-mono text-[11.5px] leading-relaxed whitespace-pre-wrap text-ink-2 shadow-hairline [overflow-wrap:anywhere]">
          {extra.map(([key, value]) => `${key}: ${JSON.stringify(value)}`).join("\n")}
        </pre>
      ) : null}
      <pre
        className={cn(
          "max-h-60 overflow-auto rounded-card px-3 py-2 font-mono text-[11.5px] leading-relaxed whitespace-pre-wrap shadow-hairline [overflow-wrap:anywhere]",
          tool.status === "error" ? "bg-red-tint text-ink" : "bg-inset text-ink-2",
        )}
      >
        {tool.output || (tool.status === "running" ? "Waiting for tool output…" : "No output.")}
      </pre>
    </div>
  );
}

function ToolStatus({ status }: { status: ToolItem["status"] }) {
  if (status === "running")
    return (
      <span role="img" aria-label="Running" className="flex size-3.5 shrink-0 items-center justify-center">
        <Spinner />
      </span>
    );

  if (status === "error")
    return <span role="img" aria-label="Failed" className="mx-[3px] size-2 shrink-0 rounded-full bg-red" />;

  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 24 24"
      fill="none"
      stroke="var(--ink-3)"
      strokeWidth="2.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      role="img"
      aria-label="Done"
      className="shrink-0"
    >
      <path d="M20 6L9 17l-5-5" />
    </svg>
  );
}

/** Consecutive tool calls as one step row with a line for each call. */
export function ToolStep({ tools, memory }: { tools: ToolItem[]; memory?: string }) {
  const working = tools.some((t) => t.status === "running");
  const failed = tools.some((t) => t.status === "error");
  const [open, setOpen] = useState<boolean | null>(null);
  // Until the user picks a call, the first failed call shows its output. A call can fail after
  // the step first shows, so this is not an initial state.
  const [picked, setSelected] = useState<string | null>();
  const selected = picked === undefined ? (tools.find((t) => t.status === "error")?.id ?? null) : picked;
  const expanded = open ?? (working || failed);
  const title = stepTitle(tools, memory);

  if (tools.length === 1 && tools[0].name === "spawn_task" && !failed) {
    const dispatch = describeTool(tools[0]);

    return (
      <div className="flex min-w-0 items-start gap-2 py-1 text-[13px] leading-5">
        <IconArrowUpRight size={16} aria-hidden className="mt-0.5 shrink-0 text-ink-3" />
        <p className="min-w-0 [overflow-wrap:anywhere]">
          {working ? (
            <Shimmer>{dispatch.verb}</Shimmer>
          ) : (
            <span className="font-medium text-ink-2">{dispatch.verb}</span>
          )}
          {tools[0].taskId ? (
            <Link
              to="/tasks/$taskId"
              params={{ taskId: tools[0].taskId }}
              className="ml-1.5 font-medium text-ink decoration-line-strong underline-offset-3 hover:underline"
            >
              {dispatch.target}
            </Link>
          ) : (
            <span className="ml-1.5 font-medium text-ink">{dispatch.target}</span>
          )}
        </p>
      </div>
    );
  }

  return (
    <div className="flex w-full flex-col">
      <StepHeader
        working={working}
        active={title.active}
        done={title.done}
        failed={failed}
        open={expanded}
        onToggle={() => setOpen(!expanded)}
      />
      {expanded ? (
        <Trace>
          {tools.map((tool) => {
            const { verb, target } = describeTool(tool, memory);
            const shown = selected === tool.id;

            return (
              <div key={tool.id} data-tool={tool.id}>
                <button
                  type="button"
                  aria-expanded={shown}
                  onClick={() => setSelected(shown ? null : tool.id)}
                  className={cn(
                    "flex min-h-7 w-full min-w-0 items-center gap-2 rounded-[6px] px-1.5 py-0.5 text-left transition-colors duration-150",
                    shown ? "bg-inset" : "hover:bg-hover",
                  )}
                >
                  <ToolStatus status={tool.status} />
                  <span className="shrink-0 text-[12.5px] font-medium text-ink">{verb}</span>
                  <span
                    className={cn(
                      "min-w-0 flex-1 font-mono text-[11.5px] text-ink-3",
                      shown ? "whitespace-pre-wrap [overflow-wrap:anywhere]" : "truncate",
                    )}
                    title={target}
                  >
                    {target}
                  </span>
                  {tool.durationMs !== undefined ? (
                    <span className="shrink-0 font-mono text-[11px] text-ink-3 tabular-nums">
                      {seconds(tool.durationMs)}
                    </span>
                  ) : null}
                </button>
                {shown ? <Output tool={tool} /> : null}
              </div>
            );
          })}
        </Trace>
      ) : null}
    </div>
  );
}

function ThinkingStep({ text, live }: { text: string; live: boolean }) {
  const [open, setOpen] = useState(false);

  return (
    <div className="flex flex-col">
      <StepHeader
        working={live}
        active="Thinking"
        done="Thought process"
        open={open}
        onToggle={() => setOpen(!open)}
      />
      {open ? (
        <Trace>
          <p className="px-1.5 text-[12.5px] leading-relaxed whitespace-pre-wrap text-ink-2">
            {text || "The provider did not share its thought process."}
          </p>
        </Trace>
      ) : null}
    </div>
  );
}

function CopyText({ text }: { text: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");

  return (
    <button
      type="button"
      aria-label="Copy answer"
      onClick={() => {
        void navigator.clipboard.writeText(text).then(
          () => setState("copied"),
          () => setState("failed"),
        );
      }}
      className="rounded-[6px] px-1.5 py-0.5 font-medium text-ink-3 transition-colors hover:bg-hover hover:text-ink"
    >
      {state === "copied" ? "Copied" : state === "failed" ? "Copy failed" : "Copy"}
    </button>
  );
}

// GET /api/uploads/{id} serves an image inline and any other file as a download.
function Files({ files }: { files: Attachment[] }) {
  return (
    <ul aria-label="Attached files" className="flex max-w-full flex-wrap justify-end gap-1.5">
      {files.map((f) =>
        isImage(f.mimeType) ? (
          <li key={f.id}>
            <a
              href={`/api/uploads/${f.id}`}
              target="_blank"
              rel="noreferrer"
              title={f.name}
              className="block"
            >
              <img
                src={`/api/uploads/${f.id}`}
                alt={f.name}
                loading="lazy"
                className="h-28 max-w-[min(240px,100%)] rounded-xl bg-field object-cover shadow-hairline"
              />
            </a>
          </li>
        ) : (
          <li key={f.id} className="min-w-0">
            <a
              href={`/api/uploads/${f.id}`}
              download={f.name}
              className="flex h-10 min-w-0 items-center gap-2 rounded-[10px] bg-field py-1 pr-2.5 pl-1 text-[11.5px] shadow-hairline transition-colors hover:bg-hover"
            >
              <span className="flex size-8 shrink-0 items-center justify-center rounded-[6px] bg-inset text-ink-3">
                <IconFileText size={15} />
              </span>
              <span className="flex min-w-0 flex-col leading-tight">
                <span className="max-w-48 truncate font-medium text-ink" title={f.name}>
                  {f.name}
                </span>
                <span className="text-ink-3 tabular-nums">{formatSize(f.size)}</span>
              </span>
            </a>
          </li>
        ),
      )}
    </ul>
  );
}

export function TranscriptItem({
  item,
  live,
  retry,
  canRetry,
}: {
  item: Exclude<Item, { kind: "tool" }>;
  live: boolean;
  retry: () => void;
  canRetry: boolean;
}) {
  switch (item.kind) {
    case "boundary":
      if (item.boundary === "compaction") return null;

      return (
        <div role="separator" className="flex items-center gap-3 py-3 text-[12px] text-ink-3">
          <span className="h-px flex-1 bg-line" />
          <span>Context cleared</span>
          <span className="h-px flex-1 bg-line" />
        </div>
      );
    case "user":
      return (
        <div className="flex animate-[fade-up_300ms_var(--ease-out-strong)_both] flex-col items-end gap-1 pl-10 sm:pl-24">
          {item.attachments?.length ? (
            <div className={cn("max-w-full", item.queued && "opacity-70")}>
              <Files files={item.attachments} />
            </div>
          ) : null}
          {item.text ? (
            <div
              className={cn(
                "rounded-xl bg-field px-3.5 py-2 text-[13px] leading-relaxed whitespace-pre-wrap text-ink shadow-hairline [overflow-wrap:anywhere]",
                item.queued && "opacity-70",
              )}
            >
              {item.text}
            </div>
          ) : null}
          {item.queued ? (
            <span className="flex items-center gap-1 text-[11.5px] text-ink-3">
              <IconClock size={12} /> Queued · runs after the current reply
            </span>
          ) : null}
        </div>
      );
    case "task":
      return (
        <header className="flex min-w-0 items-start gap-2 text-[13px] leading-5">
          <IconArrowCornerDownRight size={16} aria-hidden className="mt-0.5 shrink-0 text-ink-3" />
          <h3 className="min-w-0 [overflow-wrap:anywhere]">
            <span className="mr-1.5 font-medium text-ink-2">Background result</span>
            <Link
              to="/tasks/$taskId"
              params={{ taskId: item.taskId }}
              className="font-semibold text-ink decoration-line-strong underline-offset-3 hover:underline"
            >
              {item.title}
            </Link>
          </h3>
        </header>
      );
    case "thinking":
      return <ThinkingStep text={item.text} live={live} />;
    case "assistant":
      if (!live && !item.text.trim()) return null;

      return (
        <article className="group min-w-0">
          {live ? (
            <div className="max-w-[640px] text-[13.5px] leading-[1.65] whitespace-pre-wrap text-ink [overflow-wrap:anywhere]">
              <StreamText text={item.text} charsPerTick={Math.max(2, item.text.length)} tickMs={10} />
            </div>
          ) : (
            <Markdown text={item.text} />
          )}
          <footer className="mt-1.5 flex h-7 items-center gap-3 text-[11.5px] text-ink-3 opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100 pointer-coarse:opacity-100">
            {item.model ? <span className="min-w-0 truncate font-mono">{item.model}</span> : null}
            {item.usage ? (
              <span className="shrink-0 tabular-nums">
                {item.usage.input.toLocaleString()} in · {item.usage.output.toLocaleString()} out
              </span>
            ) : null}
            <CopyText text={item.text} />
          </footer>
        </article>
      );
    case "error":
      return (
        <div
          role={item.aborted ? "status" : "alert"}
          className={cn(
            "flex max-w-[640px] flex-col gap-2 rounded-card px-3.5 py-3 text-[13px]",
            item.aborted ? "bg-inset shadow-hairline" : "bg-red-tint",
          )}
        >
          <div className="flex items-center gap-2">
            <span
              aria-hidden
              className={cn("size-1.5 shrink-0 rounded-full", item.aborted ? "bg-ink-3" : "bg-red")}
            />
            <span className={cn("font-medium", item.aborted ? "text-ink" : "text-red")}>
              {item.aborted ? "Run stopped" : "The agent could not finish"}
            </span>
          </div>
          <p className="pl-3.5 text-[12.5px] text-ink-2 [overflow-wrap:anywhere]">{item.reason}</p>
          {canRetry ? (
            <div className="pl-3.5">
              <Button size="xs" variant="secondary" onClick={retry}>
                Retry
              </Button>
            </div>
          ) : null}
        </div>
      );
  }
}

export function Transcript({
  items,
  running,
  retry,
  canRetry,
  memory,
}: {
  items: Item[];
  running: boolean;
  retry: (index: number) => void;
  canRetry: boolean;
  memory?: string;
}) {
  function content(block: ContentBlock) {
    return block.kind === "tools" ? (
      <ToolStep key={block.key} tools={block.tools} memory={memory} />
    ) : (
      <TranscriptItem
        key={block.item.id}
        item={block.item}
        live={running && block.index === items.length - 1}
        retry={() => retry(block.index)}
        canRetry={canRetry && retryMessage(items, block.index) !== undefined}
      />
    );
  }

  return blocks(items).map((block) =>
    block.kind === "task" ? (
      <section
        key={block.item.id}
        aria-label={`Background result: ${block.item.title}`}
        className="flex min-w-0 flex-col gap-2"
      >
        <TranscriptItem item={block.item} live={false} retry={() => {}} canRetry={false} />
        {block.blocks.length ? (
          <div className="ml-2 flex min-w-0 flex-col gap-4 border-l border-line-strong pl-[15px]">
            {block.blocks.map(content)}
          </div>
        ) : null}
      </section>
    ) : (
      content(block)
    ),
  );
}

export function RetryNotice({ retry }: { retry: Retry }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);

    return () => clearInterval(timer);
  }, []);

  return (
    <div
      role="status"
      className="flex max-w-[640px] flex-col gap-1 rounded-card bg-inset px-3.5 py-3 shadow-hairline"
    >
      <Shimmer>
        Provider error · retry {retry.attempt} in {Math.max(0, Math.ceil((retry.at - now) / 1000))} s
      </Shimmer>
      <p className="text-[12.5px] text-ink-2 [overflow-wrap:anywhere]">{retry.reason}</p>
    </div>
  );
}

export function Markdown({ text }: { text: string }) {
  return (
    <div className="max-w-[640px] text-[13.5px] leading-[1.65] text-ink [overflow-wrap:anywhere]">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={markdown}>
        {text}
      </ReactMarkdown>
    </div>
  );
}
