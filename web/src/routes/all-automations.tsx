import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { getWorkOptions } from "../api/generated/@tanstack/react-query.gen";
import { automationNeedsYou } from "../lib/work";
import { runTime, scheduleText } from "../lib/automations";
import { Empty, FramedPage } from "../components/page";
import { Failure, Loading } from "../components/feedback";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";

type Status = "All" | "Needs you" | "Active" | "Paused";

export function AllAutomationsPage() {
  const [now] = useState(() => Date.now());
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState<Status>("All");
  const [bot, setBot] = useState("");

  const work = useQuery({
    ...getWorkOptions({ query: { from: now, to: now + 7 * 86400000 } }),
    refetchInterval: 10000,
  });

  const agents = work.data?.agents ?? [];
  const inbox = work.data?.inbox ?? [];

  const items = (work.data?.automations ?? []).map((item) => ({
    ...item,
    needs: automationNeedsYou(item.automation.id, item.setup, inbox),
  }));

  const rows = items
    .filter(
      (item) =>
        (!bot || item.agentId === bot) &&
        item.automation.name.toLocaleLowerCase().includes(search.toLocaleLowerCase()) &&
        (status === "All" ||
          (status === "Needs you"
            ? item.needs
            : status === "Active"
              ? item.automation.enabled
              : !item.automation.enabled)),
    )
    .sort((a, b) => Number(b.needs) - Number(a.needs) || a.automation.name.localeCompare(b.automation.name));

  return (
    <FramedPage
      headerTitle="Automations"
      title="Automations"
      description={`${items.length} automations · ${items.filter((item) => item.needs).length} need you`}
    >
      <div className="mb-4 flex flex-wrap gap-2">
        <Input
          aria-label="Search automations"
          placeholder="Search automations"
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          className="w-56"
        />
        {(["All", "Needs you", "Active", "Paused"] as const).map((value) => (
          <Button
            key={value}
            size="sm"
            variant={status === value ? "secondary" : "outline"}
            aria-pressed={status === value}
            onClick={() => setStatus(value)}
          >
            {value}
          </Button>
        ))}
        <select
          aria-label="Filter by bot"
          value={bot}
          onChange={(event) => setBot(event.target.value)}
          className="rounded-md border border-input bg-background px-2 text-sm"
        >
          <option value="">All bots</option>
          {agents.map((agent) => (
            <option key={agent.id} value={agent.id}>
              {agent.name}
            </option>
          ))}
        </select>
      </div>
      {work.isPending ? (
        <Loading />
      ) : work.isError ? (
        <Failure title="Could not load automations" error={work.error} retry={() => void work.refetch()} />
      ) : !rows.length ? (
        <Empty title="No automations">No automations match these filters.</Empty>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead>
              <tr className="border-b border-border text-xs text-muted-foreground">
                <th className="p-2">Name</th>
                <th className="p-2">Bot</th>
                <th className="p-2">Schedule</th>
                <th className="p-2">Next run</th>
                <th className="p-2">Last result</th>
                <th className="p-2">Each run</th>
              </tr>
            </thead>
            <tbody>
              {rows.map(({ agentId, automation, needs, setup }) => (
                <tr
                  key={automation.id}
                  className={`border-b border-border ${!automation.enabled ? "opacity-50" : ""}`}
                >
                  <td className="p-2 font-semibold">
                    <Link
                      className="underline"
                      to="/a/$agentId/automations/$automationId"
                      params={{ agentId, automationId: automation.id }}
                    >
                      {automation.name}
                    </Link>
                    {needs ? (
                      <span className="ml-2 text-xs text-orange">{setup ? "Setup" : "Needs you"}</span>
                    ) : null}
                  </td>
                  <td className="p-2">{agents.find((agent) => agent.id === agentId)?.name}</td>
                  <td className="p-2">{scheduleText(automation.schedule, automation.timeZone)}</td>
                  <td className="p-2">
                    {automation.enabled
                      ? automation.nextRuns[0]
                        ? runTime(automation.nextRuns[0], automation.timeZone)
                        : "—"
                      : "Paused"}
                  </td>
                  <td className="p-2">
                    {automation.lastRun?.outcome || automation.lastRun?.status || "No runs yet"}
                  </td>
                  <td className="p-2">
                    {automation.session === "new" ? "Start fresh" : "Continue from last run"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </FramedPage>
  );
}
