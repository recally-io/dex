// Tool arguments arrive as JSON values of any type.
/* eslint-disable anti-slop/no-runtime-typeof */
import type { Item, ToolItem } from "../api/generated/types.gen";

export type ContentBlock =
  | { kind: "tools"; tools: ToolItem[]; key: string }
  | { kind: "item"; item: Exclude<Item, { kind: "tool" | "task" }>; index: number };

type TaskBlock = { kind: "task"; item: Extract<Item, { kind: "task" }>; blocks: ContentBlock[] };

export type Block = ContentBlock | TaskBlock;

// A result owns the items up to the next input. Keep original indices for Retry and streaming.
export function blocks(items: Item[]): Block[] {
  const out: Block[] = [];
  let task: TaskBlock | undefined;

  items.forEach((item, index) => {
    if (item.kind === "task") {
      task = { kind: "task", item, blocks: [] };
      out.push(task);

      return;
    }

    if (item.kind === "user") task = undefined;
    const target = task?.blocks ?? out;
    const last = target.at(-1);

    if (item.kind !== "tool") target.push({ kind: "item", item, index });
    else if (last?.kind === "tools" && item.name !== "spawn_task" && last.tools[0].name !== "spawn_task")
      last.tools.push(item);
    else target.push({ kind: "tools", tools: [item], key: item.id });
  });

  return out;
}

// A string argument of a tool call, or an empty string when the call has no such string.
const arg = (tool: ToolItem, key: string) => {
  const value = tool.args[key];

  return typeof value === "string" ? value : "";
};

// The memory folder is <data dir>/memory. Without the data dir, no call counts as a memory call.
function inMemory(path: string, memory?: string) {
  return !!memory && (path === memory || path.startsWith(`${memory}/`));
}

// A memory search is a bash call that names the memory folder.
export function touchesMemory(tool: ToolItem, memory?: string) {
  return inMemory(arg(tool, "path"), memory) || (!!memory && arg(tool, "command").includes(memory));
}

// The verb and the main argument of a step row. dex sends only the tool name and its arguments.
export function describeTool(tool: ToolItem, memory?: string) {
  const path = arg(tool, "path");

  if (tool.name === "spawn_task")
    return {
      verb:
        tool.status === "running"
          ? "Sending to background"
          : tool.status === "error"
            ? "Could not send to background"
            : "Sent to background",
      target: arg(tool, "title"),
    };

  if (tool.name === "bash") {
    const command = arg(tool, "command");

    return memory && command.includes(memory)
      ? { verb: "Search memory", target: command.replaceAll(memory, "memory") }
      : { verb: "Run", target: command };
  }

  if (path) {
    const verb = tool.name.charAt(0).toUpperCase() + tool.name.slice(1);

    return memory && inMemory(path, memory)
      ? { verb: `${verb} memory`, target: path.slice(memory.length + 1) || "memory" }
      : { verb, target: path };
  }

  return { verb: tool.name, target: JSON.stringify(tool.args) };
}

const pastTense = new Map([
  ["Search", "Searched"],
  ["Edit", "Edited"],
  ["Write", "Wrote"],
]);

// The header of a step: the text while a call runs, and the text after the step ends.
export function stepTitle(tools: ToolItem[], memory?: string) {
  const running = tools.find((t) => t.status === "running");
  const failed = tools.filter((t) => t.status === "error").length;
  const n = tools.length;
  let done: string;

  if (tools.every((t) => touchesMemory(t, memory)))
    done =
      n === 1
        ? describeTool(tools[0], memory).verb.replace(/^\w+/, (verb) => pastTense.get(verb) ?? verb)
        : `Used memory ${n} times`;
  else if (n === 1) done = tools[0].name === "bash" ? "Ran a command" : `Used ${tools[0].name}`;
  else done = `Ran ${n} tools`;

  return {
    active: running ? `Running ${running.name}` : done,
    done: failed ? `${done} · ${failed} failed` : done,
  };
}

export const seconds = (ms: number) => (ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`);
