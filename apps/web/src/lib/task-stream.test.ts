import { QueryClient } from "@tanstack/react-query";
import { expect, test } from "vite-plus/test";
import { getBoardAttentionOptions } from "../api/generated/@tanstack/react-query.gen";
import { invalidateTasks } from "./task-stream";

test("refreshes filtered Task lists, Task detail, and Board attention together", async () => {
  const client = new QueryClient();
  const list = [{ _id: "listTasks", query: { botId: "default", blocked: true } }];
  const detail = [{ _id: "getTask", path: { taskKey: "T-42" } }];
  const attention = getBoardAttentionOptions().queryKey;

  for (const key of [list, detail, attention]) client.setQueryData(key, {});

  await invalidateTasks(client);

  expect(client.getQueryState(list)?.isInvalidated).toBe(true);
  expect(client.getQueryState(detail)?.isInvalidated).toBe(true);
  expect(client.getQueryState(attention)?.isInvalidated).toBe(true);
});
