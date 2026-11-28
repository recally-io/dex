import { test } from "@e2e-dev/web";
import { expect } from "e2e";

test("saves and deletes a write-only shared secret", async ({ app, browser, screen }) => {
  await app.open("/settings/secrets");
  const name = `VAULT_BROWSER_${Date.now()}`;
  const value = "vault-browser-fixture-value";
  const endpoint = new URL("/api/bots/default/vault", await browser.url());

  try {
    await screen.getByLabel("Secret name").fill(name);
    await screen.getByLabel("Secret value").fill(value);
    await screen.getByRole("button", "Save secret").tap();
    await expect(screen.getByText(`vault:${name}`)).toBeVisible();

    const response = await fetch(endpoint);
    const body = await response.text();

    if (!response.ok || !body.includes(name) || body.includes(value)) {
      throw new Error("The vault API must return only secret metadata");
    }

    await screen.getByRole("button", `Delete ${name}`).tap();
    await expect(screen.getByText(`vault:${name}`)).toHaveCount(0);
  } finally {
    endpoint.search = new URLSearchParams({ scope: "dex", name }).toString();
    await fetch(endpoint, { method: "DELETE" });
  }
});
