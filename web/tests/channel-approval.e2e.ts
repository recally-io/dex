import { test } from "@e2e-dev/web";
import { expect } from "e2e";

test("reviews and applies a single channel change", async ({ app, browser, screen }) => {
  await app.open("/");
  const baseUrl = new URL(await browser.url()).origin;

  const settings = await fetch(`${baseUrl}/api/agents`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ name: "Channel Review", provider: "faux", modelId: "tool" }),
  });

  expect(settings.status).toBe(201);
  const { id } = await settings.json();
  await app.open(`/a/${id}`);
  await screen
    .getByRole("textbox", "Message Channel Review")
    .fill(`channel_request_change {"operation":"clear_notification_target"}`);
  await screen.getByRole("button", "Send message").tap();

  const pathPattern = /\/channel-approvals\/[0-9a-f-]{36}/;
  const answer = screen.getByText(/^tool:.*\/channel-approvals\/[0-9a-f-]{36}/);
  await answer.waitFor();
  const path = (await answer.textContent())?.match(pathPattern)?.[0];

  if (!path) throw new Error("Missing channel review link");

  await app.open(path);
  await expect(screen.getByRole("heading", "Confirm channel change")).toBeVisible();
  await expect(
    screen.getByText("Clear the primary notification destination", { exact: false }),
  ).toBeVisible();
  await screen.getByRole("button", "Apply this change").tap();
  await expect(screen.getByText("The change was applied.")).toBeVisible();
  expect((await fetch(`${baseUrl}${path}`)).status).toBe(404);
});
