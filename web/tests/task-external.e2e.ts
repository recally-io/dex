import { test } from "@e2e-dev/web";
import { expect } from "e2e";
import { createClient } from "../src/api/generated/client";
import {
  attachTaskExecution,
  createTask,
  createRunner,
  deleteRunner,
  issueRunnerCode,
  updateTask,
} from "../src/api/generated/sdk.gen";

test("shows an external run on its Task and registered Runner", async ({ app, browser, screen }) => {
  await app.open("/board");

  const origin = new URL(await browser.url());

  if (origin.hostname !== "127.0.0.1") throw new Error("Use the local test server");

  const client = createClient({ baseUrl: origin.origin });

  const { data: task } = await createTask({
    client,
    body: { title: "Check the external build", assigneeBotId: "default" },
    throwOnError: true,
  });

  const name = "External build Runner";

  const { data: runner } = await createRunner({
    client,
    body: { kind: "runner", name },
    throwOnError: true,
  });

  try {
    const { data: enrollment } = await issueRunnerCode({
      client,
      path: { runnerId: runner.id },
      throwOnError: true,
    });

    const pair = await fetch(new URL(`/machine/${runner.id}/pair`, origin), {
      method: "POST",
      headers: { Authorization: `Bearer ${enrollment.code}` },
    });

    if (!pair.ok) throw new Error(`Runner enrollment failed: ${pair.status}`);

    await pair.body?.cancel();

    const { data: issue } = await attachTaskExecution({
      client,
      path: { taskKey: task.key },
      body: {
        harness: "codex",
        runnerId: runner.id,
        hostMeta: { hostname: "worker-reported" },
        workspacePath: "/code/build",
        gitBranch: "release-topic",
      },
      throwOnError: true,
    });

    await app.open(`/board/${task.key}`);

    const runs = screen.getByRole("region", "External runs");
    await expect(runs.getByText(`Registered Runner: ${name}`)).toBeVisible();
    await expect(runs.getByText(/Reported host: worker-reported.*\/code\/build/)).toBeVisible();
    await expect(runs.getByText("release-topic")).toBeVisible();
    await expect(runs.getByText(/Heartbeat/)).toBeVisible();
    await expect(runs.getByText("running", { exact: true })).toBeVisible();

    await app.open("/settings/runners");
    await expect(screen.getByRole("link", task.key, { exact: true })).toBeVisible();
    await screen.getByRole("link", task.key, { exact: true }).tap();
    await expect(screen.getByRole("heading", task.title)).toBeVisible();

    const finish = await fetch(new URL("/api/tasks/external/finish", origin), {
      method: "POST",
      headers: { Authorization: `Bearer ${issue.taskToken}`, "Content-Type": "application/json" },
      body: JSON.stringify({ outcome: "completed" }),
    });

    if (!finish.ok) throw new Error(`External run finish failed: ${finish.status}`);

    await finish.body?.cancel();
    await expect(runs.getByText("completed", { exact: true })).toBeVisible();
  } finally {
    await updateTask({
      client,
      path: { taskKey: task.key },
      body: { status: "cancelled" },
      throwOnError: true,
    });
    await deleteRunner({ client, path: { runnerId: runner.id }, throwOnError: true });
  }
});
