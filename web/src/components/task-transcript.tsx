import { useEffect, useState } from "react";
import type { ChatEvent } from "../api/generated/types.gen";
import { applyChatEvent, emptyChatState } from "../lib/chat-state";
import { blocks } from "../lib/tools";
import { ToolStep, TranscriptItem } from "./agent/transcript";

export function TaskTranscript({
  chatId,
  taskId,
  stageId,
}: {
  chatId: number;
  taskId: string;
  stageId: string;
}) {
  const [state, setState] = useState(emptyChatState);
  const [error, setError] = useState("");
  const [ready, setReady] = useState(false);

  useEffect(() => {
    let snapshotSeen = false;

    const source = new EventSource(
      `/api/chats/${chatId}/tasks/${encodeURIComponent(taskId)}/stages/${encodeURIComponent(stageId)}/events`,
    );

    const receive = (message: MessageEvent<string>) => {
      try {
        const event: ChatEvent = JSON.parse(message.data);

        if (!["snapshot", "item", "append", "run"].includes(event.type))
          throw new Error("Unknown stage event.");

        if (!snapshotSeen && event.type !== "snapshot") throw new Error("Missing stage snapshot.");

        if (event.type === "snapshot") snapshotSeen = true;
        setState((previous) => applyChatEvent(previous, event));
        setReady(true);
        setError("");
      } catch {
        source.close();
        setError("The stage stream returned an invalid event. Close and reopen the transcript to reconnect.");
      }
    };

    source.onopen = () => {
      snapshotSeen = false;
    };

    for (const type of ["snapshot", "item", "append", "run"]) source.addEventListener(type, receive);
    source.onerror = () => setError("The stage stream disconnected. Reconnecting when available.");

    return () => source.close();
  }, [chatId, taskId, stageId]);

  return (
    <div aria-label="Read-only stage transcript" className="space-y-4 pt-3">
      {error ? (
        <p role="alert" className="text-[13px] text-red">
          {error}
        </p>
      ) : null}
      {!ready && !error ? (
        <p role="status" className="text-[12px] text-ink-3">
          Loading execution transcript…
        </p>
      ) : null}
      {ready && !state.items.length ? (
        <p className="text-[12px] text-ink-3">No execution events yet.</p>
      ) : null}
      {blocks(state.items).map((block) =>
        block.kind === "tools" ? (
          <ToolStep key={block.key} tools={block.tools} />
        ) : (
          <TranscriptItem
            key={block.item.id}
            item={block.item}
            live={state.running && block.index === state.items.length - 1}
            retry={() => {}}
            canRetry={false}
          />
        ),
      )}
    </div>
  );
}
