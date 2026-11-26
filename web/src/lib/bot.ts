import type { BotSettings as Settings } from "./bot-scope";
import { useQuery } from "@tanstack/react-query";
import type { Bot, Model, ThinkingLevel } from "../api/generated/types.gen";
import { updateBot } from "../api/generated/sdk.gen";
import {
  getBotOptions,
  getBotQueryKey,
  listBotsQueryKey,
  listModelsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { queryClient } from "./queries";

let queue: Promise<void> = Promise.resolve();

// PUT /api/bots/{botId} writes the name, model, thinking level and instructions together.
// Saves run one at a time, and each reads the saved bot just before it writes, so a model change
// keeps the saved instructions and an instructions save keeps the saved model.
export function saveBot(botId: string, change: (saved: Bot) => Bot): Promise<void> {
  const run = queue.then(async () => {
    const bot = await queryClient.fetchQuery({ ...getBotOptions({ path: { botId } }), staleTime: 0 });

    try {
      await updateBot({ path: { botId }, body: change(bot), throwOnError: true });
    } catch (cause) {
      void refreshBot(botId);
      throw cause;
    }

    await refreshBot(botId);
  });

  queue = run.catch(() => {});

  return run;
}

export function saveModel(botId: string, provider: string, modelId: string) {
  return saveBot(botId, (saved) => ({ ...saved, provider, modelId }));
}

type BotModel = { label: string; ok: boolean; model?: Model };

export function findModel(provider: string, modelId: string, models: Model[] | undefined): BotModel {
  if (!modelId) return { label: "Choose a model", ok: false };

  const model = models?.find((m) => m.provider === provider && m.id === modelId);

  if (model) return { label: model.name, ok: true, model };

  // Until the list loads, the saved ID is the best label; it is not a reason to block sending.
  return models ? { label: `${provider}/${modelId} · unavailable`, ok: false } : { label: modelId, ok: true };
}

export function botModel(bot: Bot, models: Model[] | undefined): BotModel {
  return findModel(bot.provider, bot.modelId, models);
}

export function useBotModel(settings: Settings | undefined) {
  const models = useQuery(listModelsOptions());

  return settings ? botModel(settings.bot, models.data?.models) : undefined;
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

async function refreshBot(botId: string) {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: getBotQueryKey({ path: { botId } }) }),
    queryClient.invalidateQueries({ queryKey: listBotsQueryKey() }),
  ]);
}
