import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { Automation, AutomationInput, Schedule, ScheduleInput } from "../api/generated/types.gen";
import {
  createAutomationMutation,
  updateAutomationMutation,
} from "../api/generated/@tanstack/react-query.gen";
import { previewSchedule } from "../api/generated/sdk.gen";
import { refreshAutomations } from "../lib/queries";
import { runTime, weekdays } from "../lib/automations";
import { cn } from "../lib/utils";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./ui/dialog";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Failure } from "./feedback";
import { Field } from "./page";
import { Switch } from "./ui/switch";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";

function localDateTime(at: number) {
  const date = new Date(at);

  return new Date(at - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 16);
}

export function AutomationDialog({ existing, onClose }: { existing?: Automation; onClose: () => void }) {
  const browserZone = new Intl.DateTimeFormat().resolvedOptions().timeZone;
  const [name, setName] = useState(existing?.name ?? "");
  const [prompt, setPrompt] = useState(existing?.prompt ?? "");
  const [kind, setKind] = useState<Schedule["kind"]>(existing?.schedule.kind ?? "daily");
  const [minutes, setMinutes] = useState(existing?.schedule.minutes ?? 30);
  const [time, setTime] = useState(existing?.schedule.time ?? "09:00");
  const [days, setDays] = useState(existing?.schedule.days ?? [1, 2, 3, 4, 5]);
  const [at, setAt] = useState(existing?.schedule.at ? localDateTime(existing.schedule.at) : "");
  const [timeZone, setTimeZone] = useState(existing?.timeZone ?? browserZone);
  const [session, setSession] = useState<AutomationInput["session"]>(existing?.session ?? "new");
  const [enabled, setEnabled] = useState(existing?.enabled ?? true);

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
    await refreshAutomations();
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
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !pending) onClose();
      }}
    >
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-[560px]">
        <DialogHeader>
          <DialogTitle>{existing ? "Edit automation" : "New automation"}</DialogTitle>
          <DialogDescription>Set a prompt and a schedule for the agent.</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-5 text-sm"
          onSubmit={(event) => {
            event.preventDefault();

            if (!validSchedule || pending) return;
            const body = { name: name.trim(), prompt: prompt.trim(), schedule, timeZone, session, enabled };

            if (existing) update.mutate({ path: { automationId: existing.id }, body });
            else create.mutate({ body });
          }}
        >
          <Field label="Name">
            {({ id }) => <Input id={id} required value={name} onChange={(e) => setName(e.target.value)} />}
          </Field>
          <Field label="Prompt">
            {({ id }) => (
              <Textarea
                id={id}
                required
                rows={4}
                value={prompt}
                onChange={(e) => setPrompt(e.target.value)}
                placeholder="Tell the agent what to do."
              />
            )}
          </Field>
          <fieldset className="flex flex-col gap-3">
            <legend className="mb-2">Schedule</legend>
            <div className="flex gap-1 rounded-lg bg-inset p-1" role="group" aria-label="Schedule type">
              {(["every", "daily", "weekly", "once"] as const).map((value) => (
                <Button
                  key={value}
                  type="button"
                  variant="ghost"
                  aria-pressed={kind === value}
                  onClick={() => setKind(value)}
                  className={cn("flex-1 capitalize", kind === value && "bg-surface shadow-xs")}
                >
                  {value}
                </Button>
              ))}
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
                    value={time}
                    onChange={(e) => setTime(e.target.value)}
                  />
                )}
              </Field>
            )}
            {kind === "weekly" ? (
              <div className="flex flex-wrap gap-1" role="group" aria-label="Days of the week">
                {weekdays.map((day, i) => (
                  <Button
                    key={day}
                    type="button"
                    size="sm"
                    variant={days.includes(i + 1) ? "default" : "outline"}
                    aria-pressed={days.includes(i + 1)}
                    onClick={() =>
                      setDays((current) =>
                        current.includes(i + 1)
                          ? current.filter((value) => value !== i + 1)
                          : [...current, i + 1],
                      )
                    }
                  >
                    {day}
                  </Button>
                ))}
              </div>
            ) : null}
          </fieldset>
          <Field label="Time zone">
            {({ id }) => (
              <NativeSelect
                id={id}
                className="w-full"
                value={timeZone}
                onChange={(e) => setTimeZone(e.target.value)}
              >
                {zones.map((zone) => (
                  <NativeSelectOption key={zone} value={zone}>
                    {zone}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
            )}
          </Field>
          <fieldset className="flex flex-col gap-2">
            <legend className="mb-2">Run in</legend>
            {(["new", "reuse"] as const).map((value) => (
              <label
                key={value}
                className={cn(
                  "flex cursor-pointer items-start gap-3 rounded-xl border border-line p-3 has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-ink",
                  session === value && "border-ink",
                )}
              >
                <input
                  type="radio"
                  name="session"
                  value={value}
                  checked={session === value}
                  onChange={() => setSession(value)}
                  className="mt-1 accent-ink"
                />
                <span>
                  <span className="font-medium">
                    {value === "new" ? "New chat each run" : "One chat for this automation"}
                  </span>
                  <span className="mt-1 block text-xs leading-relaxed text-ink-2">
                    {value === "new"
                      ? "Each run starts fresh."
                      : "Each run continues the same chat, so the agent sees earlier runs."}
                  </span>
                </span>
              </label>
            ))}
            {session === "reuse" ? (
              <p className="rounded-lg bg-inset p-3 text-xs leading-relaxed text-ink-2">
                The chat grows with each run. When the chat is busy, the run is skipped.
              </p>
            ) : null}
          </fieldset>
          <label className="flex items-center justify-between">
            <span>Enabled</span>
            <Switch checked={enabled} onCheckedChange={setEnabled} />
          </label>
          <section className="rounded-lg bg-inset p-3" aria-live="polite">
            <h3 className="mb-2 font-medium">Next runs · {timeZone}</h3>
            {!validSchedule ? (
              <p className="text-ink-2">Complete the schedule to see the next runs.</p>
            ) : previewInput !== input || preview.isPending ? (
              <p className="text-ink-2">Loading preview…</p>
            ) : preview.isError ? (
              <Failure title="Could not preview schedule" error={preview.error} />
            ) : preview.data.nextRuns.length ? (
              <ol className="space-y-1 text-ink-2">
                {preview.data.nextRuns.map((next) => (
                  <li key={next}>{runTime(next, timeZone)}</li>
                ))}
              </ol>
            ) : (
              <p className="text-ink-2">No future runs.</p>
            )}
            {!enabled ? (
              <p className="mt-2 text-xs text-ink-2">
                This automation is paused. Enable it to run on this schedule.
              </p>
            ) : null}
          </section>
          {error ? <Failure title="Could not save automation" error={error} /> : null}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="outline" disabled={pending} onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !validSchedule || !name.trim() || !prompt.trim()}>
              {pending ? "Saving…" : "Save automation"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
