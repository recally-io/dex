import { describe, test } from "@e2e-dev/web";
import { expect } from "e2e";

describe("Channel status reactions", () => {
  test("Telegram defaults to private on without group settings", async ({ app, screen }) => {
    await app.open("/b/default/channels/telegram");
    await screen.getByRole("button", { name: "Connect bot" }).click();
    await screen.getByText("Delivery and status", { exact: true }).click();

    await expect(screen.getByRole("combobox", { name: "Typing status" })).toHaveValue("on");
    await expect(screen.getByRole("combobox", { name: "Answer preview" })).toHaveCount(0);
    await expect(screen.getByRole("combobox", { name: "Private chat status reactions" })).toHaveValue("on");
    await expect(screen.getByRole("combobox", { name: "Group status reactions" })).toHaveCount(0);
    await expect(screen.getByText("Group input", { exact: true })).toHaveCount(0);
  });

  test("Feishu defaults to private on without a group setting", async ({ app, screen }) => {
    await app.open("/b/default/channels/feishu");
    await screen.getByText("Use an existing app", { exact: true }).click();

    await expect(screen.getByRole("combobox", { name: "Private chat status reactions" })).toHaveValue("on");
    await expect(screen.getByRole("combobox", { name: "Group status reactions" })).toHaveCount(0);
  });
});
