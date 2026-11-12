import type { Item, ToolItem } from "../api/generated/types.gen";
import { arg } from "./tools";

/** A child session. A missing agentId means the bot of the open page. */
export type ThreadRef = { chatId: number; agentId?: string };

/**
 * A session tool call as the transcript shows it. A card is a child that the call created.
 * A send is a message to a child that already exists.
 */
export type ThreadCall = {
  kind: "card" | "send";
  title: string;
  /** The bot that owns the child. Set when the result names it. */
  bot?: { id: string; name: string };
  thread?: ThreadRef;
  /** The ID of a task from before child sessions. The API finds its session. */
  turn?: string;
  status: ToolItem["status"];
  /** What the failed call returned. */
  error?: string;
};

const creates = ["session_new", "session_fork", "spawn_task"];

// The fields of a session tool result (engine.SessionRef) that the transcript uses.
type SessionResult = { id?: string; title?: string; bot?: string; botId?: string };

function parse(output: string): SessionResult {
  try {
    return JSON.parse(output) ?? {};
  } catch {
    return {};
  }
}

/** The thread entry of a tool call, or undefined for any other tool. */
export function threadCall(tool: ToolItem): ThreadCall | undefined {
  const send = tool.name === "session_send";

  if (!send && !creates.includes(tool.name)) return undefined;

  const done: SessionResult = tool.status === "done" ? parse(tool.output) : {};
  // session_new returns the new ID. session_send already holds the ID of its child in args.
  const id = send ? arg(tool, "id") : (done.id ?? "");
  const chatId = /^\d+$/.test(id) ? Number(id) : 0;
  const botId = done.botId ?? "";

  return {
    kind: send ? "send" : "card",
    title: done.title || arg(tool, "title") || (send ? "session" : "New session"),
    bot: botId ? { id: botId, name: done.bot || botId } : undefined,
    thread: chatId ? { chatId, agentId: botId || undefined } : undefined,
    turn: tool.name === "spawn_task" && id && !chatId ? id : undefined,
    status: tool.status,
    error: tool.status === "error" ? tool.output : undefined,
  };
}

export type ThreadState = "running" | "idle" | "done" | "failed" | "interrupted" | "stopped";

const interrupted = ["interrupted", "orphaned", "restart_limit"];

/** busy comes from the chat. The items are the committed history of the child. */
export function threadState(busy: boolean, items: Item[]): ThreadState {
  if (busy) return "running";

  const last = items.findLast((item) => ["user", "assistant", "error"].includes(item.kind));

  if (last?.kind === "assistant") return "done";

  if (last?.kind === "error")
    return last.aborted ? "stopped" : interrupted.includes(last.failure ?? "") ? "interrupted" : "failed";

  return "idle";
}

/** The number of messages the child received, and the first two lines of its latest answer. */
export function threadSummary(items: Item[]) {
  const answer = items.findLast((item) => item.kind === "assistant" && item.text.trim());

  return {
    turns: items.filter((item) => item.kind === "user" && !item.queued).length,
    preview:
      answer?.kind === "assistant"
        ? answer.text
            .split("\n")
            .filter((line) => line.trim())
            .slice(0, 2)
            .join("\n")
        : "",
  };
}
