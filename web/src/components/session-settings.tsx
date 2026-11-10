import { useMutation, useQuery } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { ChevronDown } from "lucide-react";
import type { AgentSettings as Settings } from "../lib/agent-scope";
import { useAgentId } from "../lib/agent-scope";
import type { Chat, ChatSettings, Model, ThinkingLevel } from "../api/generated/types.gen";
import { listModelsOptions, setChatSettingsMutation } from "../api/generated/@tanstack/react-query.gen";
import { findModel, thinkingNames } from "../lib/agent";
import { refreshChats } from "../lib/queries";
import { ChatProject } from "./chat-project";
import { ModelPicker } from "./model-picker";
import { ThinkingPicker } from "./thinking-picker";
import { Button } from "./ui/button";

// A change keeps the other field. null clears a field so that it follows the bot again.
function changed(
  own: ChatSettings,
  change: { model: Model | null } | { thinking: ThinkingLevel | null },
): ChatSettings {
  const next: ChatSettings = {};

  const model =
    "model" in change ? change.model : own.modelId ? { provider: own.provider ?? "", id: own.modelId } : null;

  const thinking = "thinking" in change ? change.thinking : (own.thinkingLevel ?? null);

  if (model) {
    next.provider = model.provider;
    next.modelId = model.id;
  }

  if (thinking) next.thinkingLevel = thinking;

  return next;
}

// The project, model and thinking level of one session. Main, Chat sessions and finished
// delegated sessions share this rule: each change applies from the next input, including
// input from a channel.
export function SessionSettings({ chat, settings }: { chat: Chat; settings: Settings }) {
  const agentId = useAgentId();
  const models = useQuery(listModelsOptions());
  const save = useMutation(setChatSettingsMutation());
  const saving = useRef(false);
  const [pending, setPending] = useState(false);
  const own = chat.settings;
  const bot = findModel(settings.agent.provider, settings.agent.modelId, models.data?.models);
  const effective = own.modelId ? findModel(own.provider ?? "", own.modelId, models.data?.models) : bot;

  async function change(next: ChatSettings) {
    if (saving.current) throw new Error("Session settings are saving. Try again.");
    saving.current = true;
    setPending(true);

    try {
      await save.mutateAsync({ path: { agentId, chatId: chat.id }, body: next });
      await refreshChats(agentId);
    } finally {
      saving.current = false;
      setPending(false);
    }
  }

  return (
    <div className="flex min-w-0 items-center gap-1">
      <ChatProject chatId={chat.id} cwd={chat.cwd} />
      <ModelPicker
        override={{
          value: own.modelId ? { provider: own.provider ?? "", modelId: own.modelId } : null,
          defaultName: bot.label,
          save: (model: Model | null) => change(changed(own, { model })),
        }}
        trigger={
          <Button
            variant="ghost"
            size="sm"
            className="max-w-48"
            aria-label="Set model"
            disabled={pending}
            title={effective.label}
          >
            <span className="truncate">{effective.label}</span>
            <ChevronDown aria-hidden="true" />
          </Button>
        }
      />
      {effective.model?.reasoning ? (
        <ThinkingPicker
          clearable
          value={own.thinkingLevel ?? null}
          levels={effective.model.thinkingLevels}
          save={(thinking) => change(changed(own, { thinking }))}
          trigger={
            <Button variant="ghost" size="sm" aria-label="Set thinking level" disabled={pending}>
              {thinkingNames[own.thinkingLevel ?? settings.agent.thinkingLevel]}
              <ChevronDown aria-hidden="true" />
            </Button>
          }
        />
      ) : null}
    </div>
  );
}
