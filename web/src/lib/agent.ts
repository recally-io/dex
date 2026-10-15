import { useQuery } from "@tanstack/react-query";
import type { Agent, ChatSettings, Model, Settings, ThinkingLevel } from "../api/generated/types.gen";
import { setAgent, setChatSettings } from "../api/generated/sdk.gen";
import {
  getChatOptions,
  getChatQueryKey,
  getSettingsOptions,
  listModelsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { queryClient, refreshSettings } from "./queries";

let queue: Promise<void> = Promise.resolve();

// PUT /api/settings/agent writes the model, the thinking level and the instructions together.
// Saves run one at a time, and each reads the saved agent just before it writes, so a model change
// keeps the saved instructions and an instructions save keeps the saved model.
export function saveAgent(change: (saved: Agent) => Agent): Promise<void> {
  const run = queue.then(async () => {
    const { agent } = await queryClient.fetchQuery({ ...getSettingsOptions(), staleTime: 0 });

    try {
      await setAgent({ body: change(agent), throwOnError: true });
    } catch (cause) {
      void refreshSettings();
      throw cause;
    }

    await refreshSettings();
  });

  queue = run.catch(() => {});

  return run;
}

export function saveModel(provider: string, modelId: string) {
  return saveAgent((saved) => ({ ...saved, provider, modelId }));
}

const chatQueues = new Map<number, Promise<void>>();

// PUT /api/chats/{id}/settings writes the model and the thinking level of a chat together. As in
// saveAgent, the saves of one chat run one at a time on the saved settings.
export function saveChatSettings(chatId: number, change: (saved: ChatSettings) => ChatSettings) {
  const key = getChatQueryKey({ path: { chatId } });

  const run = (chatQueues.get(chatId) ?? Promise.resolve()).then(async () => {
    const chat = await queryClient.fetchQuery({ ...getChatOptions({ path: { chatId } }), staleTime: 0 });

    try {
      const { data } = await setChatSettings({
        path: { chatId },
        body: change(chat.settings),
        throwOnError: true,
      });

      queryClient.setQueryData(key, data);
    } catch (cause) {
      void queryClient.invalidateQueries({ queryKey: key });
      throw cause;
    }
  });

  chatQueues.set(
    chatId,
    run.catch(() => {}),
  );

  return run;
}

export type AgentModel = { label: string; ok: boolean; model?: Model };

export function findModel(provider: string, modelId: string, models: Model[] | undefined): AgentModel {
  if (!modelId) return { label: "Choose a model", ok: false };

  const model = models?.find((m) => m.provider === provider && m.id === modelId);

  if (model) return { label: model.name, ok: true, model };

  // Until the list loads, the saved ID is the best label; it is not a reason to block sending.
  return models ? { label: `${provider}/${modelId} · unavailable`, ok: false } : { label: modelId, ok: true };
}

export function agentModel(agent: Agent, models: Model[] | undefined): AgentModel {
  return findModel(agent.provider, agent.modelId, models);
}

export function useAgentModel(settings: Settings | undefined) {
  const models = useQuery(listModelsOptions());

  return settings ? agentModel(settings.agent, models.data?.models) : undefined;
}

/** The settings that a chat runs with: each setting of the chat, or else the agent's. */
export function chatModel(agent: Agent, chat: ChatSettings, models: Model[] | undefined) {
  return {
    model: chat.modelId ? findModel(chat.provider ?? "", chat.modelId, models) : agentModel(agent, models),
    thinkingLevel: chat.thinkingLevel ?? agent.thinkingLevel,
    overrides: !!chat.modelId || !!chat.thinkingLevel,
  };
}

export const thinkingNames: Record<ThinkingLevel, string> = {
  off: "Off",
  minimal: "Minimal",
  low: "Low",
  medium: "Medium",
  high: "High",
  xhigh: "Extra high",
  max: "Max",
};
