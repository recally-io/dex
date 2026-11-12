import {
  Check,
  ChevronRight,
  Clock3,
  CodeXml,
  Copy,
  CornerDownLeft,
  CornerDownRight,
  FileText,
  GitBranch,
  SquarePen,
} from "lucide-react";
import { useAgentId } from "../../lib/agent-scope";
import { useEffect, useState, type ReactNode } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import type { Attachment, Item, Retry, ToolItem } from "../../api/generated/types.gen";
import { formatSize, isImage } from "../../lib/attachments";
import { blocks, describeTool, seconds, stepTitle, type ContentBlock, type Step } from "../../lib/tools";
import { remarkStrong, strongText } from "../../lib/strong";
import { retryMessage } from "../../lib/chat-state";
import { Button } from "../ui/button";
import { Spinner } from "../feedback";
import { ResultLine, ThreadEntry } from "./thread";
import { threadCall } from "../../lib/threads";
import { cn } from "../../lib/utils";

const markdown: Components = {
  p: ({ children }) => <p className="my-3 first:mt-0 last:mb-0">{children}</p>,
  h1: ({ children }) => (
    <h3 className="mt-6 mb-2 text-[17px] leading-snug font-semibold text-foreground first:mt-0">
      {children}
    </h3>
  ),
  h2: ({ children }) => (
    <h3 className="mt-6 mb-2 text-[16px] leading-snug font-semibold text-foreground first:mt-0">
      {children}
    </h3>
  ),
  h3: ({ children }) => (
    <h4 className="mt-5 mb-1.5 text-[15px] leading-snug font-semibold text-foreground first:mt-0">
      {children}
    </h4>
  ),
  ul: ({ children }) => (
    <ul className="my-3 flex list-disc flex-col gap-1.5 pl-5 marker:text-muted-foreground">{children}</ul>
  ),
  ol: ({ children }) => (
    <ol className="my-3 flex list-decimal flex-col gap-1.5 pl-5 marker:text-muted-foreground">{children}</ol>
  ),
  strong: ({ children }) => <strong className="font-semibold text-foreground">{children}</strong>,
  a: ({ href, children }) => (
    <a href={href} className="font-medium text-primary underline">
      {children}
    </a>
  ),
  blockquote: ({ children }) => (
    <blockquote className="my-3 border-l-2 border-ring pl-3 text-muted-foreground">{children}</blockquote>
  ),
  table: ({ children }) => (
    <div className="my-3 overflow-x-auto rounded-md shadow-card">
      <table className="w-full text-left text-[13.5px] leading-normal">{children}</table>
    </div>
  ),
  th: ({ children }) => (
    <th className="border-b border-border bg-muted px-3 py-1.5 font-medium">{children}</th>
  ),
  td: ({ children }) => <td className="border-b border-border px-3 py-1.5 align-top">{children}</td>,
  pre: ({ children }) => <div className="my-3">{children}</div>,
  // Fenced code gets a code block. Inline code is a chip.
  code: ({ className, children }) => {
    const text = String(children).replace(/\n$/, "");

    return className || text.includes("\n") ? (
      <CodeBlock language={className?.replace("language-", "")} text={text} />
    ) : (
      <code className="inline rounded-md bg-muted px-1.5 py-0.5 font-mono text-[13px]">{children}</code>
    );
  },
};

/** A fenced code block: the language, a Copy button, and a line-numbered listing. */
function CodeBlock({ language, text }: { language?: string; text: string }) {
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) return;

    const timer = setTimeout(() => setCopied(false), 1500);

    return () => clearTimeout(timer);
  }, [copied]);

  return (
    <figure className="my-2 max-w-full overflow-hidden rounded-md bg-muted shadow-card">
      <figcaption className="flex h-8 items-center justify-between border-b border-border pr-1 pl-3 text-[11.5px] text-muted-foreground">
        <span className="flex items-center gap-1.5">
          <CodeXml className="size-3.5" />
          {language || "Code"}
        </span>
        <Button
          type="button"
          variant="ghost"
          size="xs"
          onClick={() => void navigator.clipboard.writeText(text).then(() => setCopied(true))}
        >
          {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
          {copied ? "Copied" : "Copy"}
        </Button>
      </figcaption>
      <pre className="overflow-auto py-2 font-mono text-[12.5px] leading-relaxed text-foreground">
        <code className="grid grid-cols-[auto_1fr]">
          {text.split("\n").map((line, index) => (
            // Lines have no identity of their own; the listing never reorders them.
            // oxlint-disable-next-line react/no-array-index-key
            <span key={index} className="contents">
              <span aria-hidden className="pr-3 pl-3 text-right text-muted-foreground select-none">
                {index + 1}
              </span>
              <span className="pr-3 whitespace-pre-wrap [overflow-wrap:anywhere]">{line || " "}</span>
            </span>
          ))}
        </code>
      </pre>
    </figure>
  );
}

const today = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit" });

const earlier = new Intl.DateTimeFormat("en-GB", {
  weekday: "short",
  day: "numeric",
  month: "short",
  hour: "2-digit",
  minute: "2-digit",
});

// The UI text is English, so the time format is too: "08:30" today, else "Thu 15 Oct, 08:30".
function stamp(at: number) {
  return new Date(at).toDateString() === new Date().toDateString() ? today.format(at) : earlier.format(at);
}

function entryName(origin?: string) {
  switch (origin?.split(":", 1)[0]) {
    case "telegram":
      return "Telegram";
    case "wechat":
      return "WeChat";
    case "feishu":
      return "Feishu";
    default:
      return undefined;
  }
}

function Shimmer({ children }: { children: ReactNode }) {
  return <span className="truncate text-[13px] font-medium text-muted-foreground">{children}</span>;
}

function Trace({ children }: { children: ReactNode }) {
  return (
    <div className="relative mt-1 ml-[5px] animate-[fade-in_200ms_ease-out_both] pl-4">
      <span aria-hidden className="absolute top-0 bottom-1 left-[3px] w-px bg-border" />
      <div className="flex flex-col gap-1 py-1">{children}</div>
    </div>
  );
}

// Tool output is plain text, failed or not. The row's red dot marks a failed call.
const outputBox =
  "overflow-auto rounded-md bg-muted px-3 py-2 font-mono text-[12px] leading-relaxed whitespace-pre-wrap [overflow-wrap:anywhere]";

function Output({ tool }: { tool: ToolItem }) {
  // The command or the path is the target of the row. The other arguments show here.
  const extra = Object.entries(tool.args).filter(([key]) => key !== "command" && key !== "path");

  return (
    <div className="mb-1 ml-6 flex animate-[fade-up_240ms_var(--ease-out-strong)_both] flex-col gap-2">
      {extra.length ? (
        <pre className={cn(outputBox, "max-h-48 text-muted-foreground")}>
          {extra.map(([key, value]) => `${key}: ${JSON.stringify(value)}`).join("\n")}
        </pre>
      ) : null}
      <pre className={cn(outputBox, "max-h-60 text-foreground")}>
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
    <Check
      role="img"
      aria-label="Done"
      strokeWidth={2.5}
      className="size-3.5 shrink-0 text-muted-foreground"
    />
  );
}

/** A line in the trace of a steps row. A click shows the output or the thought below it. */
function StepLine({
  step,
  memory,
  shown,
  onToggle,
}: {
  step: Step;
  memory?: string;
  shown: boolean;
  onToggle: () => void;
}) {
  const thought = step.kind === "thinking";

  const { verb, target } = thought
    ? { verb: "Thought", target: step.text.split("\n", 1)[0] || "Not shared by the provider" }
    : describeTool(step, memory);

  return (
    <div data-tool={thought ? undefined : step.id}>
      <button
        type="button"
        aria-expanded={shown}
        onClick={onToggle}
        className={cn(
          "flex min-h-7 w-full min-w-0 items-center gap-2 rounded-control px-1.5 py-0.5 text-left transition-colors duration-150",
          shown ? "bg-muted" : "hover:bg-accent",
        )}
      >
        {thought ? (
          <span aria-hidden className="mx-[4px] size-1.5 shrink-0 rounded-full bg-ring" />
        ) : (
          <ToolStatus status={step.status} />
        )}
        <span className="shrink-0 text-[12.5px] font-medium text-foreground">{verb}</span>
        <span
          className={cn(
            "min-w-0 flex-1 text-[12px] text-muted-foreground",
            !thought && "font-mono",
            shown && !thought ? "whitespace-pre-wrap [overflow-wrap:anywhere]" : "truncate",
          )}
          title={target}
        >
          {target}
        </span>
        {!thought && step.durationMs !== undefined ? (
          <span className="shrink-0 font-mono text-[11px] text-muted-foreground tabular-nums">
            {seconds(step.durationMs)}
          </span>
        ) : null}
      </button>
      {shown && thought ? (
        <p className="mb-1 ml-6 animate-[fade-up_240ms_var(--ease-out-strong)_both] text-[13px] leading-relaxed whitespace-pre-wrap text-muted-foreground [overflow-wrap:anywhere]">
          {step.text || "The provider did not share its thought process."}
        </p>
      ) : null}
      {shown && !thought ? <Output tool={step} /> : null}
    </div>
  );
}

/**
 * The thoughts and tool calls between two answers. Each session call shows as a thread card or
 * line in its place in the order. The steps between cards fold into collapsed rows.
 */
export function Steps({
  steps,
  memory,
  thinking = false,
}: {
  steps: Step[];
  memory?: string;
  thinking?: boolean;
}) {
  const parts: ({ card: ToolItem } | { rest: Step[] })[] = [];

  for (const step of steps) {
    const last = parts.at(-1);

    if (step.kind === "tool" && threadCall(step)) parts.push({ card: step });
    else if (last && "rest" in last) last.rest.push(step);
    else parts.push({ rest: [step] });
  }

  return (
    <div className="flex w-full flex-col">
      {parts.map((part, i) =>
        "card" in part ? (
          <ThreadEntry key={part.card.id} tool={part.card} />
        ) : (
          <StepsRow
            key={part.rest[0].id}
            steps={part.rest}
            memory={memory}
            thinking={thinking && i === parts.length - 1}
          />
        ),
      )}
    </div>
  );
}

/**
 * Consecutive thoughts and tool calls, as one collapsed row. The row opens by itself
 * only when a call failed. thinking is true while the last step is a thought that streams.
 */
function StepsRow({
  steps,
  memory,
  thinking = false,
}: {
  steps: Step[];
  memory?: string;
  thinking?: boolean;
}) {
  const failedId = steps.find((step) => step.kind === "tool" && step.status === "error")?.id;
  const [open, setOpen] = useState<boolean | null>(null);
  // Until the user picks a line, the first failed call shows its output. A call can fail after
  // the row first shows, so this is not an initial state.
  const [picked, setPicked] = useState<string | null>();
  const selected = picked === undefined ? (failedId ?? null) : picked;
  const expanded = open ?? !!failedId;
  const title = stepTitle(steps, memory, thinking);
  // Tools run without a sandbox, so a closed row still shows what runs now.
  const running = steps.find((step) => step.kind === "tool" && step.status === "running");

  return (
    <div className="flex w-full flex-col">
      <button
        type="button"
        aria-expanded={expanded}
        onClick={() => setOpen(!expanded)}
        className="-mx-1.5 flex w-fit max-w-full items-center gap-1.5 rounded-md px-1.5 py-1 text-[13px] text-muted-foreground transition-colors duration-100 hover:bg-accent hover:text-foreground"
      >
        {title.active ? (
          <Spinner />
        ) : failedId ? (
          <span aria-hidden className="size-1.5 shrink-0 rounded-full bg-red" />
        ) : null}
        <span className={cn("max-w-full shrink-0 truncate", !title.active && failedId && "text-red")}>
          {title.active ?? title.done}
        </span>
        {running?.kind === "tool" ? (
          <span className="min-w-0 truncate font-mono text-[12px]">
            {describeTool(running, memory).target}
          </span>
        ) : null}
        <ChevronRight
          aria-hidden
          className={cn("size-3.5 shrink-0 transition-transform duration-200", expanded && "rotate-90")}
        />
      </button>
      {expanded ? (
        <Trace>
          {steps.map((step) => (
            <StepLine
              key={step.id}
              step={step}
              memory={memory}
              shown={selected === step.id}
              onToggle={() => setPicked(selected === step.id ? null : step.id)}
            />
          ))}
        </Trace>
      ) : null}
    </div>
  );
}

function MessageAction({
  label,
  disabled,
  onClick,
  children,
}: {
  label: string;
  disabled?: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={onClick}
      className="grid size-7 shrink-0 place-items-center rounded-control text-muted-foreground transition-colors hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-50"
    >
      {children}
    </button>
  );
}

function CopyText({ text, label }: { text: string; label: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");

  return (
    <MessageAction
      label={state === "copied" ? "Copied" : state === "failed" ? "Copy failed" : label}
      onClick={() => {
        void navigator.clipboard.writeText(text).then(
          () => setState("copied"),
          () => setState("failed"),
        );
      }}
    >
      {state === "copied" ? <Check size={14} aria-hidden /> : <Copy size={14} aria-hidden />}
    </MessageAction>
  );
}

// GET /api/agents/{agentId}/uploads/{id} serves an image inline and any other file as a download.
function Files({ files }: { files: Attachment[] }) {
  const agentId = useAgentId();

  return (
    <ul aria-label="Attached files" className="flex max-w-full flex-wrap justify-end gap-1.5">
      {files.map((f) =>
        isImage(f.mimeType) ? (
          <li key={f.id}>
            <a
              href={`/api/agents/${encodeURIComponent(agentId)}/uploads/${f.id}`}
              target="_blank"
              rel="noreferrer"
              title={f.name}
              className="block"
            >
              <img
                src={`/api/agents/${encodeURIComponent(agentId)}/uploads/${f.id}`}
                alt={f.name}
                loading="lazy"
                className="h-28 max-w-[min(240px,100%)] rounded-xl bg-card object-cover shadow-card"
              />
            </a>
          </li>
        ) : (
          <li key={f.id} className="min-w-0">
            <a
              href={`/api/agents/${encodeURIComponent(agentId)}/uploads/${f.id}`}
              download={f.name}
              className="flex h-10 min-w-0 items-center gap-2 rounded-card bg-card py-1 pr-2.5 pl-1 text-[11.5px] shadow-card transition-colors hover:bg-accent"
            >
              <span className="flex size-8 shrink-0 items-center justify-center rounded-control bg-muted text-muted-foreground">
                <FileText size={15} />
              </span>
              <span className="flex min-w-0 flex-col leading-tight">
                <span className="max-w-48 truncate font-medium text-foreground" title={f.name}>
                  {f.name}
                </span>
                <span className="text-muted-foreground tabular-nums">{formatSize(f.size)}</span>
              </span>
            </a>
          </li>
        ),
      )}
    </ul>
  );
}

const answerPrefix = "Answer to: ";

/**
 * The server wraps an ask_user answer as "Answer to: <question>\n<answer>". The question is one line,
 * so the first line splits it from the answer. Other text returns null and shows as it is.
 */
export function splitAnswer(text: string): { question: string; answer: string } | null {
  if (!text.startsWith(answerPrefix)) return null;

  const end = text.indexOf("\n");

  if (end < 0) return null;

  return { question: text.slice(answerPrefix.length, end), answer: text.slice(end + 1) };
}

/** Edit and Remove for messages that wait in the native queue. */
/** Starts a new Chat from a message. before ends the copy just before the entry; text prefills the new composer. */
export type MessageActions = {
  pending: boolean;
  fork: (entryId: number, before: boolean, text?: string) => void;
};

export type QueuedActions = {
  /** Item IDs that the live stream confirms as queued. A message that is only local cannot be withdrawn yet. */
  ids: ReadonlySet<string>;
  pending: boolean;
  edit: (id: string) => void;
  remove: (id: string) => void;
};

function UserText({ text }: { text: string }) {
  const answer = splitAnswer(text);

  if (!answer) return text;

  return (
    <>
      <span className="block text-[12px] text-muted-foreground">{answer.question}</span>
      {answer.answer}
    </>
  );
}

function Divider({ label }: { label: string }) {
  return (
    <div role="separator" className="flex items-center gap-3 py-3 text-[12px] text-muted-foreground">
      <span className="h-px flex-1 bg-border" />
      <span>{label}</span>
      <span className="h-px flex-1 bg-border" />
    </div>
  );
}

function BoundaryItem({ item }: { item: Extract<Item, { kind: "boundary" }> }) {
  if (item.boundary === "reset")
    return (
      <div className="flex flex-col gap-1">
        <Divider label="Context cleared" />
        {item.text ? (
          <div className="mx-auto flex max-w-[560px] flex-col gap-1 rounded-md bg-muted px-3 py-2 text-[12.5px]">
            <span className="font-medium text-muted-foreground">Carried into the new context</span>
            <p className="whitespace-pre-wrap text-foreground [overflow-wrap:anywhere]">{item.text}</p>
          </div>
        ) : null}
      </div>
    );

  switch (item.status) {
    case "pending":
      return (
        <p
          role="status"
          className="flex items-center justify-center gap-2 py-2 text-[12.5px] text-muted-foreground"
        >
          <span aria-hidden className="size-1.5 animate-pulse rounded-full bg-muted-foreground" />
          Compacting context…
        </p>
      );
    case "queued":
      return (
        <p role="status" className="py-2 text-center text-[12.5px] text-muted-foreground">
          Summary ready. It replaces the older context before the next model call.
        </p>
      );
    case "nothing":
      return (
        <p role="status" className="py-2 text-center text-[12.5px] text-muted-foreground">
          Nothing to compact. The context is still short.
        </p>
      );
    case "failed":
      return (
        <div
          role="alert"
          className="mx-auto flex max-w-[560px] flex-col gap-1 rounded-md bg-red-tint px-3 py-2 text-[12.5px]"
        >
          <span className="font-medium text-red">Could not compact the context</span>
          {item.reason ? (
            <p className="text-muted-foreground [overflow-wrap:anywhere]">{item.reason}</p>
          ) : null}
        </div>
      );
    case undefined:
      return (
        <div className="flex flex-col gap-1">
          <Divider label="Context compacted" />
          {item.text ? (
            <details className="mx-auto w-full max-w-[640px] text-[12.5px]">
              <summary className="cursor-pointer text-center text-muted-foreground hover:text-foreground">
                Show summary
              </summary>
              <div className="mt-2 rounded-md bg-muted px-3 py-2">
                <Markdown text={item.text} />
              </div>
            </details>
          ) : null}
        </div>
      );
  }
}

export function TranscriptItem({
  item,
  live,
  retry,
  canRetry,
  queuedActions,
  actions,
  forkable = false,
  ending = false,
}: {
  item: Exclude<Item, { kind: "tool" | "thinking" }>;
  live: boolean;
  retry: () => void;
  canRetry: boolean;
  queuedActions?: QueuedActions;
  actions?: MessageActions;
  /** The answer waits for no tool result, so a fork can start after it. */
  forkable?: boolean;
  /** The answer ends its turn. Only such an answer has actions, so a turn reads as one unit. */
  ending?: boolean;
}) {
  switch (item.kind) {
    case "boundary":
      return (
        <div className="mt-5 first:mt-0">
          <BoundaryItem item={item} />
        </div>
      );
    case "user": {
      const origin = entryName(item.origin);
      const actionable = !!item.entryId && (!!item.text || !!actions);

      return (
        <div className="group mt-5 flex animate-[fade-up_300ms_var(--ease-out-strong)_both] flex-col items-end gap-1 pl-10 first:mt-0 sm:pl-24">
          {item.attachments?.length ? (
            <div className={cn("max-w-full", item.queued && "opacity-70")}>
              <Files files={item.attachments} />
            </div>
          ) : null}
          {item.text ? (
            <div
              className={cn(
                "rounded-bubble bg-muted px-4 py-2.5 text-[15px] leading-relaxed whitespace-pre-wrap text-foreground [overflow-wrap:anywhere]",
                item.queued && "opacity-70",
              )}
            >
              <UserText text={item.text} />
            </div>
          ) : null}
          {item.queued ? (
            <span className="flex items-center gap-2 text-[11.5px] text-muted-foreground">
              <span className="flex items-center gap-1">
                {item.steer ? (
                  <>
                    <CornerDownLeft size={12} /> Steer · joins after the current tool step
                  </>
                ) : (
                  <>
                    <Clock3 size={12} /> Queued · runs after the current reply
                  </>
                )}
              </span>
              {queuedActions?.ids.has(item.id) ? (
                <>
                  <Button
                    size="xs"
                    variant="secondary"
                    disabled={queuedActions.pending}
                    title="Edit moves it back to the box"
                    onClick={() => queuedActions.edit(item.id)}
                  >
                    Edit
                  </Button>
                  <Button
                    size="xs"
                    variant="secondary"
                    disabled={queuedActions.pending}
                    onClick={() => queuedActions.remove(item.id)}
                  >
                    Remove
                  </Button>
                </>
              ) : null}
            </span>
          ) : actionable || origin || item.at ? (
            <footer className="flex h-7 items-center gap-0.5 text-[11.5px] text-muted-foreground">
              {actionable ? (
                <span className="flex opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 pointer-coarse:opacity-100">
                  {item.text ? <CopyText text={item.text} label="Copy message" /> : null}
                  {actions ? (
                    <MessageAction
                      label="Edit in new chat"
                      disabled={actions.pending}
                      onClick={() => actions.fork(item.entryId!, true, item.text)}
                    >
                      <SquarePen size={14} aria-hidden />
                    </MessageAction>
                  ) : null}
                </span>
              ) : null}
              {origin || item.at ? (
                <span className="px-1.5">
                  {origin ? <span data-origin={item.origin}>{origin}</span> : null}
                  {origin && item.at ? " · " : null}
                  {item.at ? <time dateTime={new Date(item.at).toISOString()}>{stamp(item.at)}</time> : null}
                </span>
              ) : null}
            </footer>
          ) : null}
        </div>
      );
    }

    case "task":
      if (item.taskId) return <ResultLine item={item} />;

      return (
        <header className="flex min-w-0 items-start gap-2 text-[13px] leading-5">
          <CornerDownRight size={16} aria-hidden className="mt-0.5 shrink-0 text-muted-foreground" />
          <h3 className="min-w-0 [overflow-wrap:anywhere]">
            <span className="mr-1.5 font-medium text-muted-foreground">Background command</span>
            <span className="font-semibold text-foreground">{item.title}</span>
          </h3>
        </header>
      );
    case "assistant":
      if (!live && !item.text.trim()) return null;

      return (
        <article className="group relative min-w-0">
          {live ? (
            <div className="max-w-[680px] text-[15px] leading-[1.75] whitespace-pre-wrap text-foreground [overflow-wrap:anywhere]">
              <span>
                {item.text}
                <span aria-hidden className="stream-caret is-streaming" />
              </span>
            </div>
          ) : (
            <Markdown text={item.text} />
          )}
          {ending ? (
            // With a mouse, the actions show on hover in the 32px gap below the turn, so they
            // take no space. A touch screen has no hover, so there they always show.
            <footer className="mt-1 flex h-7 items-center gap-1.5 text-[11.5px] text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100 pointer-coarse:opacity-100 pointer-fine:absolute pointer-fine:inset-x-0 pointer-fine:top-full">
              {item.model ? <span className="min-w-0 truncate font-mono">{item.model}</span> : null}
              {item.usage ? (
                <span className="shrink-0 tabular-nums">
                  {item.usage.input.toLocaleString()} in · {item.usage.output.toLocaleString()} out
                </span>
              ) : null}
              {live ? null : <CopyText text={item.text} label="Copy answer" />}
              {actions && item.entryId && forkable && !live ? (
                <MessageAction
                  label="Fork from here"
                  disabled={actions.pending}
                  onClick={() => actions.fork(item.entryId!, false)}
                >
                  <GitBranch size={14} aria-hidden />
                </MessageAction>
              ) : null}
            </footer>
          ) : null}
        </article>
      );
    case "error":
      // Native continue found nothing to answer: this is a note, not a failure.
      if (item.failure === "nothing_to_answer")
        return (
          <p role="status" className="text-[12.5px] text-muted-foreground">
            {item.reason}
          </p>
        );

      return (
        <div
          role={item.aborted ? "status" : "alert"}
          className={cn(
            "flex max-w-[640px] flex-col gap-2 rounded-md px-3.5 py-3 text-[13px]",
            item.aborted ? "bg-muted shadow-card" : "bg-red-tint",
          )}
        >
          <div className="flex items-center gap-2">
            <span
              aria-hidden
              className={cn(
                "size-1.5 shrink-0 rounded-full",
                item.aborted ? "bg-muted-foreground" : "bg-red",
              )}
            />
            <span className={cn("font-medium", item.aborted ? "text-foreground" : "text-red")}>
              {item.aborted ? "Run stopped" : "The bot could not finish"}
            </span>
          </div>
          <p className="pl-3.5 text-[12.5px] text-muted-foreground [overflow-wrap:anywhere]">{item.reason}</p>
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

// A card for the last native run failure retries with native continue. Other error cards send the message again.
function retryable(items: Item[], index: number) {
  const item = items[index];

  if (item?.kind === "error" && item.failure) return item.failure !== "nothing_to_answer";

  return retryMessage(items, index) !== undefined;
}

export function Transcript({
  items,
  running,
  retry,
  canRetry,
  memory,
  queuedActions,
  messageActions,
}: {
  items: Item[];
  running: boolean;
  retry: (index: number) => void;
  canRetry: boolean;
  memory?: string;
  queuedActions?: QueuedActions;
  messageActions?: MessageActions;
}) {
  // An answer that called tools shares its entry with the tool items. A fork after it would copy
  // calls whose results never come.
  const withTools = new Set(
    items.flatMap((item) => (item.kind === "tool" && item.entryId ? [item.entryId] : [])),
  );

  function content(block: ContentBlock) {
    return block.kind === "steps" ? (
      <Steps
        key={block.key}
        steps={block.steps}
        memory={memory}
        thinking={running && block.last === items.length - 1 && block.steps.at(-1)?.kind === "thinking"}
      />
    ) : (
      <TranscriptItem
        key={block.item.id}
        item={block.item}
        live={running && block.index === items.length - 1}
        retry={() => retry(block.index)}
        canRetry={canRetry && retryable(items, block.index)}
        queuedActions={queuedActions}
        actions={messageActions}
        forkable={!!block.item.entryId && !withTools.has(block.item.entryId)}
        ending={["user", "task", "boundary", undefined].includes(items[block.index + 1]?.kind)}
      />
    );
  }

  return blocks(items).map((block) =>
    block.kind === "task" ? (
      <section
        key={block.item.id}
        aria-label={`Session result: ${block.item.title}`}
        className="mt-5 flex min-w-0 flex-col gap-3 first:mt-0"
      >
        <TranscriptItem item={block.item} live={false} retry={() => {}} canRetry={false} />
        {/* The main agent answers the user here; indenting it made the reply read as background output. */}
        {block.blocks.length ? (
          <div className="flex min-w-0 flex-col gap-3">{block.blocks.map(content)}</div>
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
      className="flex max-w-[640px] flex-col gap-1 rounded-md bg-muted px-3.5 py-3 shadow-card"
    >
      <Shimmer>
        Provider error · retry {retry.attempt} in {Math.max(0, Math.ceil((retry.at - now) / 1000))} s
      </Shimmer>
      <p className="text-[12.5px] text-muted-foreground [overflow-wrap:anywhere]">{retry.reason}</p>
    </div>
  );
}

export function Markdown({ text }: { text: string }) {
  return (
    <div className="max-w-[680px] text-[15px] leading-[1.75] text-foreground [overflow-wrap:anywhere] [&>*:first-child]:mt-0 [&>*:last-child]:mb-0">
      <ReactMarkdown remarkPlugins={[remarkGfm, remarkStrong]} components={markdown}>
        {strongText(text)}
      </ReactMarkdown>
    </div>
  );
}
