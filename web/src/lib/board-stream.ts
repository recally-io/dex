import { useEffect } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { checkSession } from "./session";

export async function invalidateBoard(client: QueryClient) {
  await Promise.all([
    client.invalidateQueries({ queryKey: [{ _id: "listBoardTasks" }] }),
    client.invalidateQueries({ queryKey: [{ _id: "getBoardTask" }] }),
    client.invalidateQueries({ queryKey: [{ _id: "listBoardExecutions" }] }),
  ]);
}

export function useBoardStream() {
  const client = useQueryClient();

  useEffect(() => {
    // Keep the same EventSource on disconnect. The browser sends Last-Event-ID on retry.
    const stream = new EventSource("/api/board/events");
    let timer: ReturnType<typeof setTimeout> | undefined;

    const refresh = () => {
      if (timer !== undefined) return;

      // One refresh is sufficient for a burst of catch-up events.
      timer = setTimeout(() => {
        timer = undefined;
        void invalidateBoard(client);
      }, 50);
    };

    stream.addEventListener("board_ready", refresh);
    stream.addEventListener("board_entry", refresh);
    stream.addEventListener("board_refresh", refresh);
    stream.onerror = checkSession;

    return () => {
      clearTimeout(timer);
      stream.close();
    };
  }, [client]);
}
