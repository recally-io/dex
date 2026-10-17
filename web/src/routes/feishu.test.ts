import { scopedSettingsOptions } from "../lib/agent-scope";
import type { AgentSettings as Settings } from "../lib/agent-scope";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { expect, test } from "vite-plus/test";

import { getFeishuOptions, listChatsOptions } from "../api/generated/@tanstack/react-query.gen";
import { FeishuChannel } from "./feishu";
import { ChannelsSection } from "./channels";

test("user keeps the saved Lark platform when opening settings", async () => {
  // Given saved Lark settings in the real query cache.
  const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity } } });
  client.setQueryData(getFeishuOptions({ path: { agentId: "default" } }).queryKey, {
    brand: "lark",
    appId: "lark-app",
    enabled: true,
    paired: true,
    status: "running",
    error: "",
  });

  const router = createRouter({
    routeTree: createRootRoute({ component: FeishuChannel }),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });

  await router.load();

  // When the settings page renders, then Lark is the selected form value.
  const html = renderToStaticMarkup(
    createElement(QueryClientProvider, { client }, createElement(RouterProvider, { router })),
  );

  expect(html).toContain('<option value="lark" selected="">Lark</option>');
  expect(html).not.toContain('<option value="feishu" selected="">');

  client.clear();
});

test("user sees Feishu status and shared main context without a peer link", async () => {
  // Given an enabled bot that still needs pairing.
  const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity } } });
  client.setQueryData(scopedSettingsOptions("default").queryKey, {
    agent: { name: "Dex", provider: "", modelId: "", thinkingLevel: "off", instructions: "" },
    providers: [],
    builtins: [],
    apis: {},
    feishu: { enabled: false, appId: "", brand: "feishu", paired: false, status: "off", error: "" },
    telegram: { enabled: false, allowedUsers: [], status: { state: "off" } },
    wechat: { enabled: false, status: { state: "off" } },
  });
  client.setQueryData(getFeishuOptions({ path: { agentId: "default" } }).queryKey, {
    brand: "feishu",
    appId: "app",
    enabled: true,
    paired: false,
    status: "needs_login",
    error: "",
  });
  client.setQueryData(listChatsOptions({ path: { agentId: "default" } }).queryKey, {
    chats: [
      { id: 2, channel: "feishu" as const, createdAt: 10, updatedAt: 40 },
      { id: 3, channel: "feishu" as const, createdAt: 20, updatedAt: 20 },
      { id: 4, channel: "web" as const, createdAt: 30, updatedAt: 30 },
    ].map((chat) => ({
      ...chat,
      main: false,
      readOnly: true,
      title: "Saved chat",
      busy: false,
      settings: {},
    })),
  });

  const router = createRouter({
    routeTree: createRootRoute({
      component: () =>
        createElement(ChannelsSection, {
          settings: client.getQueryData<Settings>(scopedSettingsOptions("default").queryKey)!,
        }),
    }),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });

  await router.load();

  // Then the row shows setup and status without a link to an old peer conversation.
  const html = renderToStaticMarkup(
    createElement(QueryClientProvider, { client }, createElement(RouterProvider, { router })),
  );

  expect(html).toContain("needs_login");
  expect(html).not.toContain(">Configure<");
  expect(html).not.toContain('href="/c/3"');
  expect(html).not.toContain('href="/c/2"');
  expect(html).not.toContain('href="/c/4"');
  expect(html).not.toContain("Open conversation");
  expect(html).toContain("Channels share the main timeline.");

  client.clear();
});
