import { createHmac, randomInt, randomUUID } from "node:crypto";
import { test } from "@e2e-dev/web";
import { expect } from "e2e";
import { createClient } from "../src/api/generated/client";
import {
  createAgent,
  createAutomation,
  createTask,
  splitTask,
  getBoardAttention,
  getMainSession,
  getTask,
  listHeldEvents,
  runAutomation,
  sendMessage,
  updateAgent,
  updateTask,
} from "../src/api/generated/sdk.gen";

// Tests share a server. Snapshot existing attention before this test adds any work.
async function attentionBaseline(client: ReturnType<typeof createClient>) {
  const [{ data: board }, { data: held }] = await Promise.all([
    getBoardAttention({ client, throwOnError: true }),
    listHeldEvents({ client, throwOnError: true }),
  ]);

  return (
    board.sessions.filter((session) => session.question).length +
    board.taskReviews.length +
    board.results.length +
    board.automations.filter((automation) => automation.setup).length +
    held.total
  );
}

function boardLabel(count: number) {
  return count === 0 ? "Board" : `Board, ${count} ${count === 1 ? "item needs" : "items need"} attention`;
}

test("creates, updates, and reviews Tasks from the unified Board", async ({ app, screen }) => {
  await app.open("/board");
  await expect(screen.getByRole("heading", "Board", { exact: true })).toBeVisible();
  await screen.getByRole("textbox", "Task title").fill("Fix the sign-in flow");
  await screen.getByRole("button", "Add", { exact: true }).tap();
  await screen.getByRole("link", /Fix the sign-in flow/).tap();
  await expect(screen.getByRole("heading", "Fix the sign-in flow")).toBeVisible();

  await screen.getByRole("textbox", "Task note").fill("The change is ready for review.");
  await screen.getByRole("button", "Note only", { exact: true }).tap();
  await expect(screen.getByText("The change is ready for review.")).toBeVisible();

  await screen.getByRole("button", "Reviewer: None", { exact: true }).tap();
  await screen.getByLabel("Reviewer", { exact: true }).selectOption({ value: "owner" });
  await screen.getByRole("button", "Save", { exact: true }).tap();
  await expect(screen.getByRole("button", "Reviewer: Owner", { exact: true })).toBeVisible();

  const status = screen.getByRole("button", "Status", { exact: true });
  await status.tap();
  await screen.getByRole("button", "In Progress", { exact: true }).tap();
  await status.tap();
  await screen.getByRole("button", "In Review", { exact: true }).tap();
  await expect(screen.getByRole("region", "In Review", { exact: true })).toBeVisible();
  await screen.getByRole("button", "Approve", { exact: true }).tap();
  await expect(screen.getByText(/approved/i)).toBeVisible();

  await status.tap();
  await screen.getByRole("button", "In Progress", { exact: true }).tap();
  await screen.getByRole("link", "← Board", { exact: true }).tap();
  await expect(screen.getByRole("link", /Fix the sign-in flow/)).toBeVisible();
});

test("unblocks a subtask by approving its dependency from Board", async ({ app, browser, screen }) => {
  await app.open("/board");

  const origin = new URL(await browser.url());

  if (origin.hostname !== "127.0.0.1") throw new Error("Use the local test server");

  const client = createClient({ baseUrl: origin.origin });

  const baseline = await attentionBaseline(client);

  const { data: reviewerBot } = await createAgent({
    client,
    body: { name: "Dependency fixture", provider: "faux", modelId: "echo" },
    throwOnError: true,
  });

  const { data: parent } = await createTask({
    client,
    body: { title: "Release a build", assigneeBotId: "default" },
    throwOnError: true,
  });

  const { data: upstream } = await splitTask({
    client,
    path: { taskKey: parent.key },
    body: {
      title: "Verify the build",
      assigneeBotId: reviewerBot.id,
      status: "in_progress",
      reviewer: "owner",
    },
    throwOnError: true,
  });

  await app.open(`/board/${parent.key}`);
  await screen.getByRole("button", "Task actions", { exact: true }).tap();
  await screen.getByRole("menuitem", "Add subtask", { exact: true }).tap();
  await screen.getByRole("textbox", "Subtask title").fill("Publish the build");
  await screen.getByRole("button", "Add subtask", { exact: true }).tap();
  await screen.getByRole("link", /Publish the build/).tap();
  const publishPath = new URL(await browser.url()).pathname;

  await screen.getByRole("button", "Task actions", { exact: true }).tap();
  await screen.getByRole("menuitem", "Add dependency", { exact: true }).tap();
  await screen.getByLabel("Sibling task").selectOption({ value: upstream.key });
  await screen.getByRole("button", "Add dependency", { exact: true }).tap();
  await expect(screen.getByText("Blocked", { exact: true })).toBeVisible();

  await app.open("/board");
  const blocked = screen.getByRole("region", "Blocked", { exact: true });
  await expect(blocked.getByRole("link", /Publish the build/)).toBeVisible();
  await expect(screen.getByRole("link", boardLabel(baseline), { exact: true })).toBeVisible();

  await updateTask({
    client,
    path: { taskKey: upstream.key },
    body: { status: "in_review" },
    throwOnError: true,
  });
  const needsYou = screen.getByRole("region", "Needs you", { exact: true });
  // Review from another client appears before the 10-second polling interval.
  await expect(needsYou.getByRole("link", /Verify the build/)).toBeVisible({ timeout: 4000 });
  await expect(screen.getByRole("link", boardLabel(baseline + 1), { exact: true })).toBeVisible();
  const reviewStatus = screen.getByRole("link", `${reviewerBot.name}, Needs attention`, { exact: true });
  await expect(reviewStatus).toBeVisible();
  await needsYou.getByRole("button", "Approve", { exact: true }).tap();
  await expect(needsYou.getByRole("link", /Verify the build/)).toHaveCount(0);
  await expect(screen.getByRole("link", boardLabel(baseline), { exact: true })).toBeVisible();
  await expect(reviewStatus).toHaveCount(0);

  await app.open(publishPath);
  await expect(screen.getByRole("heading", "Publish the build")).toBeVisible();
  await expect(screen.getByText("Blocked", { exact: true })).toHaveCount(0);
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

  const { data: main } = await getMainSession({ client, path: { agentId: owner.id }, throwOnError: true });

  const { data: task } = await createTask({
    client,
    body: { title: "Ship a shared Task", assigneeBotId: owner.id },
    throwOnError: true,
  });

  const path = { taskKey: task.key };

  await app.open(`/board/${task.key}`);
  await expect(screen.getByRole("heading", task.title)).toBeVisible();

  async function startChild(title: string, bot?: string) {
    await sendMessage({
      client,
      path: { agentId: owner.id, sessionId: main.id },
      body: {
        id: randomUUID(),
        taskId: task.key,
        text: `session_new ${JSON.stringify({ title, bot, message: "Check the change", provider: "faux", modelId: "echo" })}`,
      },
      throwOnError: true,
    });
  }

  await startChild("Implement the change");
  await startChild("Review the change", collaborator.id);
  const sessions = screen.getByRole("region", "Sessions", { exact: true });
  await expect(sessions.getByText(collaborator.name, { exact: true })).toBeVisible({ timeout: 15_000 });
  await expect(sessions.getByText("Primary", { exact: true })).toHaveCount(1);
  const { data: detail } = await getTask({ client, path, throwOnError: true });
  const review = detail.sessions.find((session) => session.agentId === collaborator.id);

  if (!review) throw new Error("Missing the collaborator Session");
  await screen.getByRole("link", "Review the change", { exact: true }).tap();
  expect(new URL(await browser.url()).pathname).toBe(`/a/${collaborator.id}/s/${review.sessionId}`);
});

test("answers questions and resolves automation updates directly from Board", async ({
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

  const baseline = await attentionBaseline(client);

  const { data: bot } = await createAgent({
    client,
    body: { name: "Board fixture", provider: "faux", modelId: "tool", runnerId: "local" },
    throwOnError: true,
  });

  const { data: unconfigured } = await createAgent({
    client,
    body: { name: "Board setup fixture", provider: "", modelId: "", runnerId: "local" },
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
      name: "Board changed fixture",
      enabled: false,
      prompt: 'automation_report {"outcome":"changed","summary":"A new item appeared"}',
    },
    throwOnError: true,
  });

  await createAutomation({
    client,
    path: { agentId: unconfigured.id },
    body: { ...routine, name: "Board missing model fixture", enabled: true, prompt: "Check board" },
    throwOnError: true,
  });
  await runAutomation({ client, path: { agentId: bot.id, automationId: changed.id }, throwOnError: true });
  const { data: main } = await getMainSession({ client, path: { agentId: bot.id }, throwOnError: true });
  await sendMessage({
    client,
    path: { agentId: bot.id, sessionId: main.id },
    body: {
      id: randomUUID(),
      text: 'ask_user {"question":"Which color should the bot use?","options":["Blue","Green"]}',
    },
    throwOnError: true,
  });

  await app.open(`/a/${bot.id}`);
  await expect(screen.getByRole("button", "A Blue", { exact: true })).toBeVisible();

  // Allow one 10-second attention refresh if the initial read comes before the result.
  await app.open("/board");

  const update = screen
    .getByRole("region", "Updates", { exact: true })
    .getByRole("button", /^Board changed fixture/);

  await expect(update).toBeVisible({ timeout: 15_000 });
  await expect(screen.getByRole("link", boardLabel(baseline + 3), { exact: true })).toBeVisible();

  // The tool fixture expects a tool call as input. Use echo for the owner's answer.
  await updateAgent({
    client,
    path: { agentId: bot.id },
    body: { name: bot.name, provider: "faux", modelId: "echo", thinkingLevel: "off", runnerId: "local" },
    throwOnError: true,
  });
  await expect(screen.getByText("Board missing model fixture", { exact: true })).toBeVisible();
  await expect(screen.getByRole("link", "Configure model")).toBeVisible();

  await screen.getByRole("button", "Blue", { exact: true }).tap();
  await expect(screen.getByText("Which color should the bot use?", { exact: true })).toHaveCount(0);
  await expect(screen.getByRole("link", boardLabel(baseline + 2), { exact: true })).toBeVisible();

  await update.tap();
  await expect(screen.getByText("A new item appeared", { exact: true })).toBeVisible();
  await screen.getByRole("button", "Done", { exact: true }).tap();
  await expect(screen.getByRole("link", boardLabel(baseline + 1), { exact: true })).toBeVisible();
  await expect(screen.getByText("Board changed fixture", { exact: true })).toHaveCount(0);
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

  await screen.getByRole("button", "Task actions", { exact: true }).tap();
  await screen.getByRole("menuitem", "Link GitHub PR", { exact: true }).tap();
  await screen.getByRole("textbox", "Repository ID").fill(String(repositoryId));
  await screen.getByRole("textbox", "PR number").fill("42");
  await screen.getByRole("textbox", "Commit SHA (optional)").fill("commit-a");
  await screen.getByRole("button", "Link pull request").tap();
  const link = screen.getByText(`pull_request 42 · github.com/repository/${repositoryId}`);
  const waiting = screen.getByText(/Waiting review\.submitted/);
  const received = screen.getByText(/Received review\.submitted/);
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

  await review(4, "commit-c", "Unlinked review for Board", "MEMBER", 43);
  await app.open("/board");
  await expect(screen.getByText(/Unlinked review for Board/)).toBeVisible();
  await screen.getByRole("button", "Dismiss", { exact: true }).tap();
  await expect(screen.getByText(/Unlinked review for Board/)).toHaveCount(0);
});
