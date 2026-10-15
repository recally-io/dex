import { useEffect, useId, useRef } from "react";
import { Link } from "@tanstack/react-router";
import { ArrowUp, ChevronDown, Square } from "lucide-react";
import type { AgentModel } from "../lib/agent";
import { Button } from "./atoms/Button";
import { ChatComposerInput } from "./primitives/ChatComposer";
import { Tooltip, TooltipTrigger, TooltipContent } from "./ui/tooltip";
import { ModelPicker } from "./model-picker";

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
  autoFocus?: boolean;
  readOnly?: boolean;
}) {
  const area = useRef<HTMLTextAreaElement>(null);
  const composing = useRef(false);
  const descriptionId = useId();
  useEffect(() => {
    if (autoFocus) area.current?.focus();
  }, [autoFocus]);

  async function send() {
    if (!text.trim() || disabledReason || pending) return;
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
      >
        <ChatComposerInput
          footer={
            <>
              <div className="min-w-0">
                <ModelPicker
                  trigger={
                    <Button
                      type="button"
                      size="xs"
                      variant="quiet"
                      aria-label={`Agent model: ${model.label}. Change model`}
                      className={`max-w-full ${model.ok ? "" : "text-orange"}`}
                    >
                      <span className="truncate">{model.label}</span>
                      <ChevronDown aria-hidden className="size-3.5 shrink-0" />
                    </Button>
                  }
                />
              </div>
              <div className="flex shrink-0 items-center gap-2">
                {running ? (
                  <Tooltip>
                    <TooltipTrigger
                      render={
                        <Button
                          type="button"
                          variant="secondary"
                          aria-label="Stop run"
                          className="size-9 rounded-control p-0 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ink"
                          // A missing model blocks new messages, not stopping the current run.
                          disabled={pending || readOnly}
                          onClick={onStop}
                        />
                      }
                    >
                      <Square aria-hidden="true" className="size-3.5 fill-current" />
                    </TooltipTrigger>
                    <TooltipContent>Stop the run and withdraw queued messages.</TooltipContent>
                  </Tooltip>
                ) : null}
                {!running || text.trim() ? (
                  <Button
                    type="submit"
                    variant="primary"
                    aria-label={running ? "Queue message" : "Send message"}
                    title={running ? "Queue message" : "Send message"}
                    className="size-9 rounded-control p-0 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ink"
                    disabled={!text.trim() || !!disabledReason || pending}
                  >
                    <ArrowUp aria-hidden="true" className="size-4" />
                  </Button>
                ) : null}
              </div>
            </>
          }
        >
          <textarea
            ref={(element) => {
              area.current = element;

              if (element) {
                element.style.height = "auto";
                element.style.height = `${Math.min(element.scrollHeight, 200)}px`;
              }
            }}
            rows={2}
            className="block max-h-50 min-h-20 w-full resize-none border-0 bg-transparent p-0 text-sm leading-6 text-ink outline-none placeholder:text-ink-3 disabled:cursor-not-allowed disabled:opacity-60"
            maxLength={32000}
            placeholder={readOnly ? "Reply in Telegram" : running ? "Queue a follow-up…" : "Message dex…"}
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
        </ChatComposerInput>
      </form>
      <p id={descriptionId} className="mt-3 text-center text-xs text-ink-2">
        {disabledReason || "Tools run on this computer without a sandbox. Review commands and results."}
        {!model.ok && !readOnly ? (
          <>
            {" "}
            {hasProvider ? (
              <ModelPicker
                trigger={
                  <button type="button" className="text-brand underline">
                    Choose a model
                  </button>
                }
              />
            ) : (
              <Link className="text-brand underline" to="/providers">
                Add a provider
              </Link>
            )}
          </>
        ) : null}
      </p>
    </div>
  );
}
