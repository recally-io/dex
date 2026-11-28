import { describe, test } from "@e2e-dev/web";
import { expect } from "e2e";
import { createClient } from "../src/api/generated/client";
import {
  createBot,
  createAutomation,
  listAutomationResults,
  runAutomation,
} from "../src/api/generated/sdk.gen";

describe("Automation run history", () => {
  test("runs a new Automation and follows up on its Board Update", async ({ app, browser, screen }) => {
    await app.open("/");
    const client = createClient({ baseUrl: new URL(await browser.url()).origin });

    const { data: bot } = await createBot({
      client,
      body: { name: "Update follow-up Bot", provider: "faux", modelId: "tool", runnerId: "local" },
      throwOnError: true,
    });

    await app.open(`/b/${bot.id}/automations`);
    await screen.getByRole("button", "New automation", { exact: true }).tap();
    const dialog = screen.getByRole("dialog", "New automation");
    const name = "Check the release queue";
    const summary = "One release needs a follow-up";
    await dialog.getByLabel("Name", { exact: true }).fill(name);
    await dialog
      .getByLabel("Prompt", { exact: true })
      .fill(`automation_report ${JSON.stringify({ outcome: "changed", summary })}`);
    await dialog.getByRole("switch", "Enabled", { exact: true }).tap();
    await dialog.getByRole("button", "Save automation", { exact: true }).tap();
    await screen.getByRole("link", new RegExp(name)).tap();
    await screen.getByRole("button", "Run now", { exact: true }).tap();
    await screen.getByText(summary, { exact: true }).waitFor({ timeout: 15_000 });

    const { data } = await listAutomationResults({ client, throwOnError: true });
    const result = data.results.find((item) => item.botId === bot.id && item.automationName === name);

    if (!result) throw new Error("The completed Automation has no Board Update");

    await app.open("/board");
    const updates = screen.getByRole("region", "Updates", { exact: true });
    const group = updates.getByRole("button", new RegExp(`^${name}`));
    await group.tap();
    await expect(updates.getByText(summary, { exact: true })).toBeVisible();

    const resultPath = `/board/results/${result.runId}`;
    await app.open(resultPath);
    await expect(screen.getByRole("heading", name, { exact: true })).toBeVisible();
    await screen.getByRole("button", "Continue in session", { exact: true }).tap();
    await expect(browser).toHaveURL(new RegExp(`/b/${bot.id}/s/\\d+$`));
    expect(new URL(await browser.url()).pathname).not.toBe(`/b/${bot.id}/s/${result.sessionId}`);
    await expect(screen.getByRole("textbox", `Message ${bot.name}`)).toBeVisible();
    await expect(screen.getByText(/^Follow up on [\s\S]+Run transcript: \/b\//)).toBeVisible();

    await app.open(resultPath);
    await screen.getByRole("button", "Done", { exact: true }).tap();
    await expect(browser).toHaveURL("/board");
    await expect(group).toHaveCount(0);
  });
  test("offers the Outbox on a read-only Automation transcript", async ({ app, browser, screen }) => {
    await app.open("/");
    const origin = new URL(await browser.url());

    if (origin.hostname !== "127.0.0.1") throw new Error("Use the local test server");

    const client = createClient({ baseUrl: origin.origin });

    const { data: bot } = await createBot({
      client,
      body: { name: "Routine Outbox fixture", provider: "faux", modelId: "echo" },
      throwOnError: true,
    });

    const { data: routine } = await createAutomation({
      client,
      path: { botId: bot.id },
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
      path: { botId: bot.id, automationId: routine.id },
      throwOnError: true,
    });

    expect(run.sessionId).toBeDefined();
    await app.open(`/b/${bot.id}/s/${run.sessionId}`);
    await screen.getByRole("button", "Session activity").tap();
    await expect(screen.getByText("No outbound messages yet.")).toBeVisible();
    await expect(screen.getByRole("textbox", "Message Dex")).toHaveCount(0);
  });

  test("shows when a skipped run has no notification target", async ({ app, browser, screen }) => {
    await app.open("/");
    const origin = new URL(await browser.url());

    if (origin.hostname !== "127.0.0.1") throw new Error("Use the local test server");

    const client = createClient({ baseUrl: origin.origin });

    // Empty fields prevent the fixture from inheriting a model or a runner.
    const { data: bot } = await createBot({
      client,
      body: { name: "Routine fixture", provider: "", modelId: "", runnerId: "local" },
      throwOnError: true,
    });

    const { data: routine } = await createAutomation({
      client,
      path: { botId: bot.id },
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
      path: { botId: bot.id, automationId: routine.id },
      throwOnError: true,
    });

    expect(run.status).toBe("skipped");
    expect(run.reason).toBe("the bot has no model");

    await app.open(`/b/${bot.id}/automations/${routine.id}`);

    await expect(screen.getByText("the bot has no model", { exact: true })).toBeVisible();
    await expect(screen.getByText("not delivered: no notification target", { exact: true })).toBeVisible();
  });
});
