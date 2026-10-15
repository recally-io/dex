import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, test } from "vite-plus/test";
import { TranscriptItem } from "./transcript";

test("user sees the tool result status and can inspect its output", () => {
  const html = renderToStaticMarkup(
    createElement(TranscriptItem, {
      item: {
        kind: "tool",
        id: "tool-1",
        name: "bash",
        args: { command: "printf hello" },
        output: "hello",
        status: "done",
        durationMs: 1200,
      },
      live: false,
      retry: () => {},
      canRetry: false,
    }),
  );

  expect(html).toContain("Completed");
  expect(html).toContain("1.2 s");
  expect(html).toContain("printf hello");
  expect(html).toContain("hello");
});
