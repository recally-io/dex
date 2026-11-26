import { test } from "@e2e-dev/web";
import { expect } from "e2e";
import { createClient } from "../src/api/generated/client";
import { createBot } from "../src/api/generated/sdk.gen";

test("creates, renames, archives, and restores a Bot from navigation", async ({ app, browser, screen }) => {
  // Given Dex opens the default Bot and an owner can ask a Bot to archive another.
  await app.open("/");
  await expect(browser).toHaveURL("/b/default");
  const client = createClient({ baseUrl: new URL(await browser.url()).origin });

  const { data: manager } = await createBot({
    client,
    body: { name: "Archive helper", provider: "faux", modelId: "tool", runnerId: "local" },
    throwOnError: true,
  });

  // When the owner creates a Bot and changes its name in Settings.
  const bots = screen.getByRole("navigation", "Bots", { exact: true });
  const navigation = screen.getByRole("navigation", "Bot navigation");
  await bots.getByRole("button", "New bot").tap();
  const dialog = screen.getByRole("dialog", "Create bot");
  await dialog.getByLabel("Name", { exact: true }).fill("Main path Bot");
  await dialog.getByRole("button", "Create bot", { exact: true }).tap();
  await expect(browser).toHaveURL(/\/b\/(?!default$)[^/]+$/);
  const created = new URL(await browser.url()).pathname;
  await expect(screen.getByRole("textbox", "Message Main path Bot", { exact: true })).toBeVisible();
  await bots.getByRole("link", /^Dex(?:,|$)/).tap();
  await expect(browser).toHaveURL("/b/default");
  await bots.getByRole("link", "Main path Bot", { exact: true }).tap();
  await navigation.getByRole("link", "Bot settings", { exact: true }).tap();
  await expect(browser).toHaveURL(`${created}/bot`);
  const name = screen.getByRole("textbox", "Name", { exact: true });
  await name.fill("Renamed path Bot");
  await name.press("Enter");
  const renamed = bots.getByRole("link", "Renamed path Bot", { exact: true });
  await expect(renamed).toBeVisible();
  expect(await renamed.getAttribute("aria-current")).toBe("page");
  await expect(browser).toHaveClass(renamed, /\bfont-semibold\b/);

  // When the owner approves the archive question, then the archive can restore the saved Bot.
  await bots.getByRole("link", manager.name, { exact: true }).tap();
  await screen
    .getByRole("textbox", `Message ${manager.name}`, { exact: true })
    .fill(`bot_delete ${JSON.stringify({ bot: "Renamed path Bot", reason: "No longer in use" })}`);
  await screen.getByRole("button", "Send message", { exact: true }).tap();
  await screen.getByRole("button", /Archive Renamed path Bot/).tap();
  await expect(renamed).toHaveCount(0);

  await screen.getByRole("complementary", "Sidebar").getByRole("link", "Settings", { exact: true }).tap();
  await screen.getByRole("link", "Archived", { exact: true }).tap();
  await expect(browser).toHaveURL("/settings/archived");
  const archived = screen.getByRole("listitem").filter({ hasText: "Renamed path Bot" });
  await archived.getByRole("button", "Restore", { exact: true }).tap();
  await expect(archived).toHaveCount(0);
  await renamed.tap();
  await expect(browser).toHaveURL(created);
  await expect(screen.getByRole("textbox", "Message Renamed path Bot", { exact: true })).toBeVisible();
});
