import { useMutation } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { createChatMutation } from "../api/generated/@tanstack/react-query.gen";
import { useAgentId } from "../lib/agent-scope";
import { useApp } from "../lib/app-state";
import { errorText, refreshChats } from "../lib/queries";

/** Opens a Chat with its own context. The server reuses the newest empty Chat. */
export function useNewChat() {
  const agentId = useAgentId();
  const navigate = useNavigate();
  const { notify } = useApp();

  return useMutation({
    ...createChatMutation(),
    onSuccess: async (chat) => {
      await refreshChats(agentId);
      await navigate({ to: "/a/$agentId/c/$chatId", params: { agentId, chatId: String(chat.id) } });
    },
    onError: (cause) => notify(`Could not open a new chat. ${errorText(cause)}`),
  });
}

export const row =
  "h-8 min-w-0 justify-start gap-2 rounded-[9px] border-0 px-2 text-[13px] font-normal text-muted-foreground hover:bg-accent hover:text-foreground aria-[current=page]:bg-card aria-[current=page]:text-foreground aria-[current=page]:shadow-[0_0_0_1px_var(--border),0_1px_2px_rgb(0_0_0/0.06)]";
