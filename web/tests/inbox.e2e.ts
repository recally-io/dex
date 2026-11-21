import { createHmac, randomInt } from "node:crypto";
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
  await expect(screen.getByText("Owner")).toBeVisible();

  await screen.getByRole("button", "Complete task").tap();
  await expect(screen.getByRole("button", "Reopen task")).toBeVisible();
  await screen.getByRole("button", "Reopen task").tap();
  await expect(screen.getByRole("button", "Complete task")).toBeVisible();
});

type LocalGitHubAuthor = { user: { login: string }; author_association: string };

type LocalGitHubPayload =
  | { issue: { id: number; number: number; title: string; created_at: string } & LocalGitHubAuthor }
  | {
      pull_request: { number: number };
      review: {
        id: number;
        state: string;
        commit_id: string;
        body: string;
        submitted_at: string;
      } & LocalGitHubAuthor;
    };

test("links a PR and sees its reviews and wait on the Issue Task", async ({ app, browser, screen }) => {
  await app.open("/inbox");
  const origin = new URL(await browser.url()).origin;

  if (new URL(origin).hostname !== "127.0.0.1") throw new Error("Use the local test server");
  const repositoryId = randomInt(1_000_000, 1_000_000_000);
  const secret = "local browser test webhook secret only";

  const settings = await fetch(`${origin}/api/settings/github-webhooks/${repositoryId}`, {
    method: "PUT",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ secret, issueAgentId: "default" }),
  });

  if (settings.status !== 204) throw new Error(`Webhook setup: ${settings.status}`);

  async function deliver(kind: string, id: string, data: LocalGitHubPayload) {
    const body = JSON.stringify({
      action: kind === "issues" ? "opened" : "submitted",
      repository: { id: repositoryId },
      ...data,
    });

    const response = await fetch(`${origin}/hooks/github/${repositoryId}`, {
      method: "POST",
      headers: {
        "X-GitHub-Event": kind,
        "X-GitHub-Delivery": id,
        "X-Hub-Signature-256": `sha256=${createHmac("sha256", secret).update(body).digest("hex")}`,
      },
      body,
    });

    if (response.status !== 204) throw new Error(`Local delivery: ${response.status}`);
  }

  const title = `GitHub issue #${repositoryId}`;
  await deliver("issues", "issue", {
    issue: {
      id: 1,
      number: repositoryId,
      title: "Review login fix",
      created_at: new Date().toISOString(),
      user: { login: "maintainer" },
      author_association: "MEMBER",
    },
  });
  await screen.getByRole("link", new RegExp(title)).tap();
  await expect(screen.getByRole("heading", title)).toBeVisible();
  await expect(screen.getByText("External event")).toBeVisible();
  await screen.getByRole("textbox", "Repository ID").fill(String(repositoryId));
  await screen.getByRole("textbox", "PR number").fill("42");
  await screen.getByRole("textbox", "Commit SHA (optional)").fill("commit-a");
  await screen.getByRole("button", "Link pull request").tap();
  await expect(screen.getByText(`PR #42 · Repository ${repositoryId}`)).toBeVisible();
  await expect(screen.getByText("Waiting for review")).toBeVisible();

  async function review(id: number, commit: string, text: string, association = "MEMBER") {
    await deliver("pull_request_review", `review-${id}`, {
      pull_request: { number: 42 },
      review: {
        id,
        state: "commented",
        commit_id: commit,
        body: text,
        user: { login: "reviewer" },
        author_association: association,
        submitted_at: new Date(Date.now() + 1000).toISOString(),
      },
    });
  }

  await review(1, "commit-b", "Review of another commit");
  await expect(screen.getByText(/Review of another commit/)).toBeVisible();
  await expect(screen.getByText("Waiting for review")).toBeVisible();
  await review(3, "commit-a", "Review from outside the repository", "NONE");
  await expect(screen.getByText(/Review from outside the repository/)).toBeVisible();
  await expect(screen.getByText("Waiting for review")).toBeVisible();
  await review(2, "commit-a", "Review of the selected commit");
  await expect(screen.getByText(/Review of the selected commit/)).toBeVisible();
  await expect(screen.getByText("Review received")).toBeVisible();

  await app.open(new URL(await browser.url()).pathname);
  await expect(screen.getByRole("heading", title)).toBeVisible();
  await expect(screen.getByText(`PR #42 · Repository ${repositoryId}`)).toBeVisible();
  await expect(screen.getByText("Review received")).toBeVisible();
  await expect(screen.getByText(/Review of the selected commit/)).toBeVisible();
});
