import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClientProvider } from "@tanstack/react-query";
import {
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
  redirect,
  Link,
} from "@tanstack/react-router";
import { errorText, queryClient } from "./lib/queries";
import { Shell } from "./components/shell";
import { ChatPage } from "./routes/chat";
import { AutomationsPage, AutomationPage } from "./routes/automations";
import { SettingsPage } from "./routes/settings";
import { TooltipProvider } from "./components/ui/tooltip";
import "./styles.css";

const root = createRootRoute({
  component: Shell,
  notFoundComponent: () => (
    <div className="p-10">
      <h1 className="text-xl font-semibold">Page not found</h1>
      <Link className="mt-4 inline-block text-brand underline" to="/">
        Start a new chat
      </Link>
    </div>
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
    <div role="alert" className="p-10">
      Invalid chat ID.
    </div>
  );
}

const settings = createRoute({
  getParentRoute: () => root,
  path: "/settings/$section",
  component: SettingsRoute,
});

function SettingsRoute() {
  const { section } = settings.useParams();

  return <SettingsPage section={section} />;
}

const defaultSettings = createRoute({
  getParentRoute: () => root,
  path: "/settings",
  beforeLoad: () => {
    throw redirect({ to: "/settings/$section", params: { section: "providers" } });
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
  routeTree: root.addChildren([home, chat, settings, defaultSettings, automations, automation]),
  defaultErrorComponent: ({ error, reset }) => (
    <div className="p-10">
      <h1 className="mb-3 font-semibold">Could not open this page</h1>
      <p className="mb-4 text-ink-2">{errorText(error)}</p>
      <button onClick={reset} className="text-brand underline">
        Try again
      </button>
    </div>
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
      <TooltipProvider>
        <RouterProvider router={router} />
      </TooltipProvider>
    </QueryClientProvider>
  </StrictMode>,
);
