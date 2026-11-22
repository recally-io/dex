import { useMutation, useMutationState, useQuery } from "@tanstack/react-query";
import {
  listAgentEnvironmentsOptions,
  setChatEnvironmentMutation,
} from "../api/generated/@tanstack/react-query.gen";
import type { Chat } from "../api/generated/types.gen";
import { useAgentId } from "../lib/agent-scope";
import { errorText, refreshChats } from "../lib/queries";
import { Select } from "./form";

const saveKey = ["setChatEnvironment"];

// The first message must not run before the selection is saved: it would run in the default
// environment, and the selection would then fail with 409. This returns why sending waits, from
// the latest selection of the chat.
export function useEnvironmentSendBlock(chatId: number | undefined): string | undefined {
  const states = useMutationState({
    filters: { mutationKey: saveKey },
    select: (m) => {
      // SAFETY: only ChatEnvironment uses saveKey, and it passes the setChatEnvironment options.
      const variables = m.state.variables as { path: { chatId: number } } | undefined;

      return { chatId: variables?.path.chatId, status: m.state.status };
    },
  });

  const latest = states.filter((s) => s.chatId === chatId).at(-1);

  if (latest?.status === "pending") return "Saving the environment…";

  if (latest?.status === "error") return "The environment was not saved. Choose it again before you send.";

  return undefined;
}

// A chat picks its environment before the first message. After that the name is read-only.
export function ChatEnvironment({ chat, started }: { chat: Chat; started: boolean }) {
  const agentId = useAgentId();
  const environments = useQuery(listAgentEnvironmentsOptions({ path: { agentId } }));
  const list = environments.data?.environments ?? [];
  const own = list.find((env) => env.default);
  const selected = chat.environmentId ?? "";

  const save = useMutation({
    ...setChatEnvironmentMutation(),
    mutationKey: saveKey,
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

  if (!environments.data || (list.length < 2 && own?.id === "local" && !selected)) return null;

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
        {selected && !list.some((env) => env.id === selected) ? (
          <option value={selected} disabled>
            {selected} (removed)
          </option>
        ) : null}
        {list.map((env) => (
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
