import type { Channels, Feishu, NotificationTargets } from "../api/generated/types.gen";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { expect, test } from "vite-plus/test";

import {
  getChannelsOptions,
  getFeishuOptions,
  getNotificationTargetsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { AppProvider } from "../lib/app-state";
import { ChannelPage } from "./channels";
import type { ChannelId } from "../components/channel-kit";

const path = { path: { agentId: "default" } };

const connected: Channels = {
  telegram: { enabled: true, botId: "123456", allowedUsers: [42], status: { state: "running" } },
  wechat: { enabled: true, owner: "wx-owner", botId: "wx-bot", status: { state: "running" } },
  feishu: { enabled: true, appId: "cli_app", brand: "lark", paired: true, status: "running", error: "" },
};

const targets: NotificationTargets = {
  primaryId: "telegram:42",
  targets: [
    { id: "telegram:42", platform: "telegram", peer: "telegram:42" },
    { id: "wechat:wx-owner", platform: "wechat", peer: "wechat:wx-owner" },
    { id: "feishu:ou_owner", platform: "feishu", peer: "feishu:ou_owner" },
  ],
};

async function render(channel: ChannelId) {
  const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity } } });
  client.setQueryData(getChannelsOptions(path).queryKey, connected);
  client.setQueryData(getFeishuOptions(path).queryKey, connected.feishu satisfies Feishu);
  client.setQueryData(getNotificationTargetsOptions(path).queryKey, targets);

  const router = createRouter({
    routeTree: createRootRoute({ component: () => createElement(ChannelPage, { channel }) }),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });

  await router.load();

  const html = renderToStaticMarkup(
    createElement(
      QueryClientProvider,
      { client },
      createElement(AppProvider, null, createElement(RouterProvider, { router })),
    ),
  );

  client.clear();

  return html;
}

const sections = ["Connection", "Who can use it", "Notifications", "How to use", "Turn off"];

test.each(["telegram", "wechat", "feishu"] as const)(
  "user sees the same sections on a connected %s page",
  async (channel) => {
    // Given a connected channel with an owner, when its page renders.
    const html = await render(channel);

    // Then the sections come in one order, and the settings switcher stays.
    const at = sections.map((title) => html.indexOf(`>${title}<`));

    expect(at.every((index) => index > 0)).toBe(true);
    expect([...at].sort((a, b) => a - b)).toEqual(at);
    expect(html).toContain('aria-label="Settings pages"');
    expect(html).toContain("/clear");
    expect(html).toContain("Connected");
  },
);

test("user sees which channel owner gets notifications", async () => {
  // Given Telegram as the primary target, when the Telegram and WeChat pages render.
  const telegram = await render("telegram");
  const wechat = await render("wechat");

  // Then Telegram shows the primary badge, and WeChat offers to become primary.
  expect(telegram).toContain(">Primary<");
  expect(telegram).not.toContain(">Make primary<");
  expect(wechat).toContain(">Make primary<");
});

test("user sees the saved Lark app and can replace it", async () => {
  // Given a connected Lark app, when the Feishu page renders.
  const html = await render("feishu");

  // Then the facts name the app, and setup is folded under Replace the app.
  expect(html).toContain("cli_app");
  expect(html).toContain(">Lark<");
  expect(html).toContain(">Replace the app<");
  expect(html).toContain(">Paired<");
});
