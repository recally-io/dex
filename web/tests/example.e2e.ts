import { test } from "@e2e-dev/web";
import { expect } from "e2e";

test("app opens", async ({ app, browser }) => {
  await app.open("/");
  await expect(browser.locator("body")).toBeVisible();
});

// With the model key in the environment, uncomment:
// test('the bot drives a flow', async ({ app, bot }) => {
//   await app.open('/');
//   await bot.act('one goal in natural language');
//   await bot.assert('one question about the screen');
// });
