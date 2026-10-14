import { useEffect, useState } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { Check, Copy, ChevronRight, Terminal, AlertCircle, Square, Brain } from "lucide-react";
import type { Item, Retry } from "../../api/generated/types.gen";
import { Shimmer } from "../atoms/Shimmer";
import { StreamText } from "../atoms/StreamText";
import CodeBlock from "../atoms/CodeBlock";
import { Button } from "../ui/button";
import { Alert, AlertTitle, AlertDescription } from "../ui/alert";

const markdownComponents: Components = {
  pre: ({ children }) => <div>{children}</div>,
  code: ({ className, children }) => {
    const text = String(children).replace(/\n$/, "");

    return className || text.includes("\n") ? (
      <CodeBlock
        filename={className?.replace("language-", "") || "Code"}
        lines={text.split("\n")}
        code={text}
      />
    ) : (
      <code className={className}>{children}</code>
    );
  },
};

function CopyText({ text }: { text: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");

  return (
    <Button
      variant="ghost"
      size="sm"
      aria-label="Copy answer"
      onClick={() => {
        void navigator.clipboard.writeText(text).then(
          () => setState("copied"),
          () => setState("failed"),
        );
      }}
    >
      {state === "copied" ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
      {state === "failed" ? "Copy failed" : state === "copied" ? "Copied" : null}
    </Button>
  );
}

export function TranscriptItem({
  item,
  live,
  retry,
  canRetry,
}: {
  item: Item;
  live: boolean;
  retry: (text: string) => void;
  canRetry: boolean;
}) {
  switch (item.kind) {
    case "user":
      return (
        <div className="flex justify-end">
          <div className="max-w-[85%] rounded-2xl rounded-br-md bg-hover px-4 py-3">
            <p className="whitespace-pre-wrap break-words leading-relaxed">{item.text}</p>
            {item.queued ? <p className="mt-2 text-right text-xs text-ink-2">Queued</p> : null}
          </div>
        </div>
      );
    case "thinking":
      return (
        <details className="text-sm text-ink-2">
          <summary className="flex items-center gap-2 py-2">
            <ChevronRight className="size-3.5" />
            <Brain className="size-4" />
            {live ? <Shimmer>Thinking…</Shimmer> : "Thought process"}
          </summary>
          <p className="mt-2 whitespace-pre-wrap rounded-lg bg-inset p-4 text-xs leading-relaxed">
            {item.text || "The provider did not share its thought process."}
          </p>
        </details>
      );
    case "tool":
      return (
        <details
          open={item.status === "error" ? true : undefined}
          className="surface overflow-hidden text-sm"
        >
          <summary className="flex items-center gap-2.5 p-3">
            <ChevronRight className="size-3.5 shrink-0 text-ink-3" />
            <Terminal className="size-4 shrink-0 text-ink-2" />
            <span className="font-mono text-xs font-medium">
              {item.status === "running" ? <Shimmer>{item.name}</Shimmer> : item.name}
            </span>
            <span className="min-w-0 flex-1 truncate font-mono text-xs text-ink-2">
              {Object.values(item.args)
                .map((v) => JSON.stringify(v))
                .join(" · ")
                .slice(0, 100)}
            </span>
            {item.durationMs !== undefined ? (
              <span className="shrink-0 text-xs text-ink-3">{(item.durationMs / 1000).toFixed(1)} s</span>
            ) : null}
            {item.status === "error" ? (
              <AlertCircle className="size-4 text-red" />
            ) : item.status === "done" ? (
              <Check className="size-4 text-green" />
            ) : (
              <span className="busy-dot" />
            )}
          </summary>
          <div className="flex flex-col gap-3 px-3 pb-3">
            <CodeBlock
              filename="Arguments"
              lines={JSON.stringify(item.args, null, 2).split("\n")}
              code={JSON.stringify(item.args, null, 2)}
            />
            {item.output ? (
              <CodeBlock filename="Output" lines={item.output.split("\n")} code={item.output} />
            ) : (
              <p className="p-2 text-xs text-ink-2">
                {item.status === "running" ? "Waiting for tool output…" : "No output."}
              </p>
            )}
          </div>
        </details>
      );
    case "assistant":
      return (
        <article className="group">
          <div className="mb-3 flex items-center gap-2 text-xs font-medium text-ink-2">
            <span className="flex size-6 items-center justify-center rounded-md bg-ink text-page">d</span>dex
          </div>
          {live ? (
            <div className="whitespace-pre-wrap leading-7">
              <StreamText text={item.text} charsPerTick={Math.max(2, item.text.length)} tickMs={10} />
            </div>
          ) : (
            <div className="prose">
              <ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>
                {item.text}
              </ReactMarkdown>
            </div>
          )}
          <footer className="mt-2 flex items-center gap-3 text-xs text-ink-3 opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100 pointer-coarse:opacity-100">
            <span>{item.model}</span>
            {item.usage ? (
              <span>
                {item.usage.input.toLocaleString()} in · {item.usage.output.toLocaleString()} out
              </span>
            ) : null}
            <CopyText text={item.text} />
          </footer>
        </article>
      );
    case "error":
      return (
        <Alert variant={item.aborted ? "default" : "destructive"}>
          {item.aborted ? <Square /> : <AlertCircle />}
          <AlertTitle>{item.aborted ? "Run stopped" : "The agent could not finish"}</AlertTitle>
          <AlertDescription>
            <p>{item.reason}</p>
            {canRetry ? (
              <Button variant="outline" size="sm" onClick={() => retry("")}>
                Retry
              </Button>
            ) : (
              <p className="text-xs">Retry is unavailable while the chat cannot accept messages.</p>
            )}
          </AlertDescription>
        </Alert>
      );
  }
}

export function RetryNotice({ retry }: { retry: Retry }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);

    return () => clearInterval(timer);
  }, []);

  return (
    <div role="status" className="rounded-lg bg-inset px-4 py-3 text-xs text-ink-2">
      <Shimmer>
        Provider error · retry {retry.attempt} in {Math.max(0, Math.ceil((retry.at - now) / 1000))} s
      </Shimmer>
      <p className="mt-1">{retry.reason}</p>
    </div>
  );
}
