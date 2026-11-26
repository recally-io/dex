import type { BotSettings as Settings } from "../lib/bot-scope";
import { BotContext, useBotSettings, useBotId } from "../lib/bot-scope";
import { Link, Outlet, useParams, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";

import {
  getSessionOptions,
  getMainSessionOptions,
  listBotsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { SessionTitle, MainTimeline } from "../routes/session";
import { Artifacts } from "./bot/artifacts";
import { BotNavigation } from "./bot-navigation";
import { BotAvatar } from "./bot-avatar";
import { Failure, Loading } from "./feedback";
import { InPaneContext, MainPane, MenuButton } from "./page";
import { cn } from "../lib/utils";

/** The name of each page under a bot that is not a session. */
const pageNames = new Map([
  ["automations", "Automations"],
  ["bot", "Settings"],
  ["skills", "Settings"],
  ["integrations", "Settings"],
  ["secrets", "Settings"],
  ["channels", "Settings"],
  ["sessions", "Sessions"],
]);

/**
 * The bot header holds navigation and task controls. The main timeline stays mounted under the
 * other pages, so its stream and the
 * unsent message survive a visit to them.
 */
export function BotLayout() {
  const { botId = "default" } = useParams({ strict: false });

  return (
    <BotContext.Provider key={botId} value={botId}>
      <BotView />
    </BotContext.Provider>
  );
}

function BotView() {
  const botId = useBotId();
  const bots = useQuery(listBotsOptions());
  const missing = bots.data && !bots.data.bots.some((bot) => bot.id === botId);
  const settings = useBotSettings(botId);

  return missing ? (
    <MainPane>
      <div className="p-8">
        <h1 id="page-title" className="text-xl font-semibold">
          Bot not found
        </h1>
        <p className="mt-2 text-muted-foreground">Dex has no bot with this ID.</p>
        <Link to="/" className="mt-4 block underline">
          Open Dex
        </Link>
      </div>
    </MainPane>
  ) : settings.isPending ? (
    <MainPane>
      <Loading />
    </MainPane>
  ) : settings.isError ? (
    <MainPane>
      <div className="mx-auto max-w-[720px] px-4 py-10 sm:px-8">
        <Failure
          title="Could not load settings"
          error={settings.error}
          retry={() => void settings.refetch()}
        />
      </div>
    </MainPane>
  ) : (
    <BotPane settings={settings.data} />
  );
}

function BotPane({ settings }: { settings: Settings }) {
  const botId = useBotId();
  const path = useRouterState({ select: (s) => s.location.pathname });
  const [, , segment, sessionSegment] = path.split("/").filter(Boolean);
  const onMain = segment === undefined;
  const openSession = segment === "c" ? Number(sessionSegment) : 0;

  // The session page reads the same query, so this adds no request.
  const session = useQuery({
    ...getSessionOptions({ path: { botId, sessionId: openSession } }),
    enabled: openSession > 0,
  });

  const main = useQuery({ ...getMainSessionOptions({ path: { botId } }), enabled: openSession === 0 });
  const shown = onMain ? main.data : openSession > 0 ? session.data : undefined;

  return (
    <InPaneContext.Provider value>
      <section className="flex min-w-0 flex-1 flex-col overflow-hidden bg-background">
        <header className="grid h-12 shrink-0 grid-cols-[minmax(0,1fr)_minmax(0,2fr)_minmax(0,1fr)] items-center gap-1 border-b border-border pr-2 pl-3 sm:pl-4">
          <div className="flex min-w-0 items-center gap-1">
            <MenuButton />
          </div>
          <div className="flex min-w-0 items-center justify-center gap-2">
            <span title={settings.bot.name} className="shrink-0">
              <BotAvatar bot={settings.bot} className="size-5 rounded-control text-[11px]" />
            </span>
            {shown ? (
              <SessionTitle session={shown} heading mainTitle={settings.bot.name} />
            ) : (
              <span className="min-w-0 truncate text-[13px] font-medium">
                {pageNames.get(segment ?? "") ?? ""}
              </span>
            )}
          </div>
          <div className="flex min-w-0 items-center justify-end gap-1">
            {shown ? <Artifacts key={`${botId}:${shown.id}`} sessionId={shown.id} /> : null}
            <BotNavigation
              bot={settings.bot}
              segment={segment ?? ""}
              cwd={shown?.cwd ?? main.data?.cwd ?? ""}
            />
          </div>
        </header>
        <div inert={!onMain} className={cn("min-h-0 flex-1 flex-col", onMain ? "flex" : "hidden")}>
          <MainTimeline settings={settings} />
        </div>
        {!onMain ? <Outlet /> : null}
      </section>
    </InPaneContext.Provider>
  );
}
