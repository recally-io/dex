import { createContext, useContext } from "react";
import { queryOptions } from "@tanstack/react-query";
import { getAgentOptions, getSettingsOptions } from "../api/generated/@tanstack/react-query.gen";
import { queryClient } from "./queries";

export const AgentContext = createContext("default");
export const useAgentId = () => useContext(AgentContext);

// Global settings and the selected agent have separate API resources.
export function scopedSettingsOptions(agentId: string) {
  return queryOptions({
    queryKey: [{ _id: "getSettings", path: { agentId } }],
    queryFn: async () => {
      const [settings, agent] = await Promise.all([
        queryClient.fetchQuery(getSettingsOptions()),
        queryClient.fetchQuery(getAgentOptions({ path: { agentId } })),
      ]);

      return { ...settings, agent };
    },
  });
}
