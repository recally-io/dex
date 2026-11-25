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
import { InboxPage, InboxResultPage } from "./routes/inbox";
import { BoardPage, WorkTaskPage } from "./routes/work-tasks";
import { Shell } from "./components/shell";
import { AgentLayout } from "./components/agent-layout";
import { ChatPage } from "./routes/chat";
import { AutomationPage, AutomationsPage } from "./routes/automations";
import { SessionsPage, TurnPage } from "./routes/sessions";
import { AgentPage } from "./routes/agent";
import { GeneralSettingsPage } from "./routes/general-settings";
import { ProvidersPage } from "./routes/providers";
import { BotIntegrationsPage, ChannelPage } from "./routes/channels";
import { BotSecretsPage, SkillsPage } from "./routes/extensions";
import { ArchivedPage } from "./routes/archived";
import { GlobalIntegrationsPage, RunnersPage, SharedSecretsPage } from "./routes/environment";
import { AboutPage } from "./routes/about";
import { AppProvider } from "./lib/app-state";
import { FramedPage, Section } from "./components/page";
import { Button, buttonVariants } from "./components/ui/button";
import { applySavedTheme } from "./components/theme";
import { SignIn } from "./components/sign-in";
import { useSignedOut } from "./lib/session";
import "./styles.css";

function Missing({ title, description }: { title: string; description: string }) {
  return (
    <FramedPage title={title} description={description}>
      <Section title="Go back">
        <Link className={buttonVariants({ size: "sm", variant: "secondary" })} to="/">
          Open timeline
        </Link>
      </Section>
    </FramedPage>
  );
}

const root = createRootRoute({
  component: Shell,
  notFoundComponent: () => <Missing title="Page not found" description="dex has no page at this address." />,
});

// The pages of a bot share its tabs. The bot pane draws the main timeline itself.
const agentPages = createRoute({ getParentRoute: () => root, path: "/a/$agentId", component: AgentLayout });

const home = createRoute({ getParentRoute: () => agentPages, path: "/", component: () => null });
const chat = createRoute({ getParentRoute: () => agentPages, path: "/c/$chatId", component: ChatRoute });

function ChatRoute() {
  const { chatId } = chat.useParams();
  const id = Number(chatId);

  return Number.isSafeInteger(id) && id > 0 ? (
    <ChatPage chatId={id} />
  ) : (
    <Missing title="Session not found" description="The session ID in the address is not valid." />
  );
}

const page = <P extends string>(path: P, component: () => React.ReactNode) =>
  createRoute({ getParentRoute: () => root, path, component });

const agentPage = <P extends string>(path: P, component: () => React.ReactNode) =>
  createRoute({ getParentRoute: () => agentPages, path, component });

const agent = agentPage("/agent", AgentPage);
const skills = agentPage("/skills", SkillsPage);
const botIntegrations = agentPage("/integrations", BotIntegrationsPage);
const botSecrets = agentPage("/secrets", BotSecretsPage);

const retiredBotSettings = (
  path: "/instructions" | "/channels" | "/extensions",
  to: "/a/$agentId/agent" | "/a/$agentId/integrations",
  hash?: string,
) =>
  createRoute({
    getParentRoute: () => agentPages,
    path,
    beforeLoad: ({ params, location }) => {
      throw redirect({
        to,
        params: { agentId: params.agentId },
        search: true,
        hash: hash ?? location.hash,
        replace: true,
      });
    },
  });

const instructions = retiredBotSettings("/instructions", "/a/$agentId/agent", "instructions");
const channels = retiredBotSettings("/channels", "/a/$agentId/integrations");

// The MCP OAuth callback still returns to this URL.
const extensions = retiredBotSettings("/extensions", "/a/$agentId/integrations");

const automations = agentPage("/automations", AutomationsPage);

// Keep links to retired global pages usable.
const retired = <P extends string>(path: P, href: string) =>
  createRoute({
    getParentRoute: () => root,
    path,
    beforeLoad: () => {
      throw redirect({ href, replace: true });
    },
  });

const retiredRoutes = [
  retired("/needs-you", "/inbox"),
  retired("/work", "/board"),
  retired("/automations", "/a/default/automations"),
  retired("/secrets", "/settings/secrets"),
  retired("/providers", "/settings/providers"),
  retired("/environment", "/settings/runners"),
  retired("/settings/environment", "/settings/runners"),
  retired("/archived", "/settings/archived"),
  retired("/about", "/settings/about"),
] as const;

const board = page("/board", BoardPage);
const inbox = page("/inbox", InboxPage);

const taskDetail = agentPage("/inbox/$taskId", () => {
  const { agentId, taskId } = taskDetail.useParams();

  return <WorkTaskPage key={taskId} agentId={agentId} taskId={taskId} />;
});

const inboxDetail = page("/inbox/$runId", () => {
  const { runId } = inboxDetail.useParams();

  return <InboxResultPage runId={runId} />;
});

const generalSettings = page("/settings/general", GeneralSettingsPage);
const providers = page("/settings/providers", ProvidersPage);

const runners = page("/settings/runners", RunnersPage);
const sharedSecrets = page("/settings/secrets", SharedSecretsPage);
const globalIntegrations = page("/settings/integrations", GlobalIntegrationsPage);
const archived = page("/settings/archived", ArchivedPage);
const about = page("/settings/about", AboutPage);

const channel = createRoute({
  getParentRoute: () => agentPages,
  path: "/channels/$channelId",
  beforeLoad: ({ params }) => {
    if (params.channelId !== "wechat" && params.channelId !== "telegram" && params.channelId !== "feishu")
      throw notFound();
  },
  component: ChannelRoute,
});

function ChannelRoute() {
  const { channelId } = channel.useParams();

  return (
    <ChannelPage
      key={channelId}
      channel={channelId === "wechat" ? "wechat" : channelId === "feishu" ? "feishu" : "telegram"}
    />
  );
}

// Old addresses from the single settings page.
const legacy = new Map<string, string>([
  ["model", "/agent"],
  ["telegram", "/channels/telegram"],
]);

const settings = createRoute({
  getParentRoute: () => root,
  path: "/settings/$section",
  beforeLoad: ({ params }) => {
    const to = legacy.get(params.section);

    if (!to) throw notFound();
    throw redirect({
      href: `/a/default${to}`,
      replace: true,
    });
  },
});

const defaultSettings = createRoute({
  getParentRoute: () => root,
  path: "/settings",
  beforeLoad: () => {
    throw redirect({ href: "/settings/general", replace: true });
  },
});

const automation = createRoute({
  getParentRoute: () => agentPages,
  path: "/automations/$automationId",
  component: AutomationRoute,
});

function AutomationRoute() {
  const { automationId } = automation.useParams();

  return <AutomationPage key={automationId} automationId={automationId} />;
}

const sessions = agentPage("/sessions", SessionsPage);

const tasks = createRoute({
  getParentRoute: () => root,
  path: "/tasks",
  beforeLoad: () => {
    throw redirect({ href: "/a/default/sessions", replace: true });
  },
});

// A result link and an old task link name a turn. The page opens the session that holds it.
const turn = createRoute({ getParentRoute: () => agentPages, path: "/tasks/$turnId", component: TurnRoute });

function TurnRoute() {
  const { turnId } = turn.useParams();

  return <TurnPage key={turnId} turnId={turnId} />;
}

const oldAgentPage = <P extends string>(path: P) =>
  createRoute({
    getParentRoute: () => root,
    path,
    beforeLoad: ({ location }) => {
      throw redirect({
        href: `/a/default${location.pathname === "/" ? "" : location.pathname}`,
        replace: true,
      });
    },
  });

const oldAgentRoutes = [
  oldAgentPage("/"),
  oldAgentPage("/instructions"),
  oldAgentPage("/channels"),
  oldAgentPage("/channels/$channelId"),
  oldAgentPage("/automations/$automationId"),
  oldAgentPage("/sessions"),
  oldAgentPage("/c/$chatId"),
  oldAgentPage("/tasks/$turnId"),
  oldAgentPage("/agent"),
  oldAgentPage("/extensions"),
] as const;

const router = createRouter({
  defaultPreload: "intent",
  routeTree: root.addChildren([
    agentPages.addChildren([
      home,
      chat,
      sessions,
      taskDetail,
      turn,
      instructions,
      channels,
      channel,
      automations,
      automation,
      agent,
      skills,
      botIntegrations,
      botSecrets,
      extensions,
    ]),
    ...oldAgentRoutes,
    tasks,
    ...retiredRoutes,
    board,
    inbox,
    inboxDetail,
    generalSettings,
    providers,
    runners,
    sharedSecrets,
    globalIntegrations,
    archived,
    about,
    settings,
    defaultSettings,
  ]),
  defaultErrorComponent: ({ error, reset }) => (
    <FramedPage title="Could not open this page" description={errorText(error)}>
      <Section title="What to do" caption="The error can be temporary.">
        <Button size="sm" variant="secondary" onClick={reset}>
          Try again
        </Button>
      </Section>
    </FramedPage>
  ),
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

// The theme class is set before the first paint, so a dark page does not flash light.
applySavedTheme();

function App() {
  return useSignedOut() ? <SignIn /> : <RouterProvider router={router} />;
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <AppProvider>
        <App />
      </AppProvider>
    </QueryClientProvider>
  </StrictMode>,
);
