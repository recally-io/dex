import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  backupSystemDatabaseMutation,
  cancelSystemOperationMutation,
  checkSystemUpdateMutation,
  getHealthOptions,
  getHealthQueryKey,
  getSystemLogsOptions,
  getSystemStatusOptions,
  getSystemStatusQueryKey,
  restartSystemMutation,
  updateSystemSettingsMutation,
  upgradeSystemMutation,
} from "../api/generated/@tanstack/react-query.gen";
import type { SystemStatus, SystemUpdateChannel } from "../api/generated/types.gen";
import { Confirm } from "../components/confirm";
import { Failure, Loading, Spinner } from "../components/feedback";
import { RadioSegments } from "../components/form";
import { Facts, FramedPage, Mono, Note, Panel, Section } from "../components/page";
import { GlobalSettingsSwitcher } from "../components/settings-switcher";
import { ThemeToggle } from "../components/theme";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { useApp } from "../lib/app-state";
import { queryClient } from "../lib/queries";

const channelOptions: readonly { value: SystemUpdateChannel; label: string }[] = [
  { value: "stable", label: "Stable" },
  { value: "prerelease", label: "Prerelease (RC)" },
];

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;

  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;

  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function formatTime(ms: number): string {
  if (!ms) return "—";

  return new Date(ms).toLocaleString();
}

function capabilityReasonText(reason?: string): string {
  switch (reason) {
    case "dev":
      return "This server is running a development build. Self-upgrade is available only on release binaries.";
    case "package":
      return "This binary is managed by a package manager. Upgrade it through that package manager instead.";
    case "not-writable":
      return "The executable directory is not writable by this process. Replace the binary manually or move it to a user-writable path.";
    default:
      return "In-place self-upgrade is not available on this host.";
  }
}

function stateBadge(state: SystemStatus["state"], reconnecting: boolean, unreachable: boolean) {
  if (reconnecting || state === "restarting") {
    return (
      <Badge variant="secondary" className="h-5 bg-orange-tint px-2 text-[11.5px] text-orange">
        Restarting…
      </Badge>
    );
  }

  if (unreachable) {
    return (
      <Badge variant="secondary" className="h-5 bg-red-tint px-2 text-[11.5px] text-red">
        Unavailable
      </Badge>
    );
  }

  switch (state) {
    case "downloading":
      return (
        <Badge variant="secondary" className="h-5 bg-blue-tint px-2 text-[11.5px] text-blue">
          Downloading…
        </Badge>
      );
    case "waiting_idle":
      return (
        <Badge variant="secondary" className="h-5 bg-orange-tint px-2 text-[11.5px] text-orange">
          Waiting for active runs
        </Badge>
      );
    case "checking":
      return (
        <Badge variant="secondary" className="h-5 px-2 text-[11.5px]">
          Checking…
        </Badge>
      );
    case "error":
      return (
        <Badge variant="secondary" className="h-5 bg-red-tint px-2 text-[11.5px] text-red">
          Attention needed
        </Badge>
      );
    default:
      return (
        <Badge variant="secondary" className="h-5 bg-green-tint px-2 text-[11.5px] text-green">
          Running
        </Badge>
      );
  }
}

function isActiveOperation(state: SystemStatus["state"] | undefined) {
  return state === "downloading" || state === "waiting_idle" || state === "restarting";
}

export function AboutPage() {
  const { notify } = useApp();
  const [confirmRestart, setConfirmRestart] = useState(false);
  const [confirmUpgrade, setConfirmUpgrade] = useState(false);
  const [showNotes, setShowNotes] = useState(false);
  const [showLogs, setShowLogs] = useState(false);
  const restartBaseline = useRef<number | null>(null);
  const statusKey = getSystemStatusQueryKey();

  const status = useQuery({
    ...getSystemStatusOptions(),
    refetchOnMount: "always",
    refetchInterval: (query) => (isActiveOperation(query.state.data?.state) ? 1000 : false),
    retry: (failures) =>
      failures < 1 && !isActiveOperation(queryClient.getQueryData<SystemStatus>(statusKey)?.state),
  });

  const health = useQuery(getHealthOptions());
  const sys = status.data;
  const reconnecting = sys?.state === "restarting" || (status.isError && isActiveOperation(sys?.state));

  const logs = useQuery({
    ...getSystemLogsOptions({ query: { limit: 200 } }),
    enabled: showLogs && !reconnecting,
  });

  useEffect(() => {
    if (!sys || status.isError) return;

    if (restartBaseline.current !== null && sys.startedAt !== restartBaseline.current) {
      void queryClient.invalidateQueries({ queryKey: getHealthQueryKey() });
      notify(`Server is back online (${sys.version}).`);
    }

    restartBaseline.current = isActiveOperation(sys.state) ? sys.startedAt : null;
  }, [sys, status.isError, notify]);

  function applyStatus(next: SystemStatus) {
    if (isActiveOperation(next.state)) restartBaseline.current ??= next.startedAt;

    queryClient.setQueryData(statusKey, next);
  }

  const setChannel = useMutation({
    ...updateSystemSettingsMutation(),
    onSuccess: (data) => {
      applyStatus(data);
      notify(`Update channel set to ${data.updateChannel}.`);
    },
  });

  const checkUpdate = useMutation({
    ...checkSystemUpdateMutation(),
    onSuccess: (data) => {
      applyStatus(data);

      if (data.updateAvailable && data.latest) {
        notify(`Update available: ${data.latest.tag}`);
      } else if (data.latest) {
        notify(`Up to date (latest on ${data.updateChannel}: ${data.latest.tag}).`);
      }
    },
  });

  const upgrade = useMutation({
    ...upgradeSystemMutation(),
    onSuccess: (data) => {
      setConfirmUpgrade(false);
      applyStatus(data);

      if (data.state === "downloading") {
        notify(`Downloading ${data.targetVersion ?? data.latest?.tag ?? "update"}…`);
      } else if (data.state === "restarting") {
        notify("Upgrade applied. Restarting server…");
      }
    },
  });

  const restart = useMutation({
    ...restartSystemMutation(),
    onSuccess: (data) => {
      setConfirmRestart(false);
      applyStatus(data);

      if (data.state === "waiting_idle") {
        notify("Waiting for active runs to finish before restarting.");
      } else if (data.state === "restarting") {
        notify("Restarting server…");
      }
    },
  });

  const cancelOp = useMutation({
    ...cancelSystemOperationMutation(),
    onSuccess: (data) => {
      applyStatus(data);
      notify("Pending server operation cancelled.");
    },
  });

  const backup = useMutation({
    ...backupSystemDatabaseMutation(),
    onSuccess: (data) => {
      void queryClient.invalidateQueries({ queryKey: getSystemStatusQueryKey() });
      notify(`Saved backup ${data.name}.`);
    },
  });

  const busyOp =
    reconnecting || checkUpdate.isPending || sys?.state === "checking" || isActiveOperation(sys?.state);

  return (
    <FramedPage
      eyebrow="Settings for this computer"
      title="About"
      description="Server status, upgrades, backups, and recent logs."
      toolbar={<GlobalSettingsSwitcher page="/settings/about" />}
    >
      <Section title="Appearance" caption="Applies to this browser.">
        <ThemeToggle />
      </Section>

      <Section
        title="This server"
        caption="Check for releases, switch between stable and release candidates, or restart the server in place."
      >
        {health.isPending && status.isPending ? (
          <Loading />
        ) : (health.isError || status.isError) && !reconnecting ? (
          <Failure
            title="Could not reach the server"
            error={status.error ?? health.error}
            retry={() => {
              void queryClient.invalidateQueries({ queryKey: getHealthQueryKey() });
              void status.refetch();
            }}
          />
        ) : (
          <div className="flex flex-col gap-3">
            {reconnecting ? (
              <Note tone="warn">
                <span className="inline-flex items-center gap-2">
                  <Spinner />
                  Server is restarting. Waiting for it to come back online…
                </span>
              </Note>
            ) : null}

            {!reconnecting && sys?.state === "downloading" ? (
              <Note>
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="inline-flex items-center gap-2">
                    <Spinner />
                    Downloading {sys.targetVersion ?? sys.latest?.tag ?? "update"}
                    {sys.downloadDone
                      ? sys.downloadTotal
                        ? ` (${formatBytes(sys.downloadDone)} / ${formatBytes(sys.downloadTotal)})`
                        : ` (${formatBytes(sys.downloadDone)})`
                      : "…"}
                  </span>
                  <Button
                    size="sm"
                    variant="secondary"
                    disabled={cancelOp.isPending}
                    onClick={() => cancelOp.mutate({})}
                  >
                    Cancel
                  </Button>
                </div>
              </Note>
            ) : null}

            {!reconnecting && sys?.state === "waiting_idle" ? (
              <Note tone="warn">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span>
                    {sys.pendingAction === "upgrade"
                      ? `Upgrade to ${sys.targetVersion ?? "new version"} is staged and backed up. Waiting for active runs to finish.`
                      : "Waiting for active runs to finish before restarting."}
                  </span>
                  <div className="flex items-center gap-2">
                    <Button
                      size="sm"
                      variant="secondary"
                      disabled={cancelOp.isPending}
                      onClick={() => cancelOp.mutate({})}
                    >
                      Cancel
                    </Button>
                    <Button
                      size="sm"
                      variant="destructive"
                      className="bg-red text-white"
                      disabled={restart.isPending || upgrade.isPending}
                      onClick={() =>
                        sys.pendingAction === "upgrade"
                          ? upgrade.mutate({ body: { version: sys.targetVersion, force: true } })
                          : restart.mutate({ body: { force: true } })
                      }
                    >
                      Force now
                    </Button>
                  </div>
                </div>
              </Note>
            ) : null}

            {sys?.lastError ? <Failure title="Last server operation failed" error={sys.lastError} /> : null}

            {checkUpdate.isError ? (
              <Failure title="Could not check for updates" error={checkUpdate.error} />
            ) : null}
            {setChannel.isError ? (
              <Failure title="Could not change update channel" error={setChannel.error} />
            ) : null}
            {upgrade.isError ? <Failure title="Could not start upgrade" error={upgrade.error} /> : null}
            {restart.isError ? <Failure title="Could not restart server" error={restart.error} /> : null}
            {cancelOp.isError ? <Failure title="Could not cancel operation" error={cancelOp.error} /> : null}

            <Panel>
              <Facts
                rows={[
                  ["Server", stateBadge(sys?.state ?? "idle", reconnecting, status.isError)],
                  ["Version", <Mono key="version">{sys?.version ?? health.data?.version ?? "—"}</Mono>],
                  [
                    "Started",
                    <span key="started" className="text-[12.5px] text-muted-foreground">
                      {formatTime(sys?.startedAt ?? health.data?.startedAt ?? 0)}
                    </span>,
                  ],
                  [
                    "Update channel",
                    <RadioSegments
                      key="channel"
                      label="Update channel"
                      className="w-56"
                      options={channelOptions}
                      value={sys?.updateChannel ?? "stable"}
                      onChange={(value) => {
                        if (!busyOp && value !== sys?.updateChannel) {
                          setChannel.mutate({ body: { updateChannel: value } });
                        }
                      }}
                    />,
                  ],
                ]}
              />
              <div className="flex flex-wrap items-center gap-2 border-t border-border px-4 py-3">
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={busyOp}
                  onClick={() => checkUpdate.mutate({})}
                >
                  {checkUpdate.isPending ? "Checking…" : "Check for updates"}
                </Button>
                {sys?.updateAvailable && sys.latest && sys.capability.supported ? (
                  <Button
                    size="sm"
                    disabled={busyOp || upgrade.isPending}
                    onClick={() => setConfirmUpgrade(true)}
                  >
                    Upgrade to {sys.latest.tag}
                  </Button>
                ) : null}
                {sys?.capability.canRestart ? (
                  <Button
                    size="sm"
                    variant="secondary"
                    disabled={busyOp || restart.isPending}
                    onClick={() => setConfirmRestart(true)}
                  >
                    Restart server
                  </Button>
                ) : null}
                {sys?.lastCheckedAt ? (
                  <span className="ml-auto text-[11.5px] text-muted-foreground">
                    Checked {formatTime(sys.lastCheckedAt)}
                  </span>
                ) : null}
              </div>
            </Panel>

            {sys && !sys.capability.supported ? (
              <Note>{capabilityReasonText(sys.capability.reason)}</Note>
            ) : null}

            {sys?.latest ? (
              <Panel className="px-4 py-3">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="flex items-center gap-2 text-[13px]">
                    <span className="font-medium text-foreground">
                      Latest on {sys.updateChannel}: <Mono>{sys.latest.tag}</Mono>
                    </span>
                    {sys.latest.prerelease ? (
                      <Badge variant="secondary" className="h-5 px-2 text-[11px]">
                        Prerelease
                      </Badge>
                    ) : null}
                    {sys.updateAvailable ? (
                      <Badge variant="secondary" className="h-5 bg-green-tint px-2 text-[11px] text-green">
                        Update available
                      </Badge>
                    ) : (
                      <Badge variant="secondary" className="h-5 px-2 text-[11px]">
                        Current
                      </Badge>
                    )}
                  </div>
                  <div className="flex items-center gap-2">
                    {sys.latest.notes ? (
                      <Button size="sm" variant="ghost" onClick={() => setShowNotes((open) => !open)}>
                        {showNotes ? "Hide notes" : "Release notes"}
                      </Button>
                    ) : null}
                    {sys.latest.url ? (
                      <a
                        href={sys.latest.url}
                        target="_blank"
                        rel="noreferrer"
                        className="text-[12px] text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
                      >
                        GitHub
                      </a>
                    ) : null}
                  </div>
                </div>
                {showNotes && sys.latest.notes ? (
                  <pre className="mt-3 max-h-64 overflow-auto rounded-md bg-accent/50 p-3 font-mono text-[11.5px] leading-5 whitespace-pre-wrap text-foreground">
                    {sys.latest.notes}
                  </pre>
                ) : null}
              </Panel>
            ) : null}
          </div>
        )}
      </Section>

      <Section
        title="Database backups"
        caption="Upgrades back up control.sqlite automatically before replacing the binary. You can also take a hot backup anytime."
      >
        <div className="flex flex-col gap-3">
          {backup.isError ? <Failure title="Could not create backup" error={backup.error} /> : null}
          <Panel>
            <div className="flex items-center justify-between gap-3 px-4 py-3">
              <span className="text-[12.5px] text-muted-foreground">
                Saved in <Mono>backups/</Mono> inside the data directory.
              </span>
              <Button
                size="sm"
                variant="secondary"
                disabled={reconnecting || backup.isPending}
                onClick={() => backup.mutate({})}
              >
                {backup.isPending ? "Backing up…" : "Back up now"}
              </Button>
            </div>
            {sys?.backups && sys.backups.length > 0 ? (
              <div className="divide-y divide-border border-t border-border">
                {sys.backups.slice(0, 8).map((item) => (
                  <div
                    key={item.name}
                    className="flex flex-wrap items-center justify-between gap-2 px-4 py-2 text-[12.5px]"
                  >
                    <Mono className="truncate">{item.name}</Mono>
                    <span className="text-[11.5px] text-muted-foreground">
                      {formatBytes(item.sizeBytes)} · {formatTime(item.createdAt)}
                    </span>
                  </div>
                ))}
              </div>
            ) : (
              <p className="border-t border-border px-4 py-3 text-[12.5px] text-muted-foreground">
                No backups yet.
              </p>
            )}
          </Panel>
        </div>
      </Section>

      <Section title="Recent logs" caption="In-memory ring buffer of recent dex server log records.">
        <Panel>
          <div className="flex items-center justify-between gap-2 px-4 py-3">
            <span className="text-[12.5px] text-muted-foreground">
              Up to the latest 200 entries from this process.
            </span>
            <div className="flex items-center gap-2">
              {showLogs ? (
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={logs.isFetching}
                  onClick={() => void logs.refetch()}
                >
                  {logs.isFetching ? "Refreshing…" : "Refresh"}
                </Button>
              ) : null}
              <Button size="sm" variant="secondary" onClick={() => setShowLogs((open) => !open)}>
                {showLogs ? "Hide logs" : "Show logs"}
              </Button>
            </div>
          </div>
          {showLogs ? (
            logs.isPending ? (
              <div className="border-t border-border p-4">
                <Loading />
              </div>
            ) : logs.isError ? (
              <div className="border-t border-border p-4">
                <Failure title="Could not load logs" error={logs.error} retry={() => void logs.refetch()} />
              </div>
            ) : (
              <div className="max-h-80 overflow-auto border-t border-border bg-accent/40 p-3 font-mono text-[11.5px] leading-5">
                {logs.data.entries.length === 0 ? (
                  <p className="text-muted-foreground">No log entries recorded yet.</p>
                ) : (
                  logs.data.entries.map((entry) => (
                    <div key={entry.seq} className="flex gap-2 break-all">
                      <span className="shrink-0 text-muted-foreground">
                        {new Date(entry.time).toLocaleTimeString()}
                      </span>
                      <span
                        className={
                          entry.level === "ERROR"
                            ? "shrink-0 font-semibold text-red"
                            : entry.level === "WARN"
                              ? "shrink-0 font-semibold text-orange"
                              : "shrink-0 text-muted-foreground"
                        }
                      >
                        {entry.level}
                      </span>
                      <span className="text-foreground">
                        {entry.message}
                        {entry.attrs ? (
                          <span className="ml-1.5 text-muted-foreground">{entry.attrs}</span>
                        ) : null}
                      </span>
                    </div>
                  ))
                )}
              </div>
            )
          ) : null}
        </Panel>
      </Section>

      <Section title="Files" caption="Everything is in the data directory.">
        {health.data ? (
          <Panel>
            <Facts
              rows={[
                ["Data directory", <Mono key="data">{health.data.dataDir}</Mono>],
                ["Sessions and settings", <Mono key="control">control.sqlite</Mono>],
                ["Transcripts and runs", <Mono key="state">agents/default/state.sqlite</Mono>],
                ["Workspace", <Mono key="workspace">agents/default/workspace</Mono>],
                ["Memory", <Mono key="memory">memory</Mono>],
              ]}
            />
          </Panel>
        ) : (
          <p className="px-0.5 text-[12.5px] text-muted-foreground">
            The paths show when the server answers.
          </p>
        )}
        <p className="mt-3 px-0.5 text-[12px] text-muted-foreground">
          Set <Mono>DEX_DATA_DIR</Mono> to use a different directory.
        </p>
      </Section>

      <Section title="Safety">
        <div className="flex flex-col gap-3">
          <Note tone="warn">
            Host tools run without a sandbox. Keep dex on 127.0.0.1 unless you set an access token.
          </Note>
          <Panel className="px-4 py-3.5">
            <ul className="flex list-disc flex-col gap-1.5 pl-5 text-[13px] leading-6 text-muted-foreground marker:text-muted-foreground">
              <li>Without an access token, any program that can reach the port can use dex.</li>
              <li>The bot runs read, write, edit and bash on this computer without a sandbox.</li>
              <li>Allowed Telegram users reach the same bot, tools and memory.</li>
              <li>Keys and tokens are write-only in the API and plain text in the data directory.</li>
            </ul>
          </Panel>
        </div>
      </Section>

      <Section title="This design">
        <p className="px-0.5 text-[12.5px] leading-relaxed text-muted-foreground">
          Built with shadcn/ui components and design inspiration from Beautiful UI by slev12397 (MIT).
        </p>
      </Section>

      {confirmRestart ? (
        <Confirm
          title="Restart dex server?"
          description="Dex will wait until active runs finish (or restart immediately if idle) and reconnect this page automatically."
          confirmLabel="Restart"
          pending={restart.isPending}
          error={restart.error}
          onCancel={() => setConfirmRestart(false)}
          onConfirm={() => restart.mutate({ body: { force: false } })}
        />
      ) : null}

      {confirmUpgrade && sys?.latest ? (
        <Confirm
          title={`Upgrade dex to ${sys.latest.tag}?`}
          description="Dex will download and verify the release archive, back up control.sqlite automatically, wait until active runs settle, replace the executable, and restart in place."
          confirmLabel={`Upgrade to ${sys.latest.tag}`}
          pending={upgrade.isPending}
          error={upgrade.error}
          onCancel={() => setConfirmUpgrade(false)}
          onConfirm={() => upgrade.mutate({ body: { version: sys.latest?.tag, force: false } })}
        />
      ) : null}
    </FramedPage>
  );
}
