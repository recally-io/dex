import { afterEach, beforeEach, test } from "@e2e-dev/web";
import { expect } from "e2e";

type SettingsWindow = Window & { settingsErrors: string[] };

beforeEach(async ({ browser }) => {
  await browser.addInitScript(() => {
    const errors: string[] = [];
    Object.assign(window, { settingsErrors: errors });
    const report = console.error.bind(console);
    console.error = (...args) => {
      errors.push(`Console: ${args.map(String).join(" ")}`);
      report(...args);
    };

    window.addEventListener("error", (event) => errors.push(`Page: ${event.message}`));
    window.addEventListener("unhandledrejection", (event) => errors.push(`Page: ${String(event.reason)}`));
  });
});

afterEach(async ({ browser }) => {
  const errors = await browser.evaluate(() => {
    const failed = performance.getEntriesByType("resource").filter((entry) => {
      if (!(entry instanceof PerformanceResourceTiming)) return false;

      const url = new URL(entry.name);

      return (
        url.origin === location.origin && url.pathname.startsWith("/api/") && entry.responseStatus >= 400
      );
    });

    // SAFETY: The init script sets this array before the Settings app loads.
    const { settingsErrors } = window as SettingsWindow;

    return [...settingsErrors, ...failed.map((entry) => `HTTP ${entry.responseStatus}: ${entry.name}`)];
  });

  expect(errors).toEqual([]);
});

test("navigates Global Settings and saves an SSH Runner", async ({ app, browser, screen }) => {
  await app.open("/settings");
  await expect(screen.getByRole("heading", "General", { exact: true })).toBeVisible();
  expect(new URL(await browser.url()).pathname).toBe("/settings/general");

  const pages = screen.getByRole("navigation", "Settings pages", { exact: true });
  await pages.getByRole("link", "Providers", { exact: true }).tap();
  await expect(browser).toHaveURL("/settings/providers");
  await screen.getByRole("heading", "Connections", { exact: true }).waitFor();

  await pages.getByRole("link", "Secrets", { exact: true }).tap();
  await expect(browser).toHaveURL("/settings/secrets");
  await expect(screen.getByRole("heading", "Secret vault", { exact: true })).toBeVisible();
  await expect(screen.getByRole("heading", "1Password", { exact: true })).toBeVisible();
  await expect(screen.getByRole("button", "Add Runner")).toHaveCount(0);
  await pages.getByRole("link", "Runners", { exact: true }).tap();
  await expect(browser).toHaveURL("/settings/runners");
  await expect(screen.getByRole("heading", "Local", { exact: true })).toBeVisible();
  await screen.getByRole("button", "Add Runner", { exact: true }).tap();
  const dialog = screen.getByRole("dialog", "Add Runner");
  await dialog.getByRole("radio", "Machine", { exact: true }).tap();
  await dialog.getByRole("radio", "SSH", { exact: true }).tap();
  const name = "Settings SSH Runner";
  await dialog.getByLabel("Name", { exact: true }).fill(name);
  await dialog.getByLabel("Host", { exact: true }).fill("runner.example.test");
  await dialog.getByRole("button", "Add Runner", { exact: true }).tap();
  await expect(screen.getByRole("heading", name, { exact: true })).toBeVisible();
  await expect(screen.getByText("runner.example.test", { exact: true })).toBeVisible();
  await screen.getByRole("button", `Edit ${name}`, { exact: true }).tap();
  const edit = screen.getByRole("dialog", `Edit ${name}`);
  await expect(edit.getByLabel("Host", { exact: true })).toHaveValue("runner.example.test");
  await edit.getByRole("button", "Cancel", { exact: true }).tap();
  await screen.getByRole("button", `Remove ${name}`, { exact: true }).tap();
  await screen.getByRole("button", "Remove Runner", { exact: true }).tap();
  await expect(screen.getByRole("heading", name, { exact: true })).toHaveCount(0);
  await expect(screen.getByRole("heading", "Secret vault", { exact: true })).toHaveCount(0);
  await expect(screen.getByRole("heading", "1Password", { exact: true })).toHaveCount(0);
  await pages.getByRole("link", "Integrations", { exact: true }).tap();
  await expect(browser).toHaveURL("/settings/integrations");
  await expect(screen.getByRole("heading", "Sources", { exact: true })).toBeVisible();
  await pages.getByRole("link", "General", { exact: true }).tap();
  await expect(screen.getByRole("heading", "Bot defaults", { exact: true })).toBeVisible();
  await expect(screen.getByRole("link", "Switch workspace", { exact: true })).toHaveCount(0);
  await expect(screen.getByRole("heading", "Sources", { exact: true })).toHaveCount(0);
});

test("opens the four Bot settings pages", async ({ app, browser, screen }) => {
  await app.open("/b/default/bot#instructions");
  await expect(screen.getByRole("textbox", "Bot instructions")).toBeVisible();
  const general = new URL(await browser.url());

  expect(general.pathname).toBe("/b/default/bot");
  expect(general.hash).toBe("#instructions");
  const pages = screen.getByRole("navigation", "Settings pages", { exact: true });
  await pages.getByRole("link", "Integrations", { exact: true }).tap();
  await expect(browser).toHaveURL("/b/default/integrations");
  await expect(screen.getByRole("heading", "Integrations", { exact: true })).toBeVisible();
  await expect(screen.getByRole("heading", "Skills", { exact: true })).toBeVisible();
  await expect(screen.getByRole("heading", "MCP servers", { exact: true })).toBeVisible();
  await screen.getByRole("button", "Reload", { exact: true }).tap();
  await expect(screen.getByRole("button", "Reload", { exact: true })).toBeVisible();
  await expect(screen.getByText("Could not reload tools and MCP servers")).toHaveCount(0);
  await pages.getByRole("link", "Channels", { exact: true }).tap();
  await expect(browser).toHaveURL("/b/default/channels");
  const telegram = screen.getByRole("link", "Telegram", { exact: true });

  await expect(telegram).toBeVisible();
  await telegram.tap();
  await expect(browser).toHaveURL("/b/default/channels/telegram");
  await expect(pages.getByRole("link", "Channels", { exact: true })).toHaveAttribute("aria-current", "page");
  await screen.getByRole("link", "Channels", { exact: true }).first().tap();
  await expect(browser).toHaveURL("/b/default/channels");
  await pages.getByRole("link", "Secrets", { exact: true }).tap();
  await expect(browser).toHaveURL("/b/default/secrets");
  await expect(screen.getByRole("heading", "Secret vault", { exact: true })).toBeVisible();
  await expect(screen.getByRole("heading", "1Password", { exact: true })).toHaveCount(0);
});
