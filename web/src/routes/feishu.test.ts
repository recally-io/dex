import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { expect, test } from "vite-plus/test";
import { getFeishuOptions, getSettingsOptions } from "../api/generated/@tanstack/react-query.gen";
import { FeishuChannel } from "./feishu";
import { ChannelsPage } from "./channels";

test("user keeps the saved Lark platform when opening settings", async () => {
  // Given saved Lark settings in the real query cache.
  const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity } } });
  client.setQueryData(getFeishuOptions().queryKey, {
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

test("user sees Feishu runtime status in the channels list", async () => {
  // Given an enabled bot that still needs pairing.
  const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity } } });
  client.setQueryData(getSettingsOptions().queryKey, {
    agent: { provider: "", modelId: "", thinkingLevel: "off", instructions: "" },
    providers: [],
    builtins: [],
    apis: {},
    telegram: { enabled: false, allowedUsers: [], status: { state: "off" } },
    wechat: { enabled: false, status: { state: "off" } },
  });
  client.setQueryData(getFeishuOptions().queryKey, {
    brand: "feishu",
    appId: "app",
    enabled: true,
    paired: false,
    status: "needs_login",
    error: "",
  });

  const router = createRouter({
    routeTree: createRootRoute({ component: ChannelsPage }),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });

  await router.load();

  // When the list renders, then its Feishu row shows the runtime state.
  const html = renderToStaticMarkup(
    createElement(QueryClientProvider, { client }, createElement(RouterProvider, { router })),
  );

  expect(html).toContain("needs_login");
  expect(html).not.toContain(">Configure<");

  client.clear();
});
