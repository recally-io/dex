import { useRef } from "react";
import { Link } from "@tanstack/react-router";
import { ArrowUp, Square } from "lucide-react";
import type { Agent } from "../api/generated/types.gen";
import { Button } from "./ui/button";
import { Textarea } from "./ui/textarea";
import { Tooltip, TooltipTrigger, TooltipContent } from "./ui/tooltip";
import { ModelPicker } from "./model-picker";

export function Composer({
  agent,
  running,
  disabledReason,
  pending,
  onSend,
  onStop,
  onModel,
  text,
  onTextChange,
}: {
  agent: Agent;
  running: boolean;
  disabledReason?: string;
  pending: boolean;
  onSend: (text: string) => Promise<void>;
  onStop: () => void;
  onModel: (provider: string, id: string) => void;
  text: string;
  onTextChange: (text: string) => void;
}) {
  const area = useRef<HTMLTextAreaElement>(null);
  const composing = useRef(false);

  async function send() {
    if (!text.trim() || disabledReason || pending) return;
    const value = text;
    await onSend(value.trim());
  }

  return (
    <div className="w-full">
      <form
        className="surface p-3 shadow-raised"
        onSubmit={(e) => {
          e.preventDefault();
          void send().catch(() => {});
        }}
      >
        <Textarea
          ref={(element) => {
            area.current = element;

            if (element) {
              element.style.height = "auto";
              element.style.height = `${Math.min(element.scrollHeight, 200)}px`;
            }
          }}
          rows={1}
          className="min-h-12 resize-none border-0 bg-transparent px-2 py-3 shadow-none focus-visible:ring-0"
          maxLength={32000}
          placeholder={disabledReason === "Reply in Telegram" ? "Reply in Telegram" : "Message dex…"}
          aria-label="Message dex"
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
            if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing && !composing.current) {
              e.preventDefault();
              void send().catch(() => {});
            }
          }}
        />
        <div className="flex items-center justify-between gap-3">
          <ModelPicker
            provider={agent.provider}
            modelId={agent.modelId}
            onSelect={onModel}
            disabled={pending}
          />
          <div className="flex items-center gap-2">
            {running ? (
              <Tooltip>
                <TooltipTrigger
                  render={
                    <Button
                      type="button"
                      size="icon"
                      variant="outline"
                      aria-label="Stop run"
                      disabled={pending || !!disabledReason}
                      onClick={onStop}
                    />
                  }
                >
                  <Square className="size-4 fill-current" />
                </TooltipTrigger>
                <TooltipContent>Stop the run and withdraw queued messages.</TooltipContent>
              </Tooltip>
            ) : null}
            {!running || text.trim() ? (
              <Button
                type="submit"
                size="icon"
                aria-label={running ? "Queue message" : "Send message"}
                disabled={!text.trim() || !!disabledReason || pending}
              >
                <ArrowUp className="size-4" />
              </Button>
            ) : null}
          </div>
        </div>
      </form>
      <p className="mt-3 text-center text-xs text-ink-2">
        {disabledReason || "Tools run on this computer. Review commands and results."}
        {!agent.modelId ? (
          <>
            {" "}
            <Link className="text-brand underline" to="/settings/$section" params={{ section: "model" }}>
              Choose a model
            </Link>
          </>
        ) : null}
      </p>
    </div>
  );
}
