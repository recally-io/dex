import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClientProvider } from "@tanstack/react-query";
import {
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
  redirect,
  notFound,
  Link,
} from "@tanstack/react-router";
import { errorText, queryClient } from "./lib/queries";
import { Shell } from "./components/shell";
import { ChatPage } from "./routes/chat";
import { AutomationsPage, AutomationPage } from "./routes/automations";
import { AgentPage } from "./routes/agent";
import { ProvidersPage } from "./routes/providers";
import { ChannelPage, ChannelsPage } from "./routes/channels";
import { ExtensionsPage } from "./routes/extensions";
import { SecretsPage } from "./routes/secrets";
import { AboutPage } from "./routes/about";
import { AppProvider } from "./lib/app-state";
import { Page, PageHeader } from "./components/page";
import { TooltipProvider } from "./components/ui/tooltip";
import "./styles.css";

const root = createRootRoute({
  component: Shell,
  notFoundComponent: () => (
    <Page>
      <PageHeader title="Page not found" description="dex has no page at this address." />
      <Link className="text-sm text-brand underline" to="/">
        Start a new chat
      </Link>
    </Page>
  ),
});

const home = createRoute({ getParentRoute: () => root, path: "/", component: () => <ChatPage /> });
const chat = createRoute({ getParentRoute: () => root, path: "/c/$chatId", component: ChatRoute });

function ChatRoute() {
  const { chatId } = chat.useParams();
  const id = Number(chatId);

  return Number.isSafeInteger(id) && id > 0 ? (
    <ChatPage chatId={id} />
  ) : (
    <Page>
      <PageHeader title="Chat not found" description="The chat ID in the address is not valid." />
    </Page>
  );
}

const page = <P extends string>(path: P, component: () => React.ReactNode) =>
  createRoute({ getParentRoute: () => root, path, component });

const agent = page("/agent", AgentPage);
const providers = page("/providers", ProvidersPage);
const channels = page("/channels", ChannelsPage);
const extensions = page("/extensions", ExtensionsPage);
const secrets = page("/secrets", SecretsPage);
const about = page("/about", AboutPage);

const channel = createRoute({
  getParentRoute: () => root,
  path: "/channels/$channelId",
  beforeLoad: ({ params }) => {
    if (params.channelId !== "web" && params.channelId !== "telegram") throw notFound();
  },
  component: ChannelRoute,
});

function ChannelRoute() {
  const { channelId } = channel.useParams();

  return <ChannelPage key={channelId} channel={channelId === "web" ? "web" : "telegram"} />;
}

// Old addresses from the single settings page.
const legacy = new Map<string, string>([
  ["model", "/agent"],
  ["providers", "/providers"],
  ["telegram", "/channels/telegram"],
  ["about", "/about"],
]);

const settings = createRoute({
  getParentRoute: () => root,
  path: "/settings/$section",
  beforeLoad: ({ params }) => {
    const to = legacy.get(params.section);

    if (!to) throw notFound();
    throw redirect({ href: to, replace: true });
  },
});

const defaultSettings = createRoute({
  getParentRoute: () => root,
  path: "/settings",
  beforeLoad: () => {
    throw redirect({ to: "/agent", replace: true });
  },
});

const automations = createRoute({
  getParentRoute: () => root,
  path: "/automations",
  component: AutomationsPage,
});

const automation = createRoute({
  getParentRoute: () => root,
  path: "/automations/$automationId",
  component: AutomationRoute,
});

function AutomationRoute() {
  const { automationId } = automation.useParams();

  return <AutomationPage key={automationId} automationId={automationId} />;
}

const router = createRouter({
  routeTree: root.addChildren([
    home,
    chat,
    automations,
    automation,
    agent,
    providers,
    channels,
    channel,
    extensions,
    secrets,
    about,
    settings,
    defaultSettings,
  ]),
  defaultErrorComponent: ({ error, reset }) => (
    <Page>
      <PageHeader title="Could not open this page" description={errorText(error)} />
      <button onClick={reset} className="text-sm text-brand underline">
        Try again
      </button>
    </Page>
  ),
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <AppProvider>
        <TooltipProvider>
          <RouterProvider router={router} />
        </TooltipProvider>
      </AppProvider>
    </QueryClientProvider>
  </StrictMode>,
);
