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

  await dialog.getByLabel("Name").fill(name);
  await screen.getByRole("button", "Get install command").tap();

  // The test server is plain HTTP on loopback, so Dex refuses to hand out a command that a
  // machine could not use, and it names the fix instead. The dialog stays as it was.
  await expect(screen.getByRole("heading", { name: "Add execution environment" })).toBeVisible();
  await expect(dialog.getByLabel("Name")).toBeDisabled();
  await expect(dialog.getByText(/Open Dex at the HTTPS address/)).toBeVisible();
  await expect(dialog.getByRole("button", "Get install command")).toBeDisabled();

  await screen.getByRole("button", "Done").tap();
  await expect(screen.getByRole("heading", { name })).toBeVisible();
});
