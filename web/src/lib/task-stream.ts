import { useEffect } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { checkSession } from "./session";

export async function invalidateTasks(client: QueryClient) {
  await Promise.all([
    client.invalidateQueries({ queryKey: [{ _id: "listTasks" }] }),
    client.invalidateQueries({ queryKey: [{ _id: "getTask" }] }),
    client.invalidateQueries({ queryKey: [{ _id: "listTaskExecutions" }] }),
  ]);
}

export function useTaskStream() {
  const client = useQueryClient();

  useEffect(() => {
    // Keep the same EventSource on disconnect. The browser sends Last-Event-ID on retry.
    const stream = new EventSource("/api/tasks/events");
    let timer: ReturnType<typeof setTimeout> | undefined;

    const refresh = () => {
      if (timer !== undefined) return;

      // One refresh is sufficient for a burst of catch-up events.
      timer = setTimeout(() => {
        timer = undefined;
        void invalidateTasks(client);
      }, 50);
    };

    stream.addEventListener("task_ready", refresh);
    stream.addEventListener("task_entry", refresh);
    stream.addEventListener("task_refresh", refresh);
    stream.onerror = checkSession;

    return () => {
      clearTimeout(timer);
      stream.close();
    };
  }, [client]);
}
