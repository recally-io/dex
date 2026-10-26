import { useEffect, useRef, useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { InboxItem } from "../api/generated/types.gen";
import { listAgentsOptions } from "../api/generated/@tanstack/react-query.gen";
import {
  createChat,
  getAutomation,
  setChatProject,
  sendMessage,
  readInboxItem,
  dismissInboxItem,
} from "../api/generated/sdk.gen";
import { queryClient, refreshChats } from "../lib/queries";
import { inboxGroups, inboxOptions, resultQuote, type InboxFilter } from "../lib/inbox";
import { Empty, FramedPage, Panel, Section } from "../components/page";
import { Failure, Loading } from "../components/feedback";
import { Button } from "../components/ui/button";
import { Badge } from "../components/ui/badge";
import { ToggleGroup, ToggleGroupItem } from "../components/ui/toggle-group";

async function refreshInbox() {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: [{ _id: "listInbox" }] }),
    queryClient.invalidateQueries({ queryKey: [{ _id: "listAgents" }] }),
  ]);
}

export function InboxPage({ runId }: { runId?: string }) {
  const inbox = useQuery({ ...inboxOptions(), refetchInterval: 10_000 });
  const agents = useQuery(listAgentsOptions());
  const [agentId, setAgentId] = useState("");
  const [filter, setFilter] = useState<InboxFilter>("All");
  const items = inbox.data?.items ?? [];
  const groups = inboxGroups(items, agentId, filter);

  if (runId)
    return (
      <InboxDetail
        key={runId}
        item={items.find((item) => item.runId === runId)}
        pending={inbox.isPending}
        error={inbox.error}
      />
    );

  return (
    <FramedPage headerTitle="Inbox" title="Inbox" description="What needs you now, across all bots.">
      <Section
        n={1}
        title="Routine results"
        action={
          <ToggleGroup
            value={[filter]}
            size="sm"
            variant="outline"
            onValueChange={(values) => {
              const value = values[0];

              if (value === "All" || value === "Unread" || value === "Needs you") setFilter(value);
            }}
          >
            {(["All", "Unread", "Needs you"] as const).map((value) => (
              <ToggleGroupItem key={value} value={value}>
                {value}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
        }
      >
        <div className="mb-4 flex flex-wrap gap-2" aria-label="Filter by bot">
          <Button
            size="sm"
            variant={!agentId ? "secondary" : "ghost"}
            aria-pressed={!agentId}
            onClick={() => setAgentId("")}
          >
            All bots
          </Button>
          {agents.data?.agents.map((agent) => (
            <Button
              key={agent.id}
              size="sm"
              variant={agentId === agent.id ? "secondary" : "ghost"}
              aria-pressed={agentId === agent.id}
              onClick={() => setAgentId(agent.id ?? "default")}
            >
              {agent.name}
            </Button>
          ))}
        </div>
        {inbox.isPending ? (
          <Loading />
        ) : inbox.isError ? (
          <Failure title="Could not load Inbox" error={inbox.error} retry={() => void inbox.refetch()} />
        ) : !groups.needsYou.length && !groups.results.length ? (
          <Empty title="You're all caught up">No routine results need your attention.</Empty>
        ) : (
          <div className="space-y-6">
            <InboxGroup title="Needs you" items={groups.needsYou} />
            <InboxGroup title="Results" items={groups.results} />
          </div>
        )}
      </Section>
    </FramedPage>
  );
}

function InboxGroup({ title, items }: { title: string; items: InboxItem[] }) {
  if (!items.length) return null;

  return (
    <section aria-label={title}>
      <h2 className="mb-2 text-sm font-semibold">
        {title} <span className="text-muted-foreground">{items.length}</span>
      </h2>
      <Panel>
        <ul className="divide-y divide-border">
          {items.map((item) => (
            <li key={item.runId}>
              <Link
                to="/inbox/$runId"
                params={{ runId: item.runId }}
                className="flex gap-3 px-4 py-3 hover:bg-accent"
              >
                <span
                  className={`mt-1.5 size-2 shrink-0 rounded-full ${item.outcome === "failed" ? "bg-red" : "bg-green"}`}
                />
                <span className="min-w-0 flex-1">
                  <span className={`block text-sm ${!item.read ? "font-semibold" : "font-normal"}`}>
                    {item.automationName}
                    {!item.read ? <span className="ml-2 text-xs text-muted-foreground">Unread</span> : null}
                  </span>
                  <span className="mt-1 line-clamp-2 text-sm text-muted-foreground [overflow-wrap:anywhere]">
                    {item.summary}
                  </span>
                  <span className="mt-2 block text-xs text-muted-foreground">
                    {item.agentName} · {new Date(item.createdAt).toLocaleString()}
                  </span>
                </span>
              </Link>
            </li>
          ))}
        </ul>
      </Panel>
    </section>
  );
}

function InboxDetail({ item, pending, error }: { item?: InboxItem; pending: boolean; error: unknown }) {
  const navigate = useNavigate();
  const opened = useRef(false);
  const continuation = useRef<{ chatId: number; messageId: string } | null>(null);

  const read = useMutation({
    mutationFn: async (runId: string) => {
      await readInboxItem({ path: { runId }, throwOnError: true });
      await refreshInbox();
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
      await refreshInbox();
      await navigate({ to: "/inbox" });
    },
  });

  const next = useMutation({
    mutationFn: async (result: InboxItem) => {
      const agentId = result.agentId;

      const routine = await getAutomation({
        path: { agentId, automationId: result.automationId },
        throwOnError: true,
      });

      if (!continuation.current) {
        const chat = await createChat({ path: { agentId }, throwOnError: true });
        continuation.current = { chatId: chat.data.id, messageId: crypto.randomUUID() };
      }

      const { chatId, messageId } = continuation.current;
      const path = { agentId, chatId };
      await setChatProject({ path, body: { cwd: routine.data.cwd ?? "" }, throwOnError: true });
      await sendMessage({ path, body: { id: messageId, text: resultQuote(result) }, throwOnError: true });
      await refreshChats(agentId);
      await navigate({ to: "/a/$agentId/c/$chatId", params: { agentId, chatId: String(chatId) } });
    },
  });

  return (
    <FramedPage
      headerTitle="Inbox"
      title={item?.automationName ?? "Inbox result"}
      description={item ? `${item.agentName} · ${new Date(item.createdAt).toLocaleString()}` : undefined}
    >
      <Section
        n={1}
        title="Result"
        action={
          <Link to="/inbox" className="text-sm underline">
            Inbox
          </Link>
        }
      >
        {pending ? (
          <Loading />
        ) : error ? (
          <Failure title="Could not load result" error={error} />
        ) : !item ? (
          <Empty title="Result not found">This run is no longer in the Inbox.</Empty>
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
                className="underline"
              >
                Open routine
              </Link>
              <Link
                to="/a/$agentId/c/$chatId"
                params={{ agentId: item.agentId, chatId: String(item.chatId) }}
                className="underline"
              >
                Run transcript
              </Link>
            </div>
            <div className="mt-6 flex flex-wrap gap-2">
              <Button onClick={() => next.mutate(item)} disabled={next.isPending || done.isPending}>
                Continue in chat
              </Button>
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
            {next.isError ? <Failure title="Could not continue in chat" error={next.error} /> : null}
          </>
        )}
      </Section>
    </FramedPage>
  );
}
