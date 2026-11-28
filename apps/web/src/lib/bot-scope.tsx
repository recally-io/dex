import type { Bot, Settings } from "../api/generated/types.gen";
import { createContext, useContext } from "react";
import { useQuery } from "@tanstack/react-query";
import { getBotOptions, getSettingsOptions } from "../api/generated/@tanstack/react-query.gen";

export type BotSettings = Settings & { bot: Bot };

export const BotContext = createContext("default");
export const useBotId = () => useContext(BotContext);

// Combine the two API resources without a second settings cache.
export function useBotSettings(botId: string) {
  const settings = useQuery(getSettingsOptions());
  const bot = useQuery(getBotOptions({ path: { botId } }));
  const refetch = () => Promise.all([settings.refetch(), bot.refetch()]);

  if (settings.isPending || bot.isPending)
    return { isPending: true, isError: false, data: undefined, error: null, refetch } as const;

  if (settings.isError || bot.isError)
    return {
      isPending: false,
      isError: true,
      data: undefined,
      error: settings.error ?? bot.error,
      refetch,
    } as const;

  return {
    isPending: false,
    isError: false,
    data: { ...settings.data, bot: bot.data },
    error: null,
    refetch,
  } as const;
}
