import { beforeEach, describe, test } from "@e2e-dev/web";
import { expect } from "e2e";

describe("chat", () => {
  beforeEach(async ({ app, browser }) => {
    await app.open("/");
    const baseUrl = new URL(await browser.url()).origin;
    const res = await fetch(`${baseUrl}/api/agents/default`);
    const agent = await res.json();
    await fetch(`${baseUrl}/api/agents/default`, {
      method: "PUT",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ ...agent, provider: "faux", modelId: "echo" }),
    });
    await app.open("/a/default");
  });

  test("sends a message and displays the assistant reply", async ({ screen }) => {
    const prompt = `Hello from e2e ${Date.now()}`;
    const composer = screen.getByRole("textbox", "Message Dex");
    await composer.waitFor();

    await composer.fill(prompt);
    await screen.getByRole("button", "Send message").tap();

    await expect(composer).toHaveValue("");
    await expect(screen.getByText(prompt, { exact: true })).toBeVisible();
    await expect(screen.getByText(`echo: ${prompt}`, { exact: true })).toBeVisible();
  });

  test("shows and cancels a session monitor", async ({ app, screen, browser }) => {
    const baseUrl = new URL(await browser.url()).origin;
    const settings = `${baseUrl}/api/agents/default`;
    const agent = await (await fetch(settings)).json();
    await fetch(settings, {
      method: "PUT",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ ...agent, provider: "faux", modelId: "tool" }),
    });
    await app.open("/a/default");
    const prompt = `Synthetic monitor ${Date.now()}`;
    await screen
      .getByRole("textbox", "Message Dex")
      .fill(
        `monitor_set ${JSON.stringify({ delaySeconds: 300, prompt, checkCommand: "printf ready", maxRuns: 3, intervalSeconds: 60 })}`,
      );
    await screen.getByRole("button", "Send message").tap();
    await screen.getByText(new RegExp(`^tool:.*${prompt}`)).waitFor();
    await screen.getByRole("button", "Monitors and outbox").tap();

    const monitor = screen
      .getByRole("list", "Follow-up monitors")
      .getByRole("listitem")
      .filter({ hasText: prompt });

    await monitor.waitFor();
    await expect(monitor).toContainText("script · 0/3 checks");
    await monitor.getByRole("button", /Cancel monitor/).tap();
    await monitor.getByText("cancelled", { exact: true }).first().waitFor();
  });

  test("opens the Sessions list and creates a session from Main", async ({ screen, browser }) => {
    await screen.getByRole("link", "Sessions").tap();
    await expect(browser).toHaveURL(/\/a\/default\/sessions/);
    await expect(screen.getByRole("heading", "Sessions")).toBeVisible();

    await screen.getByRole("button", "New session").tap();
    const closeDialog = screen.getByRole("button", "Close");

    if (await closeDialog.isVisible().catch(() => false)) {
      await closeDialog.tap();
    }

    await expect(browser).toHaveURL(/\/a\/default\/c\/\d+/);
    await expect(screen.getByText(/This session has its own context/)).toBeVisible();
  });
});
