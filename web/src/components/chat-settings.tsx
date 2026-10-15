import type { ComponentProps } from "react";
import { useQuery } from "@tanstack/react-query";
import { IconBrain } from "@central-icons-react/round-outlined-radius-2-stroke-2/IconBrain";
import type { Agent, ChatSettings } from "../api/generated/types.gen";
import { listModelsOptions } from "../api/generated/@tanstack/react-query.gen";
import { agentModel, chatModel, thinkingNames } from "../lib/agent";
import { cn } from "../lib/utils";
import { ModelPicker } from "./model-picker";
import { ThinkingPicker } from "./thinking-picker";

export type SaveChatSettings = (change: (saved: ChatSettings) => ChatSettings) => Promise<void>;

function Chevron() {
  return (
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
  );
}

// Marks a setting that the chat overrides.
function Override() {
  return <span aria-hidden title="Set for this chat" className="size-1.5 shrink-0 rounded-full bg-accent" />;
}

function ControlButton({ className, children, ...props }: ComponentProps<"button">) {
  return (
    <button
      type="button"
      {...props}
      className={cn(
        "flex h-7 min-w-0 items-center gap-1 rounded-control px-1.5 text-[12px] font-medium text-ink-2 transition-colors duration-150 hover:bg-hover hover:text-ink aria-disabled:cursor-not-allowed aria-disabled:opacity-60 aria-disabled:hover:bg-transparent aria-expanded:bg-hover",
        className,
      )}
    >
      {children}
      <Chevron />
    </button>
  );
}

/** The model and thinking level of one chat. Each list starts with the agent's default. */
export function ChatControls({
  agent,
  value,
  save,
}: {
  agent: Agent;
  value: ChatSettings;
  save: SaveChatSettings;
}) {
  const models = useQuery(listModelsOptions());
  const list = models.data?.models;
  const { model } = chatModel(agent, value, list);
  const own = !!value.modelId;
  const think = model.model?.reasoning ? model.model : undefined;

  // A disabled control keeps its title as the reason; a disabled button shows no title in Chrome.
  const noThinking = !model.model
    ? "Choose an available model first."
    : think
      ? undefined
      : `${model.model.name} does not support thinking.`;

  return (
    <>
      <ModelPicker
        override={{
          value: own ? { provider: value.provider ?? "", modelId: value.modelId ?? "" } : null,
          defaultName: agentModel(agent, list).label,
          save: (m) => save((saved) => ({ ...saved, provider: m?.provider, modelId: m?.id })),
        }}
        trigger={
          <ControlButton
            aria-label={`Chat model: ${model.label}${own ? ", set for this chat" : ""}. Change model`}
            className={cn("shrink", !model.ok && "text-orange")}
          >
            <span className="truncate">{model.label}</span>
            {own ? <Override /> : null}
          </ControlButton>
        }
      />
      {noThinking ? (
        <ControlButton
          aria-disabled
          aria-label={`Thinking: off. ${noThinking}`}
          title={noThinking}
          className="shrink-0"
        >
          <IconBrain size={14} />
          <span>Off</span>
        </ControlButton>
      ) : (
        <ThinkingPicker
          value={value.thinkingLevel ?? null}
          levels={think?.thinkingLevels ?? []}
          clearable
          save={(level) => save((saved) => ({ ...saved, thinkingLevel: level ?? undefined }))}
          trigger={
            <ControlButton
              aria-label={`Thinking: ${value.thinkingLevel ? `${thinkingNames[value.thinkingLevel]}, set for this chat` : "default"}. Change thinking level`}
              title="Thinking level"
              className="shrink-0"
            >
              <IconBrain size={14} />
              <span>{value.thinkingLevel ? thinkingNames[value.thinkingLevel] : "Default"}</span>
              {value.thinkingLevel ? <Override /> : null}
            </ControlButton>
          }
        />
      )}
    </>
  );
}
