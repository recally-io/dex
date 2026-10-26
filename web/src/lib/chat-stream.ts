import { useAgentId } from "./agent-scope";
import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { getChatOptions } from "../api/generated/@tanstack/react-query.gen";
import type { ChatEvent, UserItem } from "../api/generated/types.gen";
import { applyChatEvent, emptyChatState } from "./chat-state";
import { refreshChats } from "./queries";
import { checkSession } from "./session";

// A reconnect replaces live items with a snapshot and keeps the loaded history prefix.
export function useChatStream(chatId: number) {
  const agentId = useAgentId();
  const chat = useQuery(getChatOptions({ path: { agentId, chatId } }));
  const [state, setState] = useState(emptyChatState);
  const [queued, setQueued] = useState<UserItem[]>([]);
  const [connection, setConnection] = useState<"connecting" | "ready" | "error">("connecting");
  const [streamError, setStreamError] = useState("");
  const reconnect = useRef<(() => void) | null>(null);

  useEffect(() => {
    if (!chat.isSuccess) return;
    let source: EventSource;
    let snapshotSeen = false;

    function connect() {
      source?.close();
      snapshotSeen = false;
      setConnection("connecting");
      source = new EventSource(`/api/agents/${encodeURIComponent(agentId)}/chats/${chatId}/events`);
      source.onopen = () => setConnection("connecting");

      const receive = (message: MessageEvent<string>) => {
        try {
          const event: ChatEvent = JSON.parse(message.data);

          if (!["snapshot", "item", "append", "run"].includes(event.type))
            throw new Error("Unknown chat event.");

          if (!snapshotSeen && event.type !== "snapshot")
            throw new Error("The stream did not start with a snapshot.");

          if (event.type === "snapshot") snapshotSeen = true;
          setState((previous) => applyChatEvent(previous, event));

          if (event.type === "snapshot")
            setQueued((previous) =>
              previous.filter((item) => !event.items.some((remote) => remote.id === item.id)),
            );

          if (event.type === "item")
            setQueued((previous) => previous.filter((item) => item.id !== event.item.id));
          setConnection("ready");
          setStreamError("");

          if (
            (event.type === "run" && !event.running) ||
            (event.type === "item" &&
              event.item.kind === "tool" &&
              event.item.name === "set_project" &&
              event.item.status !== "running")
          )
            void refreshChats(agentId);
        } catch {
          source.close();
          setConnection("error");
          setStreamError("The chat stream returned an invalid event. Reconnect to load a new snapshot.");
        }
      };

      for (const type of ["snapshot", "item", "append", "run"]) source.addEventListener(type, receive);
      source.onerror = () => {
        setConnection("error");
        checkSession();
      };
    }

    reconnect.current = connect;
    connect();

    return () => {
      reconnect.current = null;
      source.close();
    };
  }, [agentId, chatId, chat.isSuccess]);

  return { chat, state, setState, queued, setQueued, connection, streamError, reconnect };
}
