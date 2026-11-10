import { useEffect, useRef } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { InboxItem } from "../api/generated/types.gen";
import {
  createChat,
  getAutomation,
  setChatProject,
  sendMessage,
  readInboxItem,
  dismissInboxItem,
} from "../api/generated/sdk.gen";
import { queryClient, refreshChats } from "../lib/queries";
import { inboxOptions, resultQuote } from "../lib/inbox";
import { Empty, FramedPage, Section } from "../components/page";
import { Failure, Loading } from "../components/feedback";
import { Button } from "../components/ui/button";
import { Badge } from "../components/ui/badge";

async function refreshInbox() {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: [{ _id: "listInbox" }] }),
    queryClient.invalidateQueries({ queryKey: [{ _id: "listAgents" }] }),
  ]);
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
      await navigate({ to: "/needs-you" });
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
      headerTitle="Needs you"
      title={item?.automationName ?? "Result"}
      description={item ? `${item.agentName} · ${new Date(item.createdAt).toLocaleString()}` : undefined}
    >
      <Section
        n={1}
        title="Result"
        action={
          <Link to="/needs-you" className="text-sm underline">
            Needs you
          </Link>
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
                className="underline"
              >
                Open routine
              </Link>
              {item.chatId ? (
                <Link
                  to="/a/$agentId/c/$chatId"
                  params={{ agentId: item.agentId, chatId: String(item.chatId) }}
                  className="underline"
                >
                  Run transcript
                </Link>
              ) : null}
            </div>
            <div className="mt-6 flex flex-wrap gap-2">
              {item.chatId ? (
                <Button onClick={() => next.mutate(item)} disabled={next.isPending || done.isPending}>
                  Continue in chat
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
            {next.isError ? <Failure title="Could not continue in chat" error={next.error} /> : null}
          </>
        )}
      </Section>
    </FramedPage>
  );
}
