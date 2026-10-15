import { useEffect, useId, useRef } from "react";
import { Link } from "@tanstack/react-router";
import { IconLock } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconLock";
import type { AgentModel } from "../lib/agent";
import { ModelPicker } from "./model-picker";
import { cn } from "../lib/utils";

// Look from upstream IceCreamHarness.tsx: a floating card with a model button and round actions.
export function Composer({
  model,
  hasProvider,
  running,
  disabledReason,
  pending,
  onSend,
  onStop,
  text,
  onTextChange,
  placeholder,
  autoFocus = false,
  readOnly = false,
}: {
  model: AgentModel;
  hasProvider: boolean;
  running: boolean;
  disabledReason?: string;
  pending: boolean;
  onSend: (text: string) => Promise<void>;
  onStop: () => void;
  text: string;
  onTextChange: (text: string) => void;
  placeholder: string;
  autoFocus?: boolean;
  readOnly?: boolean;
}) {
  const area = useRef<HTMLTextAreaElement>(null);
  const composing = useRef(false);
  const descriptionId = useId();
  const canSend = !!text.trim() && !disabledReason && !pending;
  useEffect(() => {
    if (autoFocus) area.current?.focus();
  }, [autoFocus]);

  async function send() {
    if (!canSend) return;
    const value = text;
    await onSend(value.trim());
  }

  return (
    <div className="w-full">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void send().catch(() => {});
        }}
        className={cn(
          "relative isolate flex flex-col gap-2.5 overflow-hidden rounded-[22px] border border-line bg-surface p-3.5 shadow-card transition-[border-color] duration-150 focus-within:border-line-strong",
          readOnly && "opacity-80",
        )}
      >
        <textarea
          ref={(element) => {
            area.current = element;

            if (element) {
              element.style.height = "auto";
              element.style.height = `${Math.min(element.scrollHeight, 200)}px`;
            }
          }}
          rows={1}
          className="min-h-[52px] w-full min-w-0 resize-none bg-transparent px-2 py-1.5 text-[14px] leading-5 text-ink outline-none [overflow-wrap:anywhere] placeholder:text-ink-3 disabled:cursor-not-allowed"
          maxLength={32000}
          placeholder={readOnly ? "Reply in Telegram" : running ? "Queue a follow-up…" : placeholder}
          aria-label="Message dex"
          aria-describedby={descriptionId}
          value={text}
          disabled={!!disabledReason}
          onChange={(e) => onTextChange(e.target.value)}
          onCompositionStart={() => {
            composing.current = true;
          }}
          onCompositionEnd={() => {
            composing.current = false;
          }}
          onKeyDown={(e) => {
            if (
              e.key === "Enter" &&
              !e.shiftKey &&
              !e.nativeEvent.isComposing &&
              !composing.current &&
              e.nativeEvent.keyCode !== 229
            ) {
              e.preventDefault();
              void send().catch(() => {});
            }
          }}
        />
        <div className="flex min-w-0 items-center gap-1">
          {readOnly ? (
            <span className="flex h-7 items-center gap-1.5 px-1.5 text-[12px] font-medium text-ink-3">
              <IconLock size={13} /> Read-only on the web
            </span>
          ) : (
            <ModelPicker
              trigger={
                <button
                  type="button"
                  aria-label={`Agent model: ${model.label}. Change model`}
                  className={cn(
                    "flex h-7 min-w-0 shrink items-center gap-1 rounded-control px-1.5 text-[12px] font-medium transition-colors duration-150 hover:bg-hover hover:text-ink aria-expanded:bg-hover",
                    model.ok ? "text-ink-2" : "text-orange",
                  )}
                >
                  <span className="truncate">{model.label}</span>
                  <svg
                    width="11"
                    height="11"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="2.4"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    aria-hidden
                    className="shrink-0 text-ink-3"
                  >
                    <path d="M6 9l6 6 6-6" />
                  </svg>
                </button>
              }
            />
          )}
          <span className="min-w-2 flex-1" />
          {running ? (
            <button
              type="button"
              aria-label="Stop run"
              title="Stop the run and withdraw queued messages"
              // A missing model blocks new messages, not stopping the current run.
              disabled={pending || readOnly}
              onClick={onStop}
              className="flex size-8 shrink-0 items-center justify-center rounded-full bg-ink text-surface transition-[transform,opacity] duration-150 enabled:active:scale-[0.94] disabled:opacity-40"
            >
              <span aria-hidden className="size-2.5 rounded-[2px] bg-current" />
            </button>
          ) : null}
          {!readOnly && (!running || text.trim()) ? (
            <button
              type="submit"
              aria-label={running ? "Queue message" : "Send message"}
              title={running ? "Queue message" : "Send message"}
              disabled={!canSend}
              className={cn(
                "flex size-8 shrink-0 items-center justify-center rounded-full transition-[background-color,color,transform] duration-200 enabled:active:scale-[0.94]",
                canSend ? "bg-ink text-surface" : "bg-line-strong text-ink-2",
              )}
            >
              <svg
                width="16"
                height="16"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2.4"
                strokeLinecap="round"
                strokeLinejoin="round"
                aria-hidden
              >
                <path d="M12 19V5M5 12l7-7 7 7" />
              </svg>
            </button>
          ) : null}
        </div>
      </form>
      <p id={descriptionId} className="mt-2 px-2 text-center text-[11.5px] text-ink-3">
        {disabledReason || "Tools run on this computer without a sandbox. Review commands and results."}
        {!model.ok && !readOnly ? (
          <>
            {" "}
            {hasProvider ? (
              <ModelPicker
                trigger={
                  <button type="button" className="font-medium text-accent-ink underline">
                    Choose a model
                  </button>
                }
              />
            ) : (
              <Link className="font-medium text-accent-ink underline" to="/providers">
                Add a provider
              </Link>
            )}
          </>
        ) : null}
      </p>
    </div>
  );
}
