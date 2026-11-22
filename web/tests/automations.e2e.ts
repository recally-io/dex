import { describe, test } from "@e2e-dev/web";
import { expect } from "e2e";
import { createClient } from "../src/api/generated/client";
import { createAgent, createAutomation, runAutomation } from "../src/api/generated/sdk.gen";

describe("Routine run history", () => {
  test("shows when a skipped run has no notification target", async ({ app, browser, screen }) => {
    await app.open("/");
    const origin = new URL(await browser.url());

    if (origin.hostname !== "127.0.0.1") throw new Error("Use the local test server");

    const client = createClient({ baseUrl: origin.origin });

    const { data: agent } = await createAgent({
      client,
      body: { name: "Routine fixture" },
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

    await runAutomation({
      client,
      path: { agentId: agent.id, automationId: routine.id },
      throwOnError: true,
    });

    await app.open(`/a/${agent.id}/automations/${routine.id}`);

    await expect(screen.getByText("the bot has no model", { exact: true })).toBeVisible();
    await expect(screen.getByText("not delivered: no notification target", { exact: true })).toBeVisible();
  });
});
