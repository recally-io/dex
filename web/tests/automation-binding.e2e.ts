import { describe, test } from "@e2e-dev/web";
import { expect } from "e2e";

const json = { "content-type": "application/json" };

describe("Automation session binding", () => {
  test("saves an existing session instead of a project directory", async ({ app, browser, screen }) => {
    await app.open("/");
    const origin = new URL(await browser.url()).origin;

    const created = await fetch(`${origin}/api/bots`, {
      method: "POST",
      headers: json,
      body: JSON.stringify({ name: "Routine browser fixture", provider: "faux", modelId: "echo" }),
    });

    if (created.status !== 201) throw new Error(`Create bot fixture: ${created.status}`);
    const { id: botId }: { id: string } = await created.json();
    const bot = `${origin}/api/bots/${botId}`;
    const session = await fetch(`${bot}/sessions`, { method: "POST" });

    if (session.status !== 201) throw new Error(`Create target fixture: ${session.status}`);
    const { id }: { id: number } = await session.json();
    await app.open(`/b/${botId}/automations`);
    await screen.getByRole("button", "New automation").tap();
    const dialog = screen.getByRole("dialog", "New automation");
    await dialog.getByLabel("Name", { exact: true }).fill("Routine browser check");
    await dialog.getByLabel("Prompt", { exact: true }).fill("Browser fixture prompt");
    await dialog.getByLabel("Session for each run").selectOption({ value: "session" });
    await dialog.getByRole("option", `Untitled session · #${id}`).waitFor({ state: "attached" });
    await dialog.getByLabel("Target session").selectOption({ value: String(id) });

    const overrides =
      (await dialog.getByLabel("Project directory").count()) +
      (await dialog.getByLabel("Runner", { exact: true }).count());

    await dialog.getByRole("button", "Save automation").tap();
    await screen.getByRole("link", /Routine browser check/).waitFor();
    const response = await fetch(`${bot}/automations`);

    if (!response.ok) throw new Error(`Read Routine fixture: ${response.status}`);

    const {
      automations,
    }: {
      automations: { name: string; session: string; targetSessionId?: number }[];
    } = await response.json();

    await expect(overrides).toBe(0);
    await expect(automations.find((routine) => routine.name === "Routine browser check")).toMatchObject({
      session: "session",
      targetSessionId: id,
    });
  });
});
