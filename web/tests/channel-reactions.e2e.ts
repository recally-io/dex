import { describe, test } from "@e2e-dev/web";
import { expect } from "e2e";

describe("Channel status reactions", () => {
  test("Telegram defaults to private on and group off", async ({ app, screen }) => {
    await app.open("/a/default/channels/telegram");
    await screen.getByRole("button", { name: "Connect bot" }).click();
    await screen.getByText("Delivery and previews", { exact: true }).click();

    await expect(screen.getByRole("combobox", { name: "Private chat status reactions" })).toHaveValue("on");
    await expect(screen.getByRole("combobox", { name: "Group status reactions" })).toHaveValue("off");
  });

  test("Feishu defaults to private on without a group setting", async ({ app, screen }) => {
    await app.open("/a/default/channels/feishu");
    await screen.getByText("Use an existing app", { exact: true }).click();

    await expect(screen.getByRole("combobox", { name: "Private chat status reactions" })).toHaveValue("on");
    await expect(screen.getByRole("combobox", { name: "Group status reactions" })).toHaveCount(0);
  });
});
