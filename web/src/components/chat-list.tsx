import { useMutation } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { createChat, setChatProject } from "../api/generated/sdk.gen";
import type { Chat } from "../api/generated/types.gen";
import { useAgentId } from "../lib/agent-scope";
import { useApp } from "../lib/app-state";
import { errorText, refreshChats } from "../lib/queries";

/** Opens a task with the current project. The server reuses the newest empty task. */
export function useNewChat(cwd: string, created: (chat: Chat) => void) {
  const agentId = useAgentId();
  const navigate = useNavigate();
  const { notify } = useApp();

  return useMutation({
    mutationFn: async () => {
      const { data: chat } = await createChat({ path: { agentId }, throwOnError: true });
      await setChatProject({ path: { agentId, chatId: chat.id }, body: { cwd }, throwOnError: true });

      return { ...chat, cwd };
    },
    onSuccess: async (chat) => {
      await refreshChats(agentId);
      await navigate({ to: "/a/$agentId/c/$chatId", params: { agentId, chatId: String(chat.id) } });
      created(chat);
    },
    onError: (cause) => notify(`Could not open a new task. ${errorText(cause)}`),
  });
}

export const row =
  "h-8 min-w-0 justify-start gap-2 rounded-[9px] border-0 px-2 text-[13px] font-normal text-muted-foreground hover:bg-accent hover:text-foreground aria-[current=page]:bg-card aria-[current=page]:text-foreground aria-[current=page]:shadow-[0_0_0_1px_var(--border),0_1px_2px_rgb(0_0_0/0.06)]";
