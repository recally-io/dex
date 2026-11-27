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
import { BoardResultPage } from "./components/board-results";
import { BoardPage } from "./routes/board";
import { IntegrationsPage, type IntegrationsSearch } from "./routes/integrations";
import { TaskDetailPage } from "./routes/task-detail";
import { Shell } from "./components/shell";
import { BotLayout } from "./components/bot-layout";
import { SessionPage } from "./routes/session";
import { AutomationPage, AutomationsPage } from "./routes/automations";
import { SessionsPage } from "./routes/sessions";
import { BotPage } from "./routes/bot";
import { GeneralSettingsPage } from "./routes/general-settings";
import { ProvidersPage } from "./routes/providers";
import { BotChannelsPage, ChannelPage } from "./routes/channels";
import { BotIntegrationsPage, BotSecretsPage, type IntegrationSection } from "./routes/skills";
import { ArchivedPage } from "./routes/archived";
import { GlobalIntegrationsPage, RunnersPage, SharedSecretsPage } from "./routes/runners";
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
const botPages = createRoute({ getParentRoute: () => root, path: "/b/$botId", component: BotLayout });

const home = createRoute({ getParentRoute: () => botPages, path: "/", component: () => null });

const session = createRoute({
  getParentRoute: () => botPages,
  path: "/s/$sessionId",
  component: SessionRoute,
});

function SessionRoute() {
  const { sessionId } = session.useParams();
  const id = Number(sessionId);

  return Number.isSafeInteger(id) && id > 0 ? (
    <SessionPage sessionId={id} />
  ) : (
    <Missing title="Session not found" description="The session ID in the address is not valid." />
  );
}

const page = <P extends string>(path: P, component: () => React.ReactNode) =>
  createRoute({ getParentRoute: () => root, path, component });

const botPage = <P extends string>(path: P, component: () => React.ReactNode) =>
  createRoute({ getParentRoute: () => botPages, path, component });

const botSettings = botPage("/bot", BotPage);

const skills = createRoute({
  getParentRoute: () => botPages,
  path: "/skills",
  beforeLoad: ({ params }) => {
    throw redirect({ to: "/b/$botId/integrations", params, search: { section: "skills" }, replace: true });
  },
});

function sectionSearch({ section }: { section?: unknown }): { section?: IntegrationSection } {
  return section === "skills" || section === "mcp" ? { section } : {};
}

const botIntegrations = createRoute({
  getParentRoute: () => botPages,
  path: "/integrations",
  validateSearch: sectionSearch,
  component: () => <BotIntegrationsPage section={botIntegrations.useSearch().section} />,
});

const botChannels = botPage("/channels", BotChannelsPage);
const botSecrets = botPage("/secrets", BotSecretsPage);

const automations = botPage("/automations", AutomationsPage);

const board = createRoute({
  getParentRoute: () => root,
  path: "/board",
  // The router parses the query string before it checks the Board search fields.
  validateSearch: (search: { bot?: unknown }) => {
    // eslint-disable-next-line anti-slop/no-runtime-typeof
    if (typeof search.bot === "string" && search.bot) return { bot: search.bot };

    return {};
  },
  component: () => {
    const { bot } = board.useSearch();
    const navigate = board.useNavigate();

    return <BoardPage bot={bot} onBotChange={(id) => void navigate({ search: { bot: id } })} />;
  },
});

const integrations = createRoute({
  getParentRoute: () => root,
  path: "/integrations",
  validateSearch: (search: { section?: unknown; add?: unknown; target?: unknown }): IntegrationsSearch => ({
    ...sectionSearch(search),
    add: search.add === "mcp" || search.add === "skill" ? search.add : undefined,
    // The router parses JSON in the URL. Keep an invalid target distinct from an absent target.
    // eslint-disable-next-line anti-slop/no-runtime-typeof
    target: typeof search.target === "string" ? search.target : search.target === undefined ? undefined : "",
  }),
  component: IntegrationsRoute,
});

function IntegrationsRoute() {
  const { section, add, target } = integrations.useSearch();
  const navigate = integrations.useNavigate();

  return (
    <IntegrationsPage
      section={section}
      add={add}
      target={target}
      onAdd={(kind) =>
        void navigate({ search: { section: kind === "skill" ? "skills" : "mcp", add: kind, target } })
      }
      onClose={() => void navigate({ search: { section }, replace: true })}
    />
  );
}

const taskDetail = page("/board/$taskKey", () => {
  const { taskKey } = taskDetail.useParams();

  return <TaskDetailPage key={taskKey} taskKey={taskKey} />;
});

const boardResult = page("/board/results/$runId", () => {
  const { runId } = boardResult.useParams();

  return <BoardResultPage runId={runId} />;
});

const generalSettings = page("/settings/general", GeneralSettingsPage);
const providers = page("/settings/providers", ProvidersPage);

const runners = page("/settings/runners", RunnersPage);
const sharedSecrets = page("/settings/secrets", SharedSecretsPage);
const globalIntegrations = page("/settings/integrations", GlobalIntegrationsPage);
const archived = page("/settings/archived", ArchivedPage);
const about = page("/settings/about", AboutPage);

const channel = createRoute({
  getParentRoute: () => botPages,
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

const defaultSettings = createRoute({
  getParentRoute: () => root,
  path: "/settings",
  beforeLoad: () => {
    throw redirect({ href: "/settings/general", replace: true });
  },
});

const automation = createRoute({
  getParentRoute: () => botPages,
  path: "/automations/$automationId",
  component: AutomationRoute,
});

function AutomationRoute() {
  const { automationId } = automation.useParams();

  return <AutomationPage key={automationId} automationId={automationId} />;
}

const sessions = botPage("/sessions", SessionsPage);

const index = createRoute({
  getParentRoute: () => root,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/b/$botId", params: { botId: "default" }, replace: true });
  },
});

const router = createRouter({
  defaultPreload: "intent",
  routeTree: root.addChildren([
    botPages.addChildren([
      home,
      session,
      sessions,
      channel,
      automations,
      automation,
      botSettings,
      skills,
      botIntegrations,
      botChannels,
      botSecrets,
    ]),
    index,
    board,
    integrations,
    taskDetail,
    boardResult,
    generalSettings,
    providers,
    runners,
    sharedSecrets,
    globalIntegrations,
    archived,
    about,
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
