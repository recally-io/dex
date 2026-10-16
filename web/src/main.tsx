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
import { AutomationPage } from "./routes/automations";
import { TasksPage, TaskPage } from "./routes/tasks";
import { AgentPage } from "./routes/agent";
import { ProvidersPage } from "./routes/providers";
import { ChannelPage } from "./routes/channels";
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

const home = createRoute({ getParentRoute: () => root, path: "/", component: () => <ChatPage /> });
const chat = createRoute({ getParentRoute: () => root, path: "/c/$chatId", component: ChatRoute });

function ChatRoute() {
  const { chatId } = chat.useParams();
  const id = Number(chatId);

  return Number.isSafeInteger(id) && id > 0 ? (
    <ChatPage chatId={id} />
  ) : (
    <Missing title="Chat not found" description="The chat ID in the address is not valid." />
  );
}

const page = <P extends string>(path: P, component: () => React.ReactNode) =>
  createRoute({ getParentRoute: () => root, path, component });

const agent = page("/agent", AgentPage);
const providers = page("/providers", ProvidersPage);

const channels = createRoute({
  getParentRoute: () => root,
  path: "/channels",
  beforeLoad: () => {
    throw redirect({ to: "/agent", replace: true });
  },
});

const extensions = page("/extensions", ExtensionsPage);
const secrets = page("/secrets", SecretsPage);
const about = page("/about", AboutPage);

const channel = createRoute({
  getParentRoute: () => root,
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
  beforeLoad: () => {
    throw redirect({ to: "/agent", replace: true });
  },
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

const tasks = page("/tasks", TasksPage);
const task = createRoute({ getParentRoute: () => root, path: "/tasks/$taskId", component: TaskRoute });

function TaskRoute() {
  const { taskId } = task.useParams();

  return <TaskPage key={taskId} taskId={taskId} />;
}

const router = createRouter({
  routeTree: root.addChildren([
    home,
    chat,
    tasks,
    task,
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
