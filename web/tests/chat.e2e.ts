import { beforeEach, describe, test } from "@e2e-dev/web";
import { expect } from "e2e";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

describe("chat", () => {
  beforeEach(async ({ app, browser }) => {
    await app.open("/");
    const baseUrl = new URL(await browser.url()).origin;
    const res = await fetch(`${baseUrl}/api/agents/default`);
    const agent = await res.json();
    await fetch(`${baseUrl}/api/agents/default`, {
      method: "PUT",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ ...agent, provider: "faux", modelId: "echo" }),
    });
    await app.open("/a/default");
  });

  test("sends a message and displays the assistant reply", async ({ screen }) => {
    const prompt = `Hello from e2e ${Date.now()}`;
    const composer = screen.getByRole("textbox", "Message Dex");
    await composer.waitFor();

    await composer.fill(prompt);
    await screen.getByRole("button", "Send message").tap();

    await expect(composer).toHaveValue("");
    await expect(screen.getByText(prompt, { exact: true })).toBeVisible();
    await expect(screen.getByText(`echo: ${prompt}`, { exact: true })).toBeVisible();
  });

  test("shows the child conclusion as one notification in the originating Chat", async ({
    app,
    screen,
    browser,
  }) => {
    const base = new URL(await browser.url()).origin + "/api/agents/default/chats";
    const created = await fetch(base, { method: "POST" });

    expect(created.status).toBe(201);
    const { id } = await created.json();

    const settings = await fetch(`${base}/${id}/settings`, {
      method: "PUT",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ provider: "faux", modelId: "spawn-notify", thinkingLevel: "off" }),
    });

    expect(settings.ok).toBe(true);

    const environment = await fetch(`${base}/${id}/environment`, {
      method: "PUT",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ environmentId: "local" }),
    });

    expect(environment.ok).toBe(true);

    const path = `/a/default/c/${id}`;
    await app.open(path);
    await screen.getByRole("textbox", "Message Dex").fill(
      JSON.stringify({
        title: "Web notification fixture",
        provider: "faux",
        modelId: "echo",
        message: "Raw child data",
      }),
    );
    await screen.getByRole("button", "Send message").tap();

    const notice = screen.getByRole("article", "Notification");
    await expect(notice.getByText("Reviewed child conclusion", { exact: true })).toBeVisible();
    await expect(notice.getByText("Raw child data")).toHaveCount(0);

    await app.open("/a/default");
    await expect(notice).toHaveCount(0);
    await app.open(path);
    await expect(notice.getByText("Reviewed child conclusion", { exact: true })).toBeVisible();
    await expect(notice).toHaveCount(1);
  });

  test("sends from idle Main while its child is working", async ({ app, screen, browser }) => {
    const baseUrl = new URL(await browser.url()).origin;
    const settings = `${baseUrl}/api/agents/default`;
    const agent = await (await fetch(settings)).json();

    const updated = await fetch(settings, {
      method: "PUT",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ ...agent, provider: "faux", modelId: "tool" }),
    });

    expect(updated.ok).toBe(true);
    await app.open("/a/default");

    // Given Main started a child that waits for a local gate, and Main finished its turn.
    const dir = await mkdtemp(join(tmpdir(), "dex-main-e2e-"));
    const mainGate = join(dir, "main-release");
    const stamp = Date.now();
    const title = `Gated child ${stamp}`;
    const waitForGate = "deadline=$((SECONDS+45));";
    const composer = screen.getByRole("textbox", "Message Dex");
    const send = screen.getByRole("button", "Send message");
    const childReply = screen.getByText(new RegExp(`^tool:.*${title}`));

    try {
      await composer.fill(
        `session_new ${JSON.stringify({ title, provider: "faux", modelId: "bash", message: `${waitForGate} while test -d '${dir}'; do if (( SECONDS >= deadline )); then exit 1; fi; sleep 0.05; done` })}`,
      );
      await send.tap();
      await childReply.waitFor();
      await screen.getByRole("link", "Dex, Working").waitFor();
      await app.open("/a/default");
      await childReply.waitFor();
      await screen.getByRole("link", "Dex, Working").waitFor();

      // When the user sends to Main, then Main answers before the child can finish.
      await expect(send).toBeVisible();
      await expect(screen.getByRole("button", "Stop run")).not.toBeVisible();
      await composer.fill(`bash {"command":"printf main-replied-${stamp}"}`);
      await send.tap();
      await screen.getByText(new RegExp(`^tool:.*main-replied-${stamp}`)).waitFor();
      await expect(screen.getByRole("link", "Dex, Working")).toBeVisible();

      // Main still offers Queue while its own turn runs.
      await composer.fill(
        `bash ${JSON.stringify({ command: `${waitForGate} until test -f '${mainGate}'; do if (( SECONDS >= deadline )) || ! test -d '${dir}'; then exit 1; fi; sleep 0.05; done` })}`,
      );
      await send.tap();
      const stop = screen.getByRole("button", "Stop run");
      await stop.waitFor();
      const activeComposer = screen.getByRole("textbox", "Add to this run…");
      await activeComposer.fill(`bash {"command":"printf queued-reply-${stamp}"}`);
      await screen.getByRole("button", "Choose when to send").tap();
      const queueMode = screen.getByRole("menuitemradio", /^Queue/);
      await queueMode.tap();
      await queueMode.press("Escape");
      await screen.getByRole("button", "Queue message").tap();
      await expect(activeComposer).toHaveValue("");
      await screen.getByRole("button", "Remove", { exact: true }).waitFor();
      const queuedReply = screen.getByText(new RegExp(`^tool:.*queued-reply-${stamp}`));
      await expect(queuedReply).not.toBeVisible();
      await stop.tap();
      await screen.getByText(/^Stop requested/).waitFor();
      await queuedReply.waitFor();

      // The child still has its own Stop button.
      const { chats } = await (await fetch(`${settings}/chats`)).json();
      const child = chats.find((chat: { title: string }) => chat.title === title);
      expect(child?.busy).toBe(true);
      await app.open(`/a/default/c/${child.id}`);
      await stop.tap();
      await screen.getByText(/^Stop requested/).waitFor();
      await expect(stop).not.toBeVisible();
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  });

  test("shows and cancels a session monitor", async ({ app, screen, browser }) => {
    const baseUrl = new URL(await browser.url()).origin;
    const settings = `${baseUrl}/api/agents/default`;
    const agent = await (await fetch(settings)).json();
    await fetch(settings, {
      method: "PUT",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ ...agent, provider: "faux", modelId: "tool" }),
    });
    await app.open("/a/default");
    const prompt = `Synthetic monitor ${Date.now()}`;
    await screen
      .getByRole("textbox", "Message Dex")
      .fill(
        `monitor_set_schedule ${JSON.stringify({ delaySeconds: 300, prompt, checkCommand: "printf ready", maxRuns: 3, intervalSeconds: 60 })}`,
      );
    await screen.getByRole("button", "Send message").tap();
    await screen.getByText(new RegExp(`^tool:.*${prompt}`)).waitFor();
    await screen.getByRole("button", "Monitors and outbox").tap();

    const monitor = screen
      .getByRole("list", "Follow-up monitors")
      .getByRole("listitem")
      .filter({ hasText: prompt });

    await monitor.waitFor();
    await expect(monitor).toContainText("script · 0/3 checks");
    await monitor.getByRole("button", /Cancel monitor/).tap();
    await monitor.getByText("cancelled", { exact: true }).first().waitFor();
  });

  test("opens the Sessions list and creates a session from Main", async ({ screen, browser }) => {
    await screen.getByRole("link", "Sessions").tap();
    await expect(browser).toHaveURL(/\/a\/default\/sessions/);
    await expect(screen.getByRole("heading", "Sessions")).toBeVisible();

    await screen.getByRole("button", "New session").tap();
    const closeDialog = screen.getByRole("button", "Close");

    if (await closeDialog.isVisible().catch(() => false)) {
      await closeDialog.tap();
    }

    await expect(browser).toHaveURL(/\/a\/default\/c\/\d+/);
    await expect(screen.getByText(/This session has its own context/)).toBeVisible();
  });
});
