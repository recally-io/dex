import { useBotSettings, useBotId } from "../lib/bot-scope";
import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useQuery, useMutation } from "@tanstack/react-query";
import { ChevronRight, MessageCircle, Clock3 } from "lucide-react";
import type { Session } from "../api/generated/types.gen";
import {
  getMainSessionOptions,
  listSessionsOptions,
  archiveSessionMutation,
  unarchiveSessionMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { useNow } from "../lib/clock";
import {
  activeSessionCount,
  isStalled,
  parentName,
  sessionBotId,
  sessionKind,
  sessionTime,
} from "../lib/sessions";
import { refreshSessions } from "../lib/queries";
import { Empty, FramedPage, Panel, Section } from "../components/page";
import { Failure, Loading } from "../components/feedback";
import { Button } from "../components/ui/button";
import { Badge } from "../components/ui/badge";
import { Select } from "../components/form";

export function SessionsPage() {
  const botId = useBotId();
  const settings = useBotSettings(botId);

  const sessions = useQuery({
    ...listSessionsOptions({ path: { botId } }),
    placeholderData: (previous) => previous,
    refetchInterval: 3000,
  });

  const main = useQuery(getMainSessionOptions({ path: { botId } }));
  const [kind, setKind] = useState("All kinds");
  const [archivedOpen, setArchivedOpen] = useState(false);
  const name = settings.data?.bot.name ?? "dex";
  const everySession = sessions.data?.sessions ?? [];
  const all = everySession.filter((session) => !session.archivedAt);
  const list = all.filter((session) => kind === "All kinds" || sessionKind(session) === kind);

  return (
    <FramedPage
      title="Sessions"
      description={`Sessions of ${name} apart from the main timeline. Each session has its own context. Sessions you open and child sessions that ${name} starts take messages. Automation runs are read-only.`}
    >
      <Section
        title="All sessions"
        caption={all.length ? `${activeSessionCount(list)} active · newest first` : undefined}
        action={
          all.length ? (
            <Select
              aria-label="Kind"
              className="h-8 w-auto text-[12.5px]"
              value={kind}
              onChange={(event) => setKind(event.target.value)}
            >
              {["All kinds", "Session", "Automation"].map((value) => (
                <option key={value}>{value}</option>
              ))}
            </Select>
          ) : null
        }
      >
        {sessions.isPending ? (
          <Loading />
        ) : sessions.isError ? (
          <Failure
            title="Could not load sessions"
            error={sessions.error}
            retry={() => void sessions.refetch()}
          />
        ) : list.length ? (
          <Panel>
            <ul>
              {list.map((session) => (
                <SessionRow
                  key={session.id}
                  session={session}
                  parent={parentName(session, main.data?.id, everySession)}
                />
              ))}
            </ul>
          </Panel>
        ) : all.length ? (
          <Empty title="No sessions of this kind">Choose All kinds to see every session.</Empty>
        ) : (
          <Empty title="No sessions yet">
            Your sessions, child sessions that {name} starts, and automation runs show here. Use New session
            for work that needs a clean context.
          </Empty>
        )}
      </Section>
      {everySession.some((session) => session.archivedAt) ? (
        <Section
          title="Archived"
          action={
            <Button
              variant="ghost"
              size="sm"
              aria-expanded={archivedOpen}
              onClick={() => setArchivedOpen(!archivedOpen)}
            >
              {archivedOpen ? "Hide archived" : "Show archived"}
            </Button>
          }
        >
          {archivedOpen ? (
            <Panel>
              <ul>
                {everySession
                  .filter((session) => session.archivedAt)
                  .map((session) => (
                    <SessionRow
                      key={session.id}
                      session={session}
                      parent={parentName(session, main.data?.id, everySession)}
                    />
                  ))}
              </ul>
            </Panel>
          ) : null}
        </Section>
      ) : null}
    </FramedPage>
  );
}

/** A busy row says Running, or Stalled when the session shows no sign of work. */
function SessionRow({ session, parent }: { session: Session; parent?: string }) {
  const botId = useBotId();
  const kind = sessionKind(session);
  const Icon = kind === "Session" ? MessageCircle : Clock3;
  const now = useNow();
  const stalled = isStalled(session, now);

  const changeArchive = useMutation({
    ...(session.archivedAt ? unarchiveSessionMutation() : archiveSessionMutation()),
    onSuccess: () => refreshSessions(botId),
  });

  return (
    <li className="flex items-center gap-3 border-t border-border px-4 py-3.5 first:border-t-0">
      <Icon size={16} className="mt-0.5 shrink-0 self-start text-muted-foreground" />
      <Link
        to="/b/$botId/s/$sessionId"
        params={{ botId: sessionBotId(session, botId), sessionId: String(session.id) }}
        className="min-w-0 flex-1 hover:underline"
      >
        <span className="block text-[13.5px] font-medium text-foreground [overflow-wrap:anywhere]">
          {session.title || "New session"}
        </span>
        <span className="mt-0.5 block text-[12px] text-muted-foreground">
          {parent === undefined ? kind : `Started by ${parent}`} · {sessionTime(session.updatedAt)}
        </span>
      </Link>
      {session.busy ? (
        <Badge
          variant="secondary"
          className={`h-5 shrink-0 px-2 text-[11.5px] ${stalled ? "bg-orange-tint text-orange" : "bg-primary/10 text-primary"}`}
        >
          {stalled ? "Stalled" : "Running"}
        </Badge>
      ) : null}
      {kind === "Session" ? (
        <Button
          variant="ghost"
          size="sm"
          disabled={changeArchive.isPending}
          onClick={() =>
            changeArchive.mutate({
              path: { botId: sessionBotId(session, botId), sessionId: session.id },
            })
          }
        >
          {session.archivedAt ? "Restore" : "Archive"}
        </Button>
      ) : null}
      {changeArchive.isError ? (
        <span role="alert" className="text-xs text-red">
          Could not change session.
        </span>
      ) : null}
      <ChevronRight size={16} className="shrink-0 text-muted-foreground" />
    </li>
  );
}
