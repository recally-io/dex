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
import { AgentLayout } from "./components/agent-layout";
import { ChatPage } from "./routes/chat";
import { AutomationPage, AutomationsPage } from "./routes/automations";
import { SessionsPage, TaskPage } from "./routes/sessions";
import { AgentPage, InstructionsPage } from "./routes/agent";
import { ProvidersPage } from "./routes/providers";
import { ChannelPage, ChannelsPage } from "./routes/channels";
import { ExtensionsPage } from "./routes/extensions";
import { SecretsPage } from "./routes/secrets";
import { AboutPage } from "./routes/about";
import { AppProvider } from "./lib/app-state";
import { FramedPage, Section } from "./components/page";
import { Button, buttonVariants } from "./components/atoms/Button";
import { applySavedTheme } from "./components/theme";
import { SignIn } from "./components/sign-in";
import { useSignedOut } from "./lib/session";
import "./styles.css";

function Missing({ title, description }: { title: string; description: string }) {
  return (
    <FramedPage title={title} description={description}>
      <Section n={1} title="Go back">
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
const instructions = agentPage("/instructions", InstructionsPage);
const channels = agentPage("/channels", ChannelsPage);
const automations = agentPage("/automations", AutomationsPage);
const providers = page("/providers", ProvidersPage);

const extensions = agentPage("/extensions", ExtensionsPage);
const secrets = page("/secrets", SecretsPage);
const about = page("/about", AboutPage);

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
    throw redirect({
      href: to.startsWith("/channels") || to === "/agent" ? `/a/default${to}` : to,
      replace: true,
    });
  },
});

const defaultSettings = createRoute({
  getParentRoute: () => root,
  path: "/settings",
  beforeLoad: () => {
    throw redirect({ href: "/a/default/agent", replace: true });
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

const task = createRoute({ getParentRoute: () => agentPages, path: "/tasks/$taskId", component: TaskRoute });

function TaskRoute() {
  const { taskId } = task.useParams();

  return <TaskPage key={taskId} taskId={taskId} />;
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
  oldAgentPage("/automations"),
  oldAgentPage("/automations/$automationId"),
  oldAgentPage("/sessions"),
  oldAgentPage("/c/$chatId"),
  oldAgentPage("/tasks/$taskId"),
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
      task,
      instructions,
      channels,
      channel,
      automations,
      automation,
      agent,
      extensions,
    ]),
    ...oldAgentRoutes,
    tasks,
    providers,
    secrets,
    about,
    settings,
    defaultSettings,
  ]),
  defaultErrorComponent: ({ error, reset }) => (
    <FramedPage title="Could not open this page" description={errorText(error)}>
      <Section n={1} title="What to do" caption="The error can be temporary.">
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
