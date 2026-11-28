import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { afterEach, expect, test, vi } from "vite-plus/test";
import { Sidebar } from "./sidebar";
import { WorkspaceSettings } from "./workspace-settings";

const nativeUA = "Mozilla/5.0 DexWorkspace/1";
const settingsHref = 'href="dex-workspace://settings"';

afterEach(() => vi.unstubAllGlobals());

async function renderSidebar() {
  const client = new QueryClient({ defaultOptions: { queries: { enabled: false } } });

  const router = createRouter({
    routeTree: createRootRoute({
      component: () =>
        createElement(Sidebar, {
          path: "/",
          statuses: new Map(),
          attentionCount: 0,
          lead: null,
          onSearch: () => {},
        }),
    }),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });

  await router.load();

  return renderToStaticMarkup(
    createElement(QueryClientProvider, { client }, createElement(RouterProvider, { router })),
  );
}

test.each([
  ["dex-local://workspace", "workspace"],
  ["https://local.dex.internal", "local.dex.internal"],
])("user sees a friendly name for the native local workspace at %s", async (origin, host) => {
  vi.stubGlobal("navigator", { userAgent: nativeUA });
  vi.stubGlobal("window", { location: { origin, host } });

  const settings = renderToStaticMarkup(createElement(WorkspaceSettings));
  const sidebar = await renderSidebar();

  expect(settings).toContain("Local Workspace · On this device");
  expect(settings).not.toContain(origin);
  expect(settings).toContain(settingsHref);
  expect(sidebar).toContain('aria-label="Switch workspace, Local Workspace"');
  expect(sidebar).toContain(">On this device<");
  expect(sidebar).toContain(settingsHref);
  const workspaceNavigation = sidebar.indexOf('<nav aria-label="Workspace"');
  expect(workspaceNavigation).toBeGreaterThan(-1);
  const endOfNavigation = sidebar.indexOf("</nav>", workspaceNavigation);
  expect(endOfNavigation).toBeGreaterThan(workspaceNavigation);
  expect(sidebar.indexOf(settingsHref)).toBeGreaterThan(endOfNavigation);
});

test.each([
  ["https://dex.example.com:8443", "dex.example.com:8443", "dex.example.com:8443"],
  ["null", "", "null"],
])("user sees the remote workspace label for origin %s", async (origin, host, label) => {
  vi.stubGlobal("navigator", { userAgent: nativeUA });
  vi.stubGlobal("window", { location: { origin, host } });

  const sidebar = await renderSidebar();
  const settings = renderToStaticMarkup(createElement(WorkspaceSettings));

  expect(sidebar).toContain(`aria-label="Switch workspace, ${label}"`);
  expect(sidebar).toContain(">Remote server<");
  expect(sidebar).toContain(settingsHref);
  expect(settings).toContain(`${origin} · Remote server`);
});

test.each(["Mozilla/5.0", "Mozilla/5.0 DexWorkspace/10"])(
  "user has no switcher without the exact native user agent token: %s",
  async (userAgent) => {
    vi.stubGlobal("navigator", { userAgent });
    vi.stubGlobal("window", { location: new URL("https://dex.example.com") });

    const sidebar = await renderSidebar();

    expect(sidebar).toContain(">Settings");
    expect(sidebar).not.toContain(settingsHref);
    expect(renderToStaticMarkup(createElement(WorkspaceSettings))).toBe("");
  },
);
