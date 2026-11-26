import { useAgentId } from "./agent-scope";
import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { getSessionOptions } from "../api/generated/@tanstack/react-query.gen";
import type { SessionEvent, UserItem } from "../api/generated/types.gen";
import { applySessionEvent, emptySessionState } from "./session-state";
import { refreshSessions } from "./queries";
import { checkSession } from "./session";

// A reconnect replaces live items with a snapshot and keeps the loaded history prefix.
export function useSessionStream(sessionId: number) {
  const agentId = useAgentId();
  const queryClient = useQueryClient();
  const session = useQuery(getSessionOptions({ path: { agentId, sessionId } }));
  const [state, setState] = useState(emptySessionState);
  const [queued, setQueued] = useState<UserItem[]>([]);
  const [connection, setConnection] = useState<"connecting" | "ready" | "error">("connecting");
  const [streamError, setStreamError] = useState("");
  const reconnect = useRef<(() => void) | null>(null);

  useEffect(() => {
    if (!session.isSuccess) return;
    let source: EventSource;
    let snapshotSeen = false;

    function connect() {
      source?.close();
      snapshotSeen = false;
      setConnection("connecting");
      source = new EventSource(`/api/agents/${encodeURIComponent(agentId)}/sessions/${sessionId}/events`);
      source.onopen = () => {
        setConnection("connecting");
        setState((previous) => ({ ...previous, children: undefined }));
      };

      const types = ["snapshot", "item", "append", "run", "child", "notifications"];

      const receive = (message: MessageEvent<string>) => {
        try {
          const event: SessionEvent = JSON.parse(message.data);

          if (!types.includes(event.type)) throw new Error("Unknown session event.");

          if (!snapshotSeen && event.type !== "snapshot")
            throw new Error("The stream did not start with a snapshot.");

          if (event.type === "snapshot") snapshotSeen = true;

          if (event.type === "snapshot" || (event.type === "run" && !event.running))
            void queryClient.invalidateQueries({ queryKey: [{ _id: "listArtifacts", path: { agentId } }] });
          setState((previous) => applySessionEvent(previous, event));

          if (event.type === "snapshot")
            setQueued((previous) =>
              previous.filter((item) => !event.items.some((remote) => remote.id === item.id)),
            );

          if (event.type === "item")
            setQueued((previous) => previous.filter((item) => item.id !== event.item.id));
          setConnection("ready");
          setStreamError("");

          // The session read can precede a question that is already present at snapshot time.
          if (
            event.type === "snapshot" ||
            (event.type === "run" && !event.running) ||
            (event.type === "item" &&
              event.item.kind === "tool" &&
              event.item.name === "set_project" &&
              event.item.status !== "running")
          )
            void refreshSessions(agentId);
        } catch {
          source.close();
          setConnection("error");
          setStreamError("The session stream returned an invalid event. Reconnect to load a new snapshot.");
        }
      };

      for (const type of types) source.addEventListener(type, receive);
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
  }, [agentId, sessionId, session.isSuccess, queryClient]);

  return { session, state, setState, queued, setQueued, connection, streamError, reconnect };
}
