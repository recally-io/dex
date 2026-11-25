import { test } from "@e2e-dev/web";
import { expect } from "e2e";

test("keeps old global settings links and separates Runners, Secrets, and Sources", async ({
  app,
  browser,
  screen,
}) => {
  async function checkRedirect(oldPath: string, path: string, title: string) {
    await app.open(oldPath);
    await expect(screen.getByRole("heading", title, { exact: true })).toBeVisible();
    expect(new URL(await browser.url()).pathname).toBe(path);
  }

  await checkRedirect("/settings", "/settings/general", "General");
  await checkRedirect("/providers", "/settings/providers", "Providers");
  await checkRedirect("/archived", "/settings/archived", "Archived bots");
  await checkRedirect("/about", "/settings/about", "About");
  await checkRedirect("/settings/environment", "/settings/runners", "Runners");
  await checkRedirect("/environment", "/settings/runners", "Runners");
  await checkRedirect("/secrets", "/settings/secrets", "Secrets");

  await expect(screen.getByRole("heading", "Secret vault", { exact: true })).toBeVisible();
  await expect(screen.getByRole("heading", "1Password", { exact: true })).toBeVisible();
  await expect(screen.getByRole("button", "Add Runner")).toHaveCount(0);
  await screen.getByRole("link", "Runners", { exact: true }).tap();
  await expect(screen.getByRole("heading", "Local", { exact: true })).toBeVisible();
  await expect(screen.getByRole("heading", "Secret vault", { exact: true })).toHaveCount(0);
  await expect(screen.getByRole("heading", "1Password", { exact: true })).toHaveCount(0);
  await screen.getByRole("link", "Integrations", { exact: true }).tap();
  await expect(screen.getByRole("heading", "Sources", { exact: true })).toBeVisible();
  await screen.getByRole("link", "General", { exact: true }).tap();
  await expect(screen.getByRole("heading", "Bot defaults", { exact: true })).toBeVisible();
  await expect(screen.getByRole("heading", "Sources", { exact: true })).toHaveCount(0);
});

test("moves bot instructions, skills, channels, and secrets into four settings pages", async ({
  app,
  browser,
  screen,
}) => {
  await app.open("/a/default/instructions");
  await expect(screen.getByRole("textbox", "Bot instructions")).toBeVisible();
  const general = new URL(await browser.url());

  expect(general.pathname).toBe("/a/default/agent");
  expect(general.hash).toBe("#instructions");
  await screen.getByRole("link", "Skills", { exact: true }).tap();
  await expect(screen.getByRole("heading", "Available skills")).toBeVisible();
  await expect(screen.getByRole("button", "Reload", { exact: true })).toBeVisible();
  await expect(screen.getByRole("heading", "MCP servers")).toHaveCount(0);
  await app.open("/a/default/channels");
  await expect(screen.getByRole("heading", "Integrations")).toBeVisible();
  expect(new URL(await browser.url()).pathname).toBe("/a/default/integrations");
  await expect(screen.getByRole("link", "Telegram", { exact: true })).toBeVisible();
  await expect(screen.getByRole("heading", "MCP servers")).toBeVisible();
  await screen.getByRole("button", "Reload", { exact: true }).tap();
  await expect(screen.getByRole("button", "Reload", { exact: true })).toBeVisible();
  await expect(screen.getByText("Could not reload tools and MCP servers")).toHaveCount(0);
  await screen.getByRole("link", "Secrets", { exact: true }).tap();
  await expect(screen.getByRole("heading", "Secret vault", { exact: true })).toBeVisible();
  await expect(screen.getByRole("heading", "1Password", { exact: true })).toHaveCount(0);
});
