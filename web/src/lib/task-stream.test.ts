import { QueryClient } from "@tanstack/react-query";
import { expect, test } from "vite-plus/test";
import { invalidateTasks } from "./task-stream";

test("invalidates Board filters and Task keys without refreshing Inbox", async () => {
  const client = new QueryClient();
  const list = [{ _id: "listTasks", query: { botId: "default", blocked: true } }];
  const detail = [{ _id: "getTask", path: { taskKey: "T-42" } }];
  const inbox = [{ _id: "getInboxSummary" }];

  for (const key of [list, detail, inbox]) client.setQueryData(key, {});

  await invalidateTasks(client);

  expect(client.getQueryState(list)?.isInvalidated).toBe(true);
  expect(client.getQueryState(detail)?.isInvalidated).toBe(true);
  expect(client.getQueryState(inbox)?.isInvalidated).toBe(false);
});
