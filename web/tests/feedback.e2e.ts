import { describe, test } from "@e2e-dev/web";
import { expect } from "e2e";

describe("feedback", () => {
  test("previews only the entered text and links to the public repository", async ({ app, screen }) => {
    await app.open("/a/default");
    await screen.getByRole("button", "Feedback").tap();
    await screen.getByRole("textbox", "Title").fill("Example title");
    await screen.getByRole("textbox", "Description").fill("Description <literal>\nSecond line");

    await screen.getByRole("button", "Preview draft").tap();

    await expect(screen.getByText(/recally-io\/dex is public/)).toBeVisible();
    await expect(screen.getByRole("textbox", "Title")).toHaveValue("Example title");
    await expect(screen.getByRole("textbox", "Description")).toHaveValue(
      "Description <literal>\nSecond line",
    );
    await expect(screen.getByRole("button", "Copy title")).toBeVisible();
    await expect(screen.getByRole("button", "Copy description")).toBeVisible();
    await expect(screen.getByRole("link", "Open GitHub new issue")).toHaveAttribute(
      "href",
      "https://github.com/recally-io/dex/issues/new",
    );
  });

  test("opens an empty form from slash without a configured model", async ({ app, browser, screen }) => {
    await app.open("/");
    const baseURL = new URL(await browser.url()).origin;

    const created = await fetch(`${baseURL}/api/agents`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ name: "Feedback fixture", provider: "", modelId: "" }),
    });

    if (created.status !== 201) throw new Error(`Create feedback fixture: ${created.status}`);
    const agent = await created.json();
    await app.open(`/a/${agent.id}`);
    await screen.getByText("None chosen", { exact: true }).waitFor();
    await screen.getByRole("textbox", "Message Feedback fixture").fill("/feedback do not use this text");

    await screen.getByRole("button", "Run command").tap();

    await expect(screen.getByRole("dialog", "Feedback")).toBeVisible();
    await expect(screen.getByRole("textbox", "Title")).toHaveValue("");
    await expect(screen.getByRole("textbox", "Description")).toHaveValue("");
  });
});
