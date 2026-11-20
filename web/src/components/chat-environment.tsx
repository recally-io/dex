import { useMutation, useQuery } from "@tanstack/react-query";
import {
  listAgentEnvironmentsOptions,
  setChatEnvironmentMutation,
} from "../api/generated/@tanstack/react-query.gen";
import type { Chat } from "../api/generated/types.gen";
import { useAgentId } from "../lib/agent-scope";
import { errorText, refreshChats } from "../lib/queries";
import { Select } from "./form";

// A chat picks its environment before the first message. After that the name is read-only.
export function ChatEnvironment({ chat, started }: { chat: Chat; started: boolean }) {
  const agentId = useAgentId();
  const environments = useQuery(listAgentEnvironmentsOptions({ path: { agentId } }));
  const list = environments.data?.environments ?? [];
  const own = list.find((env) => env.default);
  const selected = chat.environmentId ?? "";

  const save = useMutation({
    ...setChatEnvironmentMutation(),
    onSuccess: () => refreshChats(agentId),
  });

  if (started || chat.readOnly) {
    const name = list.find((env) => env.id === selected)?.name;

    return selected ? (
      <span
        className="min-w-0 max-w-32 shrink truncate px-1.5 text-[12.5px] text-muted-foreground sm:max-w-40"
        title={`Environment: ${name ?? selected}`}
      >
        {name ?? selected}
      </span>
    ) : null;
  }

  if (list.length < 2) return null;

  return (
    <>
      <Select
        aria-label="Environment"
        className="h-8 w-auto max-w-40 min-w-0 shrink bg-transparent px-1.5 text-[12.5px] shadow-none"
        value={selected}
        disabled={save.isPending}
        onChange={(event) =>
          save.mutate({ path: { agentId, chatId: chat.id }, body: { environmentId: event.target.value } })
        }
      >
        <option value="">Follow bot ({own?.name ?? "default"})</option>
        {list
          .filter((env) => !env.default)
          .map((env) => (
            <option key={env.id} value={env.id} disabled={env.status !== "available"}>
              {env.name}
              {env.status === "available" ? "" : ` (${env.status})`}
            </option>
          ))}
      </Select>
      {save.isError ? (
        <span role="alert" className="truncate text-[12px] text-red">
          {errorText(save.error)}
        </span>
      ) : null}
    </>
  );
}
