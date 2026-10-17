import { useQuery } from "@tanstack/react-query";
import type { Agent, Model, Settings, ThinkingLevel } from "../api/generated/types.gen";
import { updateAgent } from "../api/generated/sdk.gen";
import {
  getAgentOptions,
  getAgentQueryKey,
  listAgentsQueryKey,
  listModelsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { queryClient } from "./queries";

let queue: Promise<void> = Promise.resolve();

// PUT /api/agents/{agentId} writes the name, model, thinking level and instructions together.
// Saves run one at a time, and each reads the saved agent just before it writes, so a model change
// keeps the saved instructions and an instructions save keeps the saved model.
export function saveAgent(agentId: string, change: (saved: Agent) => Agent): Promise<void> {
  const run = queue.then(async () => {
    const agent = await queryClient.fetchQuery({ ...getAgentOptions({ path: { agentId } }), staleTime: 0 });

    try {
      await updateAgent({ path: { agentId }, body: change(agent), throwOnError: true });
    } catch (cause) {
      void refreshAgent(agentId);
      throw cause;
    }

    await refreshAgent(agentId);
  });

  queue = run.catch(() => {});

  return run;
}

export function saveModel(agentId: string, provider: string, modelId: string) {
  return saveAgent(agentId, (saved) => ({ ...saved, provider, modelId }));
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

async function refreshAgent(agentId: string) {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: getAgentQueryKey({ path: { agentId } }) }),
    queryClient.invalidateQueries({ queryKey: [{ _id: "getSettings", path: { agentId } }] }),
    queryClient.invalidateQueries({ queryKey: listAgentsQueryKey() }),
  ]);
}
