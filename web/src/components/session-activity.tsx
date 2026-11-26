import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Clock, Send, X } from "lucide-react";
import type { Monitor, OutboxMessage } from "../api/generated/types.gen";
import {
  cancelSessionMonitorMutation,
  listSessionOutboxOptions,
  listSessionMonitorsOptions,
} from "../api/generated/@tanstack/react-query.gen";
import { useAgentId } from "../lib/agent-scope";
import { errorText } from "../lib/queries";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "./ui/popover";

function formatShortTime(ms: number): string {
  if (!ms) return "";

  return new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function formatDue(ms: number): string {
  const diffSec = Math.round((ms - Date.now()) / 1000);

  if (diffSec <= 0) return "due now";

  if (diffSec < 60) return `in ${diffSec}s`;

  const diffMin = Math.round(diffSec / 60);

  if (diffMin < 60) return `in ${diffMin}m`;

  const diffHr = Math.round(diffMin / 60);

  if (diffHr < 24) return `in ${diffHr}h`;

  return new Date(ms).toLocaleDateString([], {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function monitorBadgeVariant(state: Monitor["state"]): "default" | "secondary" | "outline" | "destructive" {
  switch (state) {
    case "scheduled":
      return "default";
    case "fired":
      return "secondary";
    case "expired":
      return "destructive";
    default:
      return "outline";
  }
}

function outboxBadgeVariant(
  status: OutboxMessage["status"],
): "default" | "secondary" | "outline" | "destructive" {
  switch (status) {
    case "pending":
      return "default";
    case "sent":
      return "secondary";
    case "failed":
      return "destructive";
    default:
      return "outline";
  }
}

export function SessionActivity({ sessionId, writable }: { sessionId: number; writable: boolean }) {
  const agentId = useAgentId();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);

  const monitorsQuery = useQuery({
    ...listSessionMonitorsOptions({ path: { agentId, sessionId } }),
    refetchInterval: open ? 5000 : 30000,
  });

  const outboxQuery = useQuery({
    ...listSessionOutboxOptions({ path: { agentId, sessionId } }),
    refetchInterval: open ? 5000 : 30000,
  });

  const cancel = useMutation({
    ...cancelSessionMonitorMutation(),
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: listSessionMonitorsOptions({ path: { agentId, sessionId } }).queryKey,
      });
    },
  });

  const monitors = monitorsQuery.data?.monitors ?? [];
  const messages = outboxQuery.data?.messages ?? [];
  const scheduledCount = monitors.filter((t) => t.state === "scheduled").length;
  const issueCount = messages.filter((m) => m.status === "failed" || Boolean(m.error)).length;
  const pendingOutbox = messages.filter((m) => m.status === "pending").length;

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        setOpen(next);

        if (next) {
          void monitorsQuery.refetch();
          void outboxQuery.refetch();
        }
      }}
    >
      <PopoverTrigger
        render={
          <Button
            variant="ghost"
            size="sm"
            className="gap-1.5 px-1.5 text-muted-foreground hover:text-foreground"
            aria-label="Session activity"
            title="Scheduled Monitors and Outbox delivery"
          >
            <Clock className="size-3.5 shrink-0" />
            <span>Activity</span>
            {scheduledCount > 0 ? (
              <span className="text-[12px] font-medium text-foreground tabular-nums">{scheduledCount}</span>
            ) : null}
            {issueCount > 0 ? (
              <span
                aria-label={`${issueCount} delivery issue`}
                className="size-1.5 rounded-full bg-destructive"
              />
            ) : pendingOutbox > 0 ? (
              <span
                aria-label={`${pendingOutbox} pending outbox`}
                className="size-1.5 rounded-full bg-primary"
              />
            ) : null}
          </Button>
        }
      />
      <PopoverContent side="top" align="end" className="w-[360px] max-w-[calc(100vw-2rem)] p-0">
        <div className="max-h-[420px] divide-y divide-border overflow-y-auto">
          <div className="p-3">
            <div className="mb-2 flex items-center justify-between gap-2">
              <span className="flex items-center gap-1.5 text-[12px] font-medium text-foreground">
                <Clock className="size-3.5 text-muted-foreground" />
                Monitors
              </span>
              <span className="text-[11px] text-muted-foreground tabular-nums">{scheduledCount} active</span>
            </div>
            <p className="mb-2 text-[12px] text-muted-foreground">
              Scheduled prompts and checks. Use session tools to manage background Processes.
            </p>
            {cancel.isError ? (
              <p className="mb-2 text-[12px] text-destructive">{errorText(cancel.error)}</p>
            ) : null}
            {monitors.length === 0 ? (
              <p className="text-[12px] text-muted-foreground">No Monitors in this session.</p>
            ) : (
              <ul aria-label="Monitors" className="space-y-2">
                {monitors.slice(0, 10).map((t) => (
                  <li
                    key={t.id}
                    className="flex items-start justify-between gap-2 rounded-md border border-border/70 bg-muted/30 px-2.5 py-2 text-[12px]"
                  >
                    <div className="min-w-0 flex-1 space-y-1">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <Badge variant={monitorBadgeVariant(t.state)} className="h-4 px-1.5 text-[10px]">
                          {t.state}
                        </Badge>
                        <span className="text-[11px] text-muted-foreground tabular-nums">
                          {t.state === "scheduled" ? formatDue(t.dueAt) : formatShortTime(t.dueAt)}
                        </span>
                        <span className="text-[11px] text-muted-foreground tabular-nums">
                          {t.mode} · {t.runs}/{t.maxRuns} checks
                        </span>
                      </div>
                      <p className="line-clamp-2 text-[12px] text-foreground [overflow-wrap:anywhere]">
                        {t.prompt}
                      </p>
                      {t.outcome && <p className="text-[11px] text-muted-foreground">{t.outcome}</p>}
                      {t.state === "scheduled" && t.expiresAt > 0 && (
                        <p className="text-[11px] text-muted-foreground">Expires {formatDue(t.expiresAt)}</p>
                      )}
                    </div>
                    {writable && t.state === "scheduled" ? (
                      <Button
                        variant="ghost"
                        size="icon-xs"
                        aria-label={`Cancel monitor ${t.id}`}
                        title="Cancel monitor"
                        disabled={cancel.isPending}
                        onClick={() => cancel.mutate({ path: { agentId, sessionId, monitorId: t.id } })}
                      >
                        <X className="size-3.5" />
                      </Button>
                    ) : null}
                  </li>
                ))}
              </ul>
            )}
          </div>

          <div className="p-3">
            <div className="mb-2 flex items-center justify-between gap-2">
              <span className="flex items-center gap-1.5 text-[12px] font-medium text-foreground">
                <Send className="size-3.5 text-muted-foreground" />
                Outbox
              </span>
              <span className="text-[11px] text-muted-foreground tabular-nums">{messages.length} recent</span>
            </div>
            {outboxQuery.isError ? (
              <p className="text-[12px] text-destructive">{errorText(outboxQuery.error)}</p>
            ) : outboxQuery.isPending ? (
              <p className="text-[12px] text-muted-foreground">Loading Outbox…</p>
            ) : messages.length === 0 ? (
              <p className="text-[12px] text-muted-foreground">No outbound messages yet.</p>
            ) : (
              <ul aria-label="Recent Outbox entries" className="space-y-2">
                {messages.map((m) => (
                  <li
                    key={m.id}
                    className="space-y-1 rounded-md border border-border/70 bg-muted/30 px-2.5 py-2 text-[12px]"
                  >
                    <div className="flex flex-wrap items-center gap-1.5">
                      <Badge variant={outboxBadgeVariant(m.status)} className="h-4 px-1.5 text-[10px]">
                        {m.peer ? `Delivery: ${m.status}` : "Web record"}
                      </Badge>
                      <span className="font-mono text-[11px] text-muted-foreground">{m.kind}</span>
                      {m.peer ? (
                        <span className="truncate font-mono text-[11px] text-muted-foreground">{m.peer}</span>
                      ) : null}
                      {m.topicKey ? (
                        <span className="truncate font-mono text-[10px] text-muted-foreground">
                          #{m.topicKey}
                        </span>
                      ) : null}
                      <span className="ml-auto text-[11px] text-muted-foreground tabular-nums">
                        {formatShortTime(m.deliveredAt || m.createdAt)}
                      </span>
                    </div>
                    {m.body || m.reason ? (
                      <p className="text-[12px] whitespace-pre-wrap text-foreground [overflow-wrap:anywhere]">
                        {m.body || m.reason}
                      </p>
                    ) : null}
                    {m.error ? (
                      <p className="text-[11px] text-destructive [overflow-wrap:anywhere]">{m.error}</p>
                    ) : null}
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}
