import { useEffect, useRef } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { InboxItem } from "../api/generated/types.gen";
import { readInboxItem, dismissInboxItem } from "../api/generated/sdk.gen";
import { listAgentsOptions } from "../api/generated/@tanstack/react-query.gen";
import { refreshWork } from "../lib/queries";
import { attentionItems, workOptions } from "../lib/work";
import { type Continuation, continueResult, inboxOptions, resultQuote } from "../lib/inbox";
import { Empty, FramedPage, Section } from "../components/page";
import { Failure, Loading } from "../components/feedback";
import { Button, buttonVariants } from "../components/ui/button";
import { Badge } from "../components/ui/badge";
import { HeldEvents } from "../components/held-events";

const actionClass = buttonVariants({ variant: "secondary", size: "sm" });

export function InboxPage() {
  const work = useQuery(workOptions());
  const agents = useQuery(listAgentsOptions());
  const { questions, results, setup, taskReviews } = attentionItems(work.data);

  const botName = (agentId: string) =>
    agents.data?.agents.find((agent) => agent.id === agentId)?.name ?? agentId;

  return (
    <FramedPage title="Inbox" description="Review items that need your attention.">
      {work.isPending ? <Loading /> : null}
      {work.isError ? <Failure title="Could not load bot attention items" error={work.error} /> : null}
      {questions.length > 0 ? (
        <Section title="Questions">
          <ul className="divide-y divide-border">
            {questions.map(({ agentId, chat, question }) => (
              <li
                key={`${agentId}:${chat.id}:${question.id}`}
                className="flex flex-wrap items-start justify-between gap-2 py-3"
              >
                <div className="min-w-0">
                  <p className="break-words font-medium">{question.text}</p>
                  <p className="text-sm text-muted-foreground">
                    {botName(agentId)} · {chat.main ? "Main" : chat.title || "Untitled session"}
                  </p>
                </div>
                {chat.main ? (
                  <Link className={actionClass} to="/a/$agentId" params={{ agentId }}>
                    Open question
                  </Link>
                ) : (
                  <Link
                    className={actionClass}
                    to="/a/$agentId/c/$chatId"
                    params={{ agentId, chatId: String(chat.id) }}
                  >
                    Open question
                  </Link>
                )}
              </li>
            ))}
          </ul>
        </Section>
      ) : null}
      {taskReviews.length > 0 ? (
        <Section title="Tasks awaiting review">
          <ul className="divide-y divide-border">
            {taskReviews.map((task) => (
              <li key={task.key} className="py-3">
                <Link className="font-medium underline" to="/board/$taskKey" params={{ taskKey: task.key }}>
                  <span className="font-mono text-muted-foreground">{task.key}</span> {task.title}
                </Link>
                <p className="text-sm text-muted-foreground">{botName(task.assigneeBotId)}</p>
              </li>
            ))}
          </ul>
        </Section>
      ) : null}
      {results.length > 0 ? (
        <Section title="Automation results">
          <ul className="divide-y divide-border">
            {results.map((item) => (
              <li key={item.runId} className="py-3">
                <Link className="font-medium underline" to="/inbox/$runId" params={{ runId: item.runId }}>
                  {item.automationName}
                </Link>
                <p className="text-sm text-muted-foreground">
                  {item.agentName} · {item.outcome === "failed" ? "Failed" : "Changed"}
                </p>
              </li>
            ))}
          </ul>
        </Section>
      ) : null}
      {setup.length > 0 ? (
        <Section title="Automations without an available model">
          <ul className="divide-y divide-border">
            {setup.map(({ agentId, automation }) => (
              <li
                key={`${agentId}:${automation.id}`}
                className="flex flex-wrap items-start justify-between gap-2 py-3"
              >
                <div className="min-w-0">
                  <p className="break-words font-medium">{automation.name}</p>
                  <p className="text-sm text-muted-foreground">{botName(agentId)}</p>
                </div>
                <Link className={actionClass} to="/a/$agentId/agent" params={{ agentId }}>
                  Configure model
                </Link>
              </li>
            ))}
          </ul>
        </Section>
      ) : null}
      {work.isSuccess && !questions.length && !results.length && !setup.length && !taskReviews.length ? (
        <Empty title="No bot items need attention">
          New questions, Task reviews, and Automation items appear here.
        </Empty>
      ) : null}
      <HeldEvents />
    </FramedPage>
  );
}

export function InboxResultPage({ runId }: { runId: string }) {
  const inbox = useQuery({ ...inboxOptions(), refetchInterval: 10_000 });

  return (
    <InboxDetail
      key={runId}
      item={inbox.data?.items.find((item) => item.runId === runId)}
      pending={inbox.isPending}
      error={inbox.error}
    />
  );
}

function InboxDetail({ item, pending, error }: { item?: InboxItem; pending: boolean; error: unknown }) {
  const navigate = useNavigate();
  const opened = useRef(false);
  const continuation = useRef<Continuation | null>(null);

  const read = useMutation({
    mutationFn: async (runId: string) => {
      await readInboxItem({ path: { runId }, throwOnError: true });
      await refreshWork();
    },
  });

  const { mutate: markRead } = read;
  useEffect(() => {
    if (item && !item.read && !opened.current) {
      opened.current = true;
      markRead(item.runId);
    }
  }, [item, markRead]);

  const done = useMutation({
    mutationFn: async (result: InboxItem) => {
      await dismissInboxItem({ path: { runId: result.runId }, throwOnError: true });
      await refreshWork();
      await navigate({ to: "/inbox" });
    },
  });

  const next = useMutation({
    mutationFn: async (result: InboxItem) => {
      const agentId = result.agentId;
      const chatId = await continueResult(agentId, result, resultQuote(result), continuation);

      await navigate({ to: "/a/$agentId/c/$chatId", params: { agentId, chatId: String(chatId) } });
    },
  });

  return (
    <FramedPage
      headerTitle="Automation result"
      title={item?.automationName ?? "Result"}
      description={item ? `${item.agentName} · ${new Date(item.createdAt).toLocaleString()}` : undefined}
    >
      <Section
        title="Result"
        action={
          item ? (
            <Link
              to="/a/$agentId/automations"
              params={{ agentId: item.agentId }}
              className="text-sm underline"
            >
              Automations
            </Link>
          ) : null
        }
      >
        {pending ? (
          <Loading />
        ) : error ? (
          <Failure title="Could not load result" error={error} />
        ) : !item ? (
          <Empty title="Result not found">This run is no longer listed.</Empty>
        ) : (
          <>
            <Badge variant={item.outcome === "failed" ? "destructive" : "secondary"}>{item.outcome}</Badge>
            {item.dismissed ? (
              <Badge variant="outline" className="ml-2">
                Done
              </Badge>
            ) : null}
            <p className="my-5 whitespace-pre-wrap [overflow-wrap:anywhere]">{item.summary}</p>
            <div className="flex flex-wrap items-center gap-4 text-sm">
              <Link
                to="/a/$agentId/automations/$automationId"
                params={{ agentId: item.agentId, automationId: item.automationId }}
                className={buttonVariants({ variant: "secondary" })}
              >
                Open automation
              </Link>
              {item.chatId ? (
                <Link
                  to="/a/$agentId/c/$chatId"
                  params={{ agentId: item.agentId, chatId: String(item.chatId) }}
                  className={buttonVariants({ variant: "secondary" })}
                >
                  Run transcript
                </Link>
              ) : null}
            </div>
            <div className="mt-6 flex flex-wrap gap-2">
              {item.chatId ? (
                <Button onClick={() => next.mutate(item)} disabled={next.isPending || done.isPending}>
                  Continue in a session
                </Button>
              ) : null}
              <Button
                variant="secondary"
                onClick={() => done.mutate(item)}
                disabled={item.dismissed || done.isPending || next.isPending}
              >
                Done
              </Button>
            </div>
            {read.isError ? (
              <Failure
                title="Could not mark result read"
                error={read.error}
                retry={() => read.mutate(item.runId)}
              />
            ) : null}
            {done.isError ? <Failure title="Could not dismiss result" error={done.error} /> : null}
            {next.isError ? <Failure title="Could not continue in a session" error={next.error} /> : null}
          </>
        )}
      </Section>
    </FramedPage>
  );
}
