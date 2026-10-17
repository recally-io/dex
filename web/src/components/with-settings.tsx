import { scopedSettingsOptions, useAgentId } from "../lib/agent-scope";
import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import type { Settings } from "../api/generated/types.gen";
import { Failure, Loading } from "./feedback";

// Shows the page only with the saved settings; it has no fallback data.
export function WithSettings({ children }: { children: (settings: Settings) => ReactNode }) {
  const agentId = useAgentId();
  const settings = useQuery(scopedSettingsOptions(agentId));

  return settings.isPending ? (
    <Loading />
  ) : settings.isError ? (
    <div className="px-5 py-8 sm:px-8">
      <Failure title="Could not load settings" error={settings.error} retry={() => void settings.refetch()} />
    </div>
  ) : (
    children(settings.data)
  );
}
