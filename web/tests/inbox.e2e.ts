import { createHmac, randomInt, randomUUID } from "node:crypto";
import { test } from "@e2e-dev/web";
import { expect } from "e2e";
import { createClient } from "../src/api/generated/client";
import {
  createAgent,
  createAutomation,
  createWorkTask,
  getMainChat,
  getWorkTask,
  runAutomation,
  sendMessage,
  updateAgent,
} from "../src/api/generated/sdk.gen";

test("keeps Tasks on Board and redirects the old Work and Needs you routes", async ({ app, screen }) => {
  await app.open("/work");
  await expect(screen.getByRole("heading", "Board", { exact: true })).toBeVisible();
  await screen.getByRole("textbox", "Task title").fill("Fix the sign-in flow");
  await screen.getByLabel("Initial status").selectOption({ value: "in_progress" });
  await screen.getByRole("button", "Create task").tap();
  await expect(screen.getByRole("heading", "Fix the sign-in flow")).toBeVisible();

  await screen.getByRole("textbox", "Task note").fill("The change is ready for review.");
  await screen.getByRole("button", "Add note").tap();
  await expect(screen.getByText("The change is ready for review.")).toBeVisible();
  await expect(screen.getByText("#02", { exact: true })).toBeVisible();

  await screen.getByLabel("Status", { exact: true }).selectOption({ value: "in_review" });
  await screen.getByRole("button", "Save changes").tap();
  await expect(screen.getByRole("region", "In Review", { exact: true })).toBeVisible();
  await screen.getByRole("button", "Approve").tap();
  await expect(screen.getByText("Approved", { exact: true })).toBeVisible();
  await screen.getByLabel("Status", { exact: true }).selectOption({ value: "in_progress" });
  await screen.getByRole("button", "Save changes").tap();
  await expect(screen.getByRole("button", "Save changes")).toBeDisabled();
  await screen.getByRole("link", "Back to Board").tap();
  await screen.getByRole("button", "List", { exact: true }).tap();
  await screen.getByLabel("Filter by Bot").selectOption({ value: "default" });
  await expect(screen.getByRole("link", /Fix the sign-in flow/)).toBeVisible();
  await expect(screen.getByRole("heading", "Held events", { exact: true })).toHaveCount(0);

  await app.open("/needs-you");
  await expect(screen.getByRole("heading", "Inbox", { exact: true })).toBeVisible();
  await expect(screen.getByRole("textbox", "Task title")).toHaveCount(0);
  await expect(screen.getByRole("link", /Fix the sign-in flow/)).toHaveCount(0);
});

test("opens a collaborator Session from a Board Task", async ({ app, browser, screen }) => {
  await app.open("/board");

  const origin = new URL(await browser.url());

  if (origin.hostname !== "127.0.0.1") throw new Error("Use the local test server");

  const client = createClient({ baseUrl: origin.origin });

  const { data: owner } = await createAgent({
    client,
    body: { name: "Board owner fixture", provider: "faux", modelId: "tool" },
    throwOnError: true,
  });

  const { data: collaborator } = await createAgent({
    client,
    body: { name: "Board reviewer fixture", provider: "faux", modelId: "echo" },
    throwOnError: true,
  });

  const { data: main } = await getMainChat({ client, path: { agentId: owner.id }, throwOnError: true });

  const { data: task } = await createWorkTask({
    client,
    path: { agentId: owner.id },
    body: { title: "Ship a shared Task" },
    throwOnError: true,
  });

  const path = { agentId: owner.id, taskId: task.id };
  await app.open(`/a/${owner.id}/inbox/${task.id}`);
  await expect(screen.getByRole("heading", task.title)).toBeVisible();
  expect(new URL(await browser.url()).pathname).toMatch(/^\/board\/T-\d+$/);

  async function startChild(title: string, bot?: string) {
    await sendMessage({
      client,
      path: { agentId: owner.id, chatId: main.id },
      body: {
        id: randomUUID(),
        workTaskId: task.id,
        text: `session_new ${JSON.stringify({ title, bot, message: "Check the change", provider: "faux", modelId: "echo" })}`,
      },
      throwOnError: true,
    });
    await expect(screen.getByRole("link", title, { exact: true })).toBeVisible();
  }

  await startChild("Implement the change");
  await startChild("Review the change", collaborator.id);
  const sessions = screen.getByRole("region", "Sessions", { exact: true });
  await expect(sessions.getByText(collaborator.name, { exact: true })).toBeVisible();
  await expect(sessions.getByText("Primary", { exact: true })).toHaveCount(1);
  const { data: detail } = await getWorkTask({ client, path, throwOnError: true });
  const review = detail.sessions.find((session) => session.agentId === collaborator.id);

  if (!review) throw new Error("Missing the collaborator Session");
  await screen.getByRole("link", "Review the change", { exact: true }).tap();
  expect(new URL(await browser.url()).pathname).toBe(`/a/${collaborator.id}/c/${review.conversationId}`);
});

test("opens questions and resolves changed results from the global Inbox", async ({
  app,
  browser,
  screen,
}) => {
  await app.open("/");
  const origin = new URL(await browser.url());

  if (origin.hostname !== "127.0.0.1") throw new Error("Use the local test server");
  const client = createClient({ baseUrl: origin.origin });
  client.interceptors.error.use(
    (error, response, request) =>
      new Error(`${request.method} ${request.url}: ${response?.status} ${JSON.stringify(error)}`),
  );

  const { data: bot } = await createAgent({
    client,
    body: { name: "Inbox fixture", provider: "faux", modelId: "tool", environmentId: "local" },
    throwOnError: true,
  });

  const { data: unconfigured } = await createAgent({
    client,
    body: { name: "Inbox setup fixture", provider: "", modelId: "", environmentId: "local" },
    throwOnError: true,
  });

  const routine = {
    schedule: { kind: "daily", time: "09:00" },
    timeZone: "UTC",
    session: "new",
    notification: { mode: "none" },
  } as const;

  const { data: changed } = await createAutomation({
    client,
    path: { agentId: bot.id },
    body: {
      ...routine,
      name: "Inbox changed fixture",
      enabled: false,
      prompt: 'automation_report {"outcome":"changed","summary":"A new item appeared"}',
    },
    throwOnError: true,
  });

  await createAutomation({
    client,
    path: { agentId: unconfigured.id },
    body: { ...routine, name: "Inbox missing model fixture", enabled: true, prompt: "Check inbox" },
    throwOnError: true,
  });
  await runAutomation({ client, path: { agentId: bot.id, automationId: changed.id }, throwOnError: true });
  const { data: main } = await getMainChat({ client, path: { agentId: bot.id }, throwOnError: true });
  await sendMessage({
    client,
    path: { agentId: bot.id, chatId: main.id },
    body: {
      id: randomUUID(),
      text: 'ask_user {"question":"Which color should the bot use?","options":["Blue","Green"]}',
    },
    throwOnError: true,
  });

  await app.open(`/a/${bot.id}`);
  await expect(screen.getByRole("button", "A Blue", { exact: true })).toBeVisible();

  // Allow one 10-second Inbox refresh if the initial read comes before the result.
  await app.open("/inbox");
  await expect(screen.getByRole("link", /^Inbox, 3 items need attention$/)).toBeVisible({ timeout: 15_000 });

  // The tool fixture expects a tool call as input. Use echo for the owner's answer.
  await updateAgent({
    client,
    path: { agentId: bot.id },
    body: { name: bot.name, provider: "faux", modelId: "echo", thinkingLevel: "off", environmentId: "local" },
    throwOnError: true,
  });
  await expect(screen.getByText("Inbox missing model fixture", { exact: true })).toBeVisible();
  await expect(screen.getByRole("link", "Configure model")).toBeVisible();
  await screen.getByRole("link", "Open question").tap();
  await screen.getByRole("button", "A Blue", { exact: true }).tap();
  await screen.getByRole("link", /^Inbox, 2 items need attention$/).tap();
  await expect(screen.getByText("Which color should the bot use?", { exact: true })).toHaveCount(0);
  await screen.getByRole("link", "Inbox changed fixture").tap();
  await expect(screen.getByText("A new item appeared", { exact: true })).toBeVisible();
  await screen.getByRole("button", "Done", { exact: true }).tap();
  await expect(screen.getByRole("link", /^Inbox, 1 item needs attention$/)).toBeVisible();
  await expect(screen.getByRole("link", "Inbox changed fixture")).toHaveCount(0);
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
  await app.open("/board");
  const origin = new URL(await browser.url()).origin;

  if (new URL(origin).hostname !== "127.0.0.1") throw new Error("Use the local test server");
  const repositoryId = randomInt(1_000_000, 1_000_000_000);
  const secret = "local browser test webhook secret only";

  const settings = await fetch(`${origin}/api/plugins/github/webhooks/${repositoryId}`, {
    method: "PUT",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ secret, issueAgentId: "default", holdUnmatched: true }),
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
  await expect(screen.getByText("External", { exact: true })).toBeVisible();
  await screen.getByRole("textbox", "Repository ID").fill(String(repositoryId));
  await screen.getByRole("textbox", "PR number").fill("42");
  await screen.getByRole("textbox", "Commit SHA (optional)").fill("commit-a");
  await screen.getByRole("button", "Link pull request").tap();
  const link = screen.getByText(`pull_request 42 · github.com/repository/${repositoryId}`);
  const waiting = screen.getByText("Waiting review.submitted");
  const received = screen.getByText("Received review.submitted");
  await expect(link).toBeVisible();
  await expect(waiting).toBeVisible();

  async function review(id: number, commit: string, text: string, association = "MEMBER", pull = 42) {
    await deliver("pull_request_review", `review-${id}`, {
      pull_request: { number: pull },
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
  await expect(waiting).toBeVisible();
  await review(3, "commit-a", "Review from outside the repository", "NONE");
  await expect(screen.getByText(/Review from outside the repository/)).toBeVisible();
  await expect(waiting).toBeVisible();
  await review(2, "commit-a", "Review of the selected commit");
  await expect(screen.getByText(/Review of the selected commit/)).toBeVisible();
  await expect(received).toBeVisible();

  await app.open(new URL(await browser.url()).pathname);
  await expect(screen.getByRole("heading", title)).toBeVisible();
  await expect(link).toBeVisible();
  await expect(received).toBeVisible();
  await expect(screen.getByText(/Review of the selected commit/)).toBeVisible();

  await review(4, "commit-c", "Unlinked review for Inbox", "MEMBER", 43);
  await app.open("/inbox");
  await expect(screen.getByRole("heading", "Held events", { exact: true })).toBeVisible();
  await expect(screen.getByText(/Unlinked review for Inbox/)).toBeVisible();
  await screen.getByRole("button", "Dismiss", { exact: true }).tap();
  await expect(screen.getByText(/Unlinked review for Inbox/)).toHaveCount(0);
});
