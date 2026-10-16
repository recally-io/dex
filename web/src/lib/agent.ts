import { useQuery } from "@tanstack/react-query";
import type { Agent, Model, Settings, ThinkingLevel } from "../api/generated/types.gen";
import { setAgent } from "../api/generated/sdk.gen";
import { getSettingsOptions, listModelsOptions } from "../api/generated/@tanstack/react-query.gen";
import { queryClient, refreshSettings } from "./queries";

let queue: Promise<void> = Promise.resolve();

// PUT /api/settings/agent writes the name, model, thinking level and instructions together.
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

export const thinkingNames: Record<ThinkingLevel, string> = {
  off: "Off",
  minimal: "Minimal",
  low: "Low",
  medium: "Medium",
  high: "High",
  xhigh: "Extra high",
  max: "Max",
};
