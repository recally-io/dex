import { test } from "@e2e-dev/web";
import { expect } from "e2e";

test("offers both Telegram setup choices only after a manager is configured", async ({
  app,
  browser,
  screen,
}) => {
  await app.open("/a/default/channels");
  await screen.getByRole("link", { name: "Telegram", exact: true }).click();
  await expect(screen.getByRole("button", { name: "Connect bot" })).toBeVisible();
  await expect(screen.getByRole("button", { name: "Create a new Bot" })).toHaveCount(0);

  const manager = new URL("/api/telegram/manager", await browser.url());

  if (manager.hostname !== "127.0.0.1") throw new Error("Use the local test server");

  let cleanup: Response;

  try {
    await screen.getByRole("button", { name: "Configure manager" }).click();
    await screen.getByLabel("Manager bot token").fill("456:browserFixture");
    await screen.getByRole("button", { name: "Save manager" }).click();

    const existing = screen.getByRole("button", { name: "Use an existing Bot Token" });
    await expect(existing).toBeVisible();
    await existing.click();
    await expect(screen.getByLabel("Bot token", { exact: true })).toBeVisible();
    await screen.getByRole("button", { name: "Cancel", exact: true }).click();

    await screen.getByRole("button", { name: "Create a new Bot" }).click();
    await expect(screen.getByRole("link", { name: "Verify your Telegram account" })).toBeVisible();
    await screen.getByRole("button", { name: "Cancel setup" }).click();
    await expect(screen.getByRole("heading", { name: "Create a Telegram bot" })).toHaveCount(0);
  } finally {
    cleanup = await fetch(manager, {
      method: "PUT",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ token: "" }),
    });
  }

  if (!cleanup.ok) throw new Error(`Remove test manager: ${cleanup.status}`);
});
