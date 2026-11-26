import { describe, test } from "@e2e-dev/web";
import { expect } from "e2e";

describe("Web composer feedback", () => {
  for (const withModel of [false, true]) {
    test(`TestUserDoesNotSeeFeedbackInComposer (${withModel ? "with" : "without"} a model)`, async ({
      app,
      browser,
      screen,
    }) => {
      // Given a bot with or without a model.
      await app.open("/");
      const baseURL = new URL(await browser.url()).origin;
      const name = withModel ? "Composer with model" : "Composer without model";

      const created = await fetch(`${baseURL}/api/bots`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          name,
          provider: withModel ? "faux" : "",
          modelId: withModel ? "echo" : "",
        }),
      });

      if (created.status !== 201) throw new Error(`Create composer fixture: ${created.status}`);

      const bot = await created.json();

      // When the user opens the composer (and the slash menu if enabled).
      await app.open(`/b/${bot.id}`);
      const composer = screen.getByRole("textbox", `Message ${name}`);
      await composer.waitFor();

      if (withModel) {
        await composer.fill("/");
        await screen.getByRole("option", /\/clear/).waitFor();
      } else {
        await screen.getByText("None chosen", { exact: true }).waitFor();
      }

      // Then the composer does not offer Feedback.
      if (withModel) await expect(screen.getByRole("option", /\/feedback/)).toHaveCount(0);

      const composerForm = browser.locator("form").filter({ has: composer });
      await expect(composerForm.getByRole("button", { name: "Feedback", exact: true })).toHaveCount(0);
    });
  }
});
