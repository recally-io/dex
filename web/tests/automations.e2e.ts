import { describe, test } from "@e2e-dev/web";
import { expect } from "e2e";
import { createClient } from "../src/api/generated/client";
import { createAgent, createAutomation, runAutomation } from "../src/api/generated/sdk.gen";

describe("Routine run history", () => {
  test("offers the Outbox on a read-only Routine transcript", async ({ app, browser, screen }) => {
    await app.open("/");
    const origin = new URL(await browser.url());

    if (origin.hostname !== "127.0.0.1") throw new Error("Use the local test server");

    const client = createClient({ baseUrl: origin.origin });

    const { data: agent } = await createAgent({
      client,
      body: { name: "Routine Outbox fixture", provider: "faux", modelId: "echo" },
      throwOnError: true,
    });

    const { data: routine } = await createAutomation({
      client,
      path: { agentId: agent.id },
      body: {
        name: "Read-only Outbox fixture",
        prompt: "Routine transcript",
        schedule: { kind: "daily", time: "09:00" },
        timeZone: "UTC",
        session: "new",
        enabled: false,
        notification: { mode: "none" },
      },
      throwOnError: true,
    });

    const { data: run } = await runAutomation({
      client,
      path: { agentId: agent.id, automationId: routine.id },
      throwOnError: true,
    });

    expect(run.chatId).toBeDefined();
    await app.open(`/a/${agent.id}/c/${run.chatId}`);
    await screen.getByRole("button", "Monitors and outbox").tap();
    await expect(screen.getByText("No outbound messages yet.")).toBeVisible();
    await expect(screen.getByRole("textbox", "Message Dex")).toHaveCount(0);
  });

  test("shows when a skipped run has no notification target", async ({ app, browser, screen }) => {
    await app.open("/");
    const origin = new URL(await browser.url());

    if (origin.hostname !== "127.0.0.1") throw new Error("Use the local test server");

    const client = createClient({ baseUrl: origin.origin });

    // Empty fields prevent the fixture from inheriting a model or a runner.
    const { data: agent } = await createAgent({
      client,
      body: { name: "Routine fixture", provider: "", modelId: "", environmentId: "local" },
      throwOnError: true,
    });

    const { data: routine } = await createAutomation({
      client,
      path: { agentId: agent.id },
      body: {
        name: "Routine failure fixture",
        prompt: "Check",
        schedule: { kind: "daily", time: "09:00" },
        timeZone: "UTC",
        session: "new",
        enabled: false,
        notification: { mode: "default" },
      },
      throwOnError: true,
    });

    const { data: run } = await runAutomation({
      client,
      path: { agentId: agent.id, automationId: routine.id },
      throwOnError: true,
    });
    expect(run.status).toBe("skipped");
    expect(run.reason).toBe("the bot has no model");

    await app.open(`/a/${agent.id}/automations/${routine.id}`);

    await expect(screen.getByText("the bot has no model", { exact: true })).toBeVisible();
    await expect(screen.getByText("not delivered: no notification target", { exact: true })).toBeVisible();
  });
});
