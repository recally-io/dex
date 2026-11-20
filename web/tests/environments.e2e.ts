import { test } from "@e2e-dev/web";
import { expect } from "e2e";

test("adds a dex runner environment and copies its command in the same dialog", async ({ app, screen }) => {
  await app.open("/settings/environment");

  await screen.getByRole("button", "Add environment").tap();
  await screen.getByRole("radio", "Another machine").tap();
  await screen.getByRole("radio", "dex runner").tap();

  const name = `VM ${Date.now()}`;
  const address = screen.getByLabel("Address the machine uses for Dex");

  await expect(address).toBeVisible();
  await screen.getByLabel("Name").fill(name);
  await screen.getByRole("button", "Add environment").tap();

  const copy = screen.getByRole("button", "Copy install command");

  await copy.waitFor();
  await expect(screen.getByRole("heading", { name: "Add execution environment" })).toBeVisible();
  await expect(screen.getByLabel("Name")).toHaveValue(name);
  await expect(screen.getByLabel("Name")).toBeDisabled();
  await expect(screen.getByText(/install-machine\.sh/)).toBeVisible();
  await expect(screen.getByText(/expires in 10 minutes/)).toBeVisible();

  await screen.getByRole("button", "Done").tap();
  await expect(screen.getByRole("heading", { name })).toBeVisible();
});
