import { test } from "@e2e-dev/web";
import { expect } from "e2e";

test("tracks a task from creation to completion and reopening", async ({ app, screen }) => {
  await app.open("/inbox");
  await screen.getByRole("textbox", "Task title").fill("Fix the sign-in flow");
  await screen.getByRole("button", "Create task").tap();
  await expect(screen.getByRole("heading", "Fix the sign-in flow")).toBeVisible();

  await screen.getByRole("textbox", "Task note").fill("The change is ready for review.");
  await screen.getByRole("button", "Add note").tap();
  await expect(screen.getByText("The change is ready for review.")).toBeVisible();

  await screen.getByRole("button", "Complete task").tap();
  await expect(screen.getByRole("button", "Reopen task")).toBeVisible();
  await screen.getByRole("button", "Reopen task").tap();
  await expect(screen.getByRole("button", "Complete task")).toBeVisible();
});
