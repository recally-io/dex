import { test } from "@e2e-dev/web";
import { expect } from "e2e";

test("adds an Outbound Machine and copies its command in the same dialog", async ({
  app,
  browser,
  screen,
}) => {
  await app.open("/settings/runners");

  if (new URL(await browser.url()).hostname !== "127.0.0.1") throw new Error("Use the local test server");

  await screen.getByRole("button", "Add Runner").tap();
  await screen.getByRole("radio", "Machine").tap();
  await screen.getByRole("radio", "Outbound").tap();

  const name = `VM ${Date.now()}`;
  const dialog = screen.getByRole("dialog");

  await dialog.getByLabel("Name").fill(name);
  await dialog.getByLabel("Description").fill("Use for macOS builds");
  await screen.getByRole("button", "Get install command").tap();

  // The test server is plain HTTP on loopback, so Dex refuses to hand out a command that a
  // machine could not use, and it names the fix instead. The dialog stays as it was.
  await expect(screen.getByRole("heading", { name: "Add Runner" })).toBeVisible();
  await expect(dialog.getByLabel("Name")).toBeDisabled();
  await expect(dialog.getByText(/Open Dex at the HTTPS address/)).toBeVisible();
  await expect(dialog.getByRole("button", "Get install command")).toBeDisabled();

  await screen.getByRole("button", "Done").tap();
  await expect(screen.getByRole("heading", { name })).toBeVisible();
  await expect(screen.getByText("Use for macOS builds", { exact: true })).toBeVisible();
});

for (const [action, desc] of [
  ["edits", "<strong>Run UI checks</strong>"],
  ["clears", ""],
] as const) {
  test(`${action} a Runner description in Settings`, async ({ app, browser, screen }) => {
    const path = "/settings/runners";
    await app.open(path);

    const api = new URL("/api/settings/runners", await browser.url());
    const name = `Description Runner ${action}`;

    const saved = await fetch(api, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ name, kind: "runner", desc: "Previous description" }),
    });

    if (!saved.ok) throw new Error(`Could not create the Runner fixture (${saved.status})`);

    const { id }: { id: string } = await saved.json();

    try {
      await app.open(path);

      const edit = screen.getByRole("button", { name: `Edit ${name}`, exact: true });
      const dialog = screen.getByRole("dialog");
      await edit.tap();
      await dialog.getByLabel("Description").fill(desc);
      await dialog.getByRole("button", "Save changes").tap();
      await expect(dialog).not.toBeVisible();
      await app.open(path);

      if (desc) await expect(screen.getByText(desc, { exact: true })).toBeVisible();

      await edit.tap();
      await expect(dialog.getByLabel("Description")).toHaveValue(desc);
    } finally {
      await fetch(`${api}/${id}`, { method: "DELETE" });
    }
  });
}

test("a new session can select a sandbox and shows a disconnected runner", async ({
  app,
  browser,
  screen,
}) => {
  await app.open("/a/default");
  const baseUrl = new URL(await browser.url()).origin;

  const sandbox = await fetch(`${baseUrl}/api/settings/runners`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      name: "Local test sandbox",
      kind: "e2b",
      config: { apiUrl: "http://127.0.0.1:1" },
    }),
  });

  const runner = await fetch(`${baseUrl}/api/settings/runners`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ name: "Disconnected runner", kind: "runner" }),
  });

  const session = await fetch(`${baseUrl}/api/agents/default/sessions`, { method: "POST" });

  if (!sandbox.ok || !runner.ok || !session.ok)
    throw new Error("Could not create session environment fixtures");

  const { id: sessionId }: { id: number } = await session.json();
  const { id: sandboxId }: { id: string } = await sandbox.json();
  await app.open(`/a/default/s/${sessionId}`);
  const picker = screen.getByLabel("Runner", { exact: true });
  await picker.selectOption({ value: sandboxId });

  await expect(picker).toHaveValue(sandboxId);
  await expect(screen.getByRole("option", "Disconnected runner (offline)")).toBeDisabled();
});
