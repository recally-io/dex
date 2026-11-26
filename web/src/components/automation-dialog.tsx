import { useAgentId } from "../lib/agent-scope";
import { useEffect, useId, useMemo, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { Automation, AutomationInput, Schedule, ScheduleInput } from "../api/generated/types.gen";
import {
  createAutomationMutation,
  getNotificationTargetsOptions,
  listAgentRunnersOptions,
  listAutomationTargetsOptions,
  updateAutomationMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { previewSchedule } from "../api/generated/sdk.gen";
import { refreshAutomations } from "../lib/queries";
import { runTime, weekdays } from "../lib/automations";
import { Button } from "./ui/button";
import { Switch } from "./ui/switch";
import { Modal } from "./dialog";
import { Failure, Spinner } from "./feedback";
import { Field, Input, RadioSegments, Select, Textarea } from "./form";

const kinds = [
  { value: "every", label: "Interval" },
  { value: "daily", label: "Daily" },
  { value: "weekly", label: "Weekly" },
  { value: "once", label: "Once" },
] as const;

function localDateTime(at: number) {
  const date = new Date(at);

  return new Date(at - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 16);
}

export function AutomationDialog({ existing, onClose }: { existing?: Automation; onClose: () => void }) {
  const agentId = useAgentId();
  const formId = useId();
  const browserZone = new Intl.DateTimeFormat().resolvedOptions().timeZone;
  const [name, setName] = useState(existing?.name ?? "");
  const [cwd, setCwd] = useState(existing?.cwd ?? "");
  const [runnerId, setRunnerId] = useState(existing?.runnerId ?? "");
  const [prompt, setPrompt] = useState(existing?.prompt ?? "");
  const [kind, setKind] = useState<Schedule["kind"]>(existing?.schedule.kind ?? "daily");
  const [minutes, setMinutes] = useState(existing?.schedule.minutes ?? 30);
  const [time, setTime] = useState(existing?.schedule.time ?? "09:00");
  const [days, setDays] = useState(existing?.schedule.days ?? [1, 2, 3, 4, 5]);
  const [at, setAt] = useState(existing?.schedule.at ? localDateTime(existing.schedule.at) : "");
  const [timeZone, setTimeZone] = useState(existing?.timeZone ?? browserZone);
  const [session, setSession] = useState<AutomationInput["session"]>(existing?.session ?? "new");
  const [targetId, setTargetId] = useState(existing?.targetSessionId ?? 0);
  const [enabled, setEnabled] = useState(existing?.enabled ?? true);

  const [notification, setNotification] = useState(
    existing?.notification?.mode === "target"
      ? `target:${existing.notification.targetId}`
      : (existing?.notification?.mode ?? (existing ? "none" : "default")),
  );

  const targets = useQuery(getNotificationTargetsOptions({ path: { agentId } }));
  const bound = session === "session";

  const sessions = useQuery({
    ...listAutomationTargetsOptions({ path: { agentId } }),
    enabled: bound,
    staleTime: 0,
  });

  const targetsForRoutine = sessions.data?.targets ?? [];
  const validTarget = !bound || targetsForRoutine.some((target) => target.id === targetId);
  const runners = useQuery({ ...listAgentRunnersOptions({ path: { agentId } }), enabled: !bound });
  const runnerList = runners.data?.runners ?? [];
  const own = runnerList.find((env) => env.default);

  // An automation can select only Local or an SSH Machine. Keep a saved Runner in the list
  // even if it is no longer a choice, so a schedule edit does not move the automation.
  const choices = runnerList.filter((env) => !env.default && (env.kind === "local" || env.kind === "ssh"));

  const kept = runnerId !== "" && !choices.some((env) => env.id === runnerId);

  const keptName = runnerList.find((env) => env.id === runnerId)?.name ?? `${runnerId} (removed)`;

  const schedule = useMemo<Schedule>(() => {
    switch (kind) {
      case "every":
        return { kind, minutes };
      case "daily":
        return { kind, time };
      case "weekly":
        return { kind, time, days };
      case "once":
        return { kind, at: new Date(at).getTime() };
    }
  }, [kind, minutes, time, days, at]);

  const input = useMemo(() => ({ schedule, timeZone }), [schedule, timeZone]);
  const [previewInput, setPreviewInput] = useState<ScheduleInput>(input);
  useEffect(() => {
    const timer = setTimeout(() => setPreviewInput(input), 350);

    return () => clearTimeout(timer);
  }, [input]);

  const validSchedule =
    kind === "every"
      ? Number.isInteger(minutes) && minutes >= 5
      : kind === "once"
        ? Number.isFinite(schedule.at)
        : !!time && (kind !== "weekly" || days.length > 0);

  const preview = useQuery({
    queryKey: ["automation-preview", previewInput],
    queryFn: async ({ signal }) => {
      const { data } = await previewSchedule({ body: previewInput, signal, throwOnError: true });

      return data;
    },
    enabled: validSchedule && previewInput === input,
    retry: false,
    staleTime: 0,
  });

  const saved = async () => {
    await refreshAutomations(agentId);
    onClose();
  };

  const create = useMutation({ ...createAutomationMutation(), onSuccess: saved });
  const update = useMutation({ ...updateAutomationMutation(), onSuccess: saved });
  const pending = create.isPending || update.isPending;
  const error = create.error ?? update.error;

  const zones = [
    ...new Set([
      browserZone,
      timeZone,
      "UTC",
      "Asia/Shanghai",
      "Asia/Tokyo",
      "Europe/London",
      "Europe/Berlin",
      "America/New_York",
      "America/Los_Angeles",
    ]),
  ];

  return (
    <Modal
      title={existing ? "Edit automation" : "New automation"}
      description="Set a prompt and a schedule, and choose where the automation runs."
      pending={pending}
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" disabled={pending} onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="default"
            type="submit"
            form={formId}
            disabled={pending || !validSchedule || !validTarget || !name.trim() || !prompt.trim()}
          >
            {pending ? (
              <Spinner className="border-primary-foreground/30 border-t-primary-foreground" />
            ) : null}
            {pending ? "Saving…" : "Save automation"}
          </Button>
        </>
      }
    >
      <form
        id={formId}
        className="flex flex-col gap-4"
        onSubmit={(event) => {
          event.preventDefault();

          if (!validSchedule || !validTarget || pending) return;

          const body = {
            name: name.trim(),
            prompt: prompt.trim(),
            cwd: bound ? undefined : cwd.trim(),
            runnerId: bound ? undefined : runnerId,
            targetSessionId: bound ? targetId : undefined,
            schedule,
            timeZone,
            session,
            enabled,
            notification: notification.startsWith("target:")
              ? { mode: "target" as const, targetId: notification.slice(7) }
              : { mode: notification === "default" ? ("default" as const) : ("none" as const) },
          };

          if (existing) update.mutate({ path: { agentId, automationId: existing.id }, body });
          else create.mutate({ path: { agentId }, body });
        }}
      >
        <Field label="Name">
          {({ id }) => (
            <Input id={id} required maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />
          )}
        </Field>
        <Field label="Prompt" hint="Sent to the bot as a user message on each run.">
          {({ id, describedBy }) => (
            <Textarea
              id={id}
              aria-describedby={describedBy}
              required
              rows={3}
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              placeholder="Tell the bot what to do."
            />
          )}
        </Field>
        {!bound && (choices.length > 0 || runnerId) ? (
          <Field
            label="Runner"
            hint="Runs use only this Runner. A run is skipped when it is removed or unreachable. Changing it clears the project directory."
          >
            {({ id, describedBy }) => (
              <Select
                id={id}
                aria-describedby={describedBy}
                value={runnerId}
                onChange={(event) => {
                  // A project path belongs to one machine, so another environment starts without one.
                  if (event.target.value !== runnerId) setCwd("");
                  setRunnerId(event.target.value);
                }}
              >
                <option value="">Follow bot ({own?.name ?? "default"})</option>
                {choices.map((env) => (
                  <option key={env.id} value={env.id}>
                    {env.name}
                  </option>
                ))}
                {kept ? <option value={runnerId}>{keptName}</option> : null}
              </Select>
            )}
          </Field>
        ) : null}
        {!bound ? (
          <Field
            label="Project directory"
            hint="Use an absolute directory on the automation's Runner. Leave empty to use its workspace."
          >
            {({ id, describedBy }) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                value={cwd}
                onChange={(e) => setCwd(e.target.value)}
              />
            )}
          </Field>
        ) : null}
        <Field
          label="Send result to"
          hint="Notifications go only to an authorized recipient. The primary channel is configured on the Channels page."
        >
          {({ id, describedBy }) => (
            <Select
              id={id}
              aria-describedby={describedBy}
              value={notification}
              onChange={(event) => setNotification(event.target.value)}
            >
              <option value="none">No notification</option>
              <option value="default">Primary channel</option>
              {targets.data?.targets.map((target) => (
                <option key={target.id} value={`target:${target.id}`}>
                  {target.platform}: {target.peer}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <div className="flex flex-col gap-1.5">
          <span aria-hidden className="text-[12.5px] font-medium text-foreground">
            Repeat
          </span>
          <RadioSegments label="Repeat" options={kinds} value={kind} onChange={setKind} />
        </div>
        {kind === "every" ? (
          <Field label="Minutes between runs" hint="Minimum: 5 minutes.">
            {({ id, describedBy }) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                type="number"
                min={5}
                step={1}
                required
                className="max-w-40 font-mono tabular-nums"
                value={Number.isNaN(minutes) ? "" : minutes}
                onChange={(e) => setMinutes(e.target.valueAsNumber)}
              />
            )}
          </Field>
        ) : kind === "once" ? (
          <Field label="Date and time" hint={`Enter the time in your browser time zone: ${browserZone}.`}>
            {({ id, describedBy }) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                type="datetime-local"
                required
                className="max-w-60 font-mono tabular-nums"
                value={at}
                onChange={(e) => setAt(e.target.value)}
              />
            )}
          </Field>
        ) : (
          <Field label="Time">
            {({ id }) => (
              <Input
                id={id}
                type="time"
                required
                className="max-w-40 font-mono tabular-nums"
                value={time}
                onChange={(e) => setTime(e.target.value)}
              />
            )}
          </Field>
        )}
        {kind === "weekly" ? (
          <fieldset>
            <legend className="mb-1.5 text-[12.5px] font-medium text-foreground">Days of the week</legend>
            <div className="flex flex-wrap gap-1.5">
              {weekdays.map((day, i) => (
                <label
                  key={day}
                  className="flex h-7 cursor-pointer items-center rounded-full bg-card px-3 text-[12.5px] font-medium text-muted-foreground shadow-card transition-colors has-[:checked]:bg-foreground has-[:checked]:text-background has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-primary"
                >
                  <input
                    type="checkbox"
                    className="sr-only"
                    checked={days.includes(i + 1)}
                    onChange={() =>
                      setDays((current) =>
                        current.includes(i + 1)
                          ? current.filter((value) => value !== i + 1)
                          : [...current, i + 1],
                      )
                    }
                  />
                  {day}
                </label>
              ))}
            </div>
          </fieldset>
        ) : null}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Time zone">
            {({ id }) => (
              <Select id={id} value={timeZone} onChange={(e) => setTimeZone(e.target.value)}>
                {zones.map((zone) => (
                  <option key={zone} value={zone}>
                    {zone}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Field
            label="Session for each run"
            hint={
              session === "reuse"
                ? "The bot sees earlier runs. The session grows with each run. When the session is busy, the run is skipped."
                : bound
                  ? "Runs join the selected session as follow-ups, using its Runner and project."
                  : "Each run starts fresh."
            }
          >
            {({ id, describedBy }) => (
              <Select
                id={id}
                aria-describedby={describedBy}
                value={session}
                onChange={(e) =>
                  setSession(
                    e.target.value === "session" ? "session" : e.target.value === "reuse" ? "reuse" : "new",
                  )
                }
              >
                <option value="new">New session each run</option>
                <option value="reuse">One session for this automation</option>
                <option value="session">Existing session</option>
              </Select>
            )}
          </Field>
        </div>
        {bound ? (
          <Field label="Target session" hint="Only writable, unarchived sessions of this bot are listed.">
            {({ id, describedBy }) => (
              <Select
                id={id}
                aria-describedby={describedBy}
                value={validTarget ? targetId : 0}
                onChange={(e) => setTargetId(Number(e.target.value))}
              >
                <option value={0}>
                  {sessions.isPending
                    ? "Loading sessions…"
                    : targetId
                      ? "Saved target is unavailable — choose a session"
                      : "Choose a session"}
                </option>
                {targetsForRoutine.map((target) => (
                  <option key={target.id} value={target.id}>
                    {target.main ? "Main" : `${target.title || "Untitled session"} · #${target.id}`}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        ) : null}
        {bound && sessions.isError ? (
          <Failure title="Could not load sessions" error={sessions.error} />
        ) : null}
        <div className="flex items-center justify-between gap-3">
          <span className="text-[12.5px] font-medium text-foreground">Enabled</span>
          <Switch checked={enabled} onCheckedChange={setEnabled} aria-label="Enabled" />
        </div>
        <section
          aria-live="polite"
          className="rounded-md bg-muted px-3.5 py-3 text-[12.5px] text-muted-foreground shadow-card"
        >
          <h3 className="mb-1 font-medium text-foreground">Next runs · {timeZone}</h3>
          {!validSchedule ? (
            <p>Complete the schedule to see the next runs.</p>
          ) : previewInput !== input || preview.isPending ? (
            <p>Loading preview…</p>
          ) : preview.isError ? (
            <Failure title="Could not preview schedule" error={preview.error} />
          ) : preview.data.nextRuns.length ? (
            <ol className="flex flex-col gap-0.5 font-mono text-[12px] tabular-nums">
              {preview.data.nextRuns.map((next) => (
                <li key={next}>{runTime(next, timeZone)}</li>
              ))}
            </ol>
          ) : (
            <p>No future runs.</p>
          )}
          {!enabled ? (
            <p className="mt-2 text-[12px]">This automation is paused. Enable it to run on this schedule.</p>
          ) : null}
        </section>
        {error ? <Failure title="Could not save automation" error={error} /> : null}
      </form>
    </Modal>
  );
}
