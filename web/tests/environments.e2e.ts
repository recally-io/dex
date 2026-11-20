import { test } from "@e2e-dev/web";
import { expect } from "e2e";

test("adds a dex runner environment and copies its command in the same dialog", async ({
  app,
  browser,
  screen,
}) => {
  await app.open("/settings/environment");

  // The data directory survives a run, so start from an empty list.
  const baseUrl = new URL(await browser.url()).origin;
  const res = await fetch(`${baseUrl}/api/settings/environments`);
  const body: { environments: { id: string }[] } = await res.json();

  await Promise.all(
    body.environments.map((env) =>
      fetch(`${baseUrl}/api/settings/environments/${env.id}`, { method: "DELETE" }),
    ),
  );

  await app.open("/settings/environment");
  await screen.getByRole("button", "Add environment").tap();
  await screen.getByRole("radio", "Another machine").tap();
  await screen.getByRole("radio", "dex runner").tap();

  const name = `VM ${Date.now()}`;
  const dialog = screen.getByRole("dialog");
  const address = dialog.getByLabel("Address the machine uses for Dex");

  await expect(address).toBeVisible();
  await dialog.getByLabel("Name").fill(name);
  await screen.getByRole("button", "Get install command").tap();

  const copy = dialog.getByRole("button", "Copy install command");

  await copy.waitFor();
  await expect(screen.getByRole("heading", { name: "Add execution environment" })).toBeVisible();
  await expect(dialog.getByLabel("Name")).toHaveValue(name);
  await expect(dialog.getByLabel("Name")).toBeDisabled();
  await expect(address).toBeVisible();
  await expect(dialog.getByText(/install-machine\.sh/)).toBeVisible();
  await expect(dialog.getByText(/expires in 10 minutes/)).toBeVisible();

  await screen.getByRole("button", "Done").tap();
  await expect(screen.getByRole("heading", { name })).toBeVisible();
});
