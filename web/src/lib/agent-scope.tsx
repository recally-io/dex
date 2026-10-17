import type { Agent, Settings } from "../api/generated/types.gen";
import { createContext, useContext } from "react";
import { useQuery } from "@tanstack/react-query";
import { getAgentOptions, getSettingsOptions } from "../api/generated/@tanstack/react-query.gen";

export type AgentSettings = Settings & { agent: Agent };

export const AgentContext = createContext("default");
export const useAgentId = () => useContext(AgentContext);

// Combine the two API resources without a second settings cache.
export function useAgentSettings(agentId: string) {
  const settings = useQuery(getSettingsOptions());
  const agent = useQuery(getAgentOptions({ path: { agentId } }));
  const refetch = () => Promise.all([settings.refetch(), agent.refetch()]);

  if (settings.isPending || agent.isPending)
    return { isPending: true, isError: false, data: undefined, error: null, refetch } as const;

  if (settings.isError || agent.isError)
    return {
      isPending: false,
      isError: true,
      data: undefined,
      error: settings.error ?? agent.error,
      refetch,
    } as const;

  return {
    isPending: false,
    isError: false,
    data: { ...settings.data, agent: agent.data },
    error: null,
    refetch,
  } as const;
}
