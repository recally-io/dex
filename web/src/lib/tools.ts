// Tool arguments arrive as JSON values of any type.
/* eslint-disable anti-slop/no-runtime-typeof */
import type { Item, ToolItem } from "../api/generated/types.gen";

type ThinkingItem = Extract<Item, { kind: "thinking" }>;

/** A thinking item or a tool call. Consecutive steps of one answer show as one collapsed row. */
export type Step = ThinkingItem | ToolItem;

export type ContentBlock =
  | { kind: "steps"; steps: Step[]; key: string; last: number }
  | { kind: "item"; item: Exclude<Item, { kind: "tool" | "task" | "thinking" }>; index: number };

type TaskBlock = { kind: "task"; item: Extract<Item, { kind: "task" }>; blocks: ContentBlock[] };

type Block = ContentBlock | TaskBlock;

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

    if (item.kind === "user" || item.kind === "boundary") task = undefined;
    const target = task?.blocks ?? out;
    const last = target.at(-1);

    if (item.kind !== "tool" && item.kind !== "thinking") target.push({ kind: "item", item, index });
    else if (last?.kind === "steps") {
      last.steps.push(item);
      last.last = index;
    } else target.push({ kind: "steps", steps: [item], key: item.id, last: index });
  });

  return out;
}

// A string argument of a tool call, or an empty string when the call has no such string.
export const arg = (tool: ToolItem, key: string) => {
  const value = tool.args[key];

  return typeof value === "string" ? value : "";
};

export function isParentReport(tool: ToolItem) {
  return tool.name === "session_report";
}

// The memory folder is <data dir>/memory. Without the data dir, no call counts as a memory call.
function inMemory(path: string, memory?: string) {
  return !!memory && (path === memory || path.startsWith(`${memory}/`));
}

// A memory search is a bash call that names the memory folder.
function touchesMemory(tool: ToolItem, memory?: string) {
  return inMemory(arg(tool, "path"), memory) || (!!memory && arg(tool, "command").includes(memory));
}

const taskSteps = new Map([
  ["task_read", { verb: "Read Task", done: "Read Task" }],
  ["task_post", { verb: "Post to Task", done: "Posted to Task" }],
  ["task_review", { verb: "Review Task", done: "Reviewed Task" }],
]);

// The verb and the main argument of a step row. dex sends only the tool name and its arguments.
export function describeTool(tool: ToolItem, memory?: string) {
  const path = arg(tool, "path");

  if (tool.name === "bash") {
    const command = arg(tool, "command");

    return memory && command.includes(memory)
      ? { verb: "Search memory", target: command.replaceAll(memory, "memory") }
      : { verb: "Run", target: command };
  }

  if (tool.name === "notify_user") {
    return { verb: "Notify user", target: arg(tool, "topicKey") || arg(tool, "content") };
  }

  if (isParentReport(tool)) {
    return { verb: "Report parent", target: arg(tool, "content") };
  }

  if (tool.name === "process_start") {
    return { verb: "Start process", target: arg(tool, "label") || arg(tool, "command") };
  }

  if (tool.name === "process_list") {
    return { verb: "List processes", target: "" };
  }

  if (tool.name === "process_stop") {
    return { verb: "Stop process", target: arg(tool, "id") };
  }

  if (tool.name === "monitor_set") {
    return { verb: "Schedule monitor", target: arg(tool, "at") || arg(tool, "prompt") };
  }

  if (tool.name === "monitor_list") {
    return { verb: "List monitors", target: "" };
  }

  if (tool.name === "monitor_cancel") {
    return { verb: "Cancel monitor", target: arg(tool, "id") };
  }

  const taskStep = taskSteps.get(tool.name);

  if (taskStep) return { verb: taskStep.verb, target: arg(tool, "id") };

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

/**
 * The header of a steps row. active is set while a call runs or the model thinks; done is the
 * summary after the steps end. thinking is true while the last step is a thought that streams.
 */
export function stepTitle(steps: Step[], memory?: string, thinking = false) {
  const tools = steps.filter((step) => step.kind === "tool");
  const running = tools.find((t) => t.status === "running");
  const failed = tools.filter((t) => t.status === "error").length;
  const reports = tools.filter((t) => t.status === "done" && isParentReport(t)).length;
  const n = tools.length;
  let done = "";

  if (reports) {
    done = reports === 1 ? "Reported to parent" : `Reported to parent ${reports} times`;
    const other = n - reports;

    if (other) done += ` · Ran ${other} ${other === 1 ? "tool" : "tools"}`;
  } else if (n && tools.every((t) => touchesMemory(t, memory)))
    done =
      n === 1
        ? describeTool(tools[0], memory).verb.replace(/^\w+/, (verb) => pastTense.get(verb) ?? verb)
        : `Used memory ${n} times`;
  else if (n === 1)
    done =
      taskSteps.get(tools[0].name)?.done ??
      (tools[0].name === "bash" ? "Ran a command" : `Used ${tools[0].name}`);
  else if (n) done = `Ran ${n} tools`;

  if (steps.some((step) => step.kind === "thinking"))
    done = done ? `Thought process · ${done}` : "Thought process";

  return {
    active: running
      ? (taskSteps.get(running.name)?.verb ?? `Running ${running.name}`)
      : thinking
        ? "Thinking"
        : undefined,
    done: failed ? `${done} · ${failed} failed` : done,
  };
}

export const seconds = (ms: number) => (ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`);
