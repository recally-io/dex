import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, test } from "vite-plus/test";
import type { Item, ToolItem } from "../../api/generated/types.gen";
import { blocks } from "../../lib/tools";
import { ToolStep } from "./transcript";

const tool = (id: string, status: ToolItem["status"], output: string): ToolItem => ({
  kind: "tool",
  id,
  name: "bash",
  args: { command: `printf ${output}` },
  output,
  status,
  durationMs: 1200,
});

test("consecutive tool calls make one step", () => {
  const first = [tool("t1", "done", "one"), tool("t2", "done", "two")];

  const items: Item[] = [
    { kind: "user", id: "u", text: "hi" },
    ...first,
    { kind: "assistant", id: "a", text: "ok" },
    tool("t3", "done", "three"),
  ];

  expect(blocks(items).map((b) => (b.kind === "tools" ? b.tools.map((t) => t.id) : b.item.id))).toEqual([
    "u",
    ["t1", "t2"],
    "a",
    ["t3"],
  ]);

  // A finished step is closed. Its header counts the calls.
  const html = renderToStaticMarkup(createElement(ToolStep, { tools: first }));
  expect(html).toContain("Ran 2 tools");
  expect(html).toContain('aria-expanded="false"');
  expect(html).not.toContain("printf one");
});

test("user sees the failed call of a step, with its command, time and output", () => {
  const html = renderToStaticMarkup(
    createElement(ToolStep, { tools: [tool("t1", "done", "one"), tool("t2", "error", "boom")] }),
  );

  expect(html).toContain("Ran 2 tools · 1 failed");
  expect(html).toContain("printf one");
  expect(html).toContain("printf boom");
  expect(html).toContain("1.2 s");
  expect(html).toContain(">boom</pre>");
  expect(html).not.toContain(">one</pre>");
});

test("memory calls read as memory use", () => {
  const memory = "/data/memory";
  const read: ToolItem = { ...tool("t1", "done", ""), name: "read", args: { path: `${memory}/MEMORY.md` } };
  const html = renderToStaticMarkup(createElement(ToolStep, { tools: [read], memory }));

  expect(html).toContain("Read memory");
});
