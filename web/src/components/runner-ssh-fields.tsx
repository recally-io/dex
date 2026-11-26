import { useId, useState, type ClipboardEvent } from "react";
import type { Runner, RunnerConfig, SshHostEntry } from "../api/generated/types.gen";
import { formatResolvedSshHost } from "../lib/runners";
import { parseSshTargetInput } from "../lib/ssh-parse";
import { Field, Input } from "./form";
import { Note } from "./page";
import { Button } from "./ui/button";
import { Switch } from "./ui/switch";

type SshFormController = {
  errors: Partial<Record<"name" | "host" | "port" | "workspaceDir", string>>;
  hasConfigErrors: boolean;
  hasErrors: boolean;
  submitBody: () => { name: string; kind: "ssh"; config: RunnerConfig };
  probeBody: (runnerId?: string) => {
    runnerId?: string;
    kind: "ssh";
    config: RunnerConfig;
  };
  viewProps: {
    isEditing: boolean;
    name: string;
    onNameChange: (value: string) => void;
    host: string;
    onHostChange: (value: string) => void;
    onHostSelectAlias: (alias: string) => void;
    onHostPaste: (event: ClipboardEvent<HTMLInputElement>) => void;
    user: string;
    onUserChange: (value: string) => void;
    port: string;
    onPortChange: (value: string) => void;
    identityFile: string;
    onIdentityFileChange: (value: string) => void;
    loginShell: boolean;
    onLoginShellChange: (checked: boolean) => void;
    workspaceDir: string;
    onWorkspaceDirChange: (value: string) => void;
    showAdvanced: boolean;
    onToggleAdvanced: () => void;
    matchedSshHost?: SshHostEntry;
  };
};

export function useSshRunnerForm(
  existing: Runner | undefined,
  sshHosts: SshHostEntry[],
  onChangeResetProbe: () => void,
): SshFormController {
  const [name, setName] = useState(existing?.name ?? "");
  const [nameEdited, setNameEdited] = useState(!!existing);
  const [host, setHost] = useState(existing?.config.host ?? "");
  const [user, setUser] = useState(existing?.config.user ?? "");
  const [port, setPort] = useState(existing?.config.port ? String(existing.config.port) : "");
  const [identityFile, setIdentityFile] = useState(existing?.config.identityFile ?? "");
  const [loginShell, setLoginShell] = useState(existing?.config.loginShell ?? true);
  const [workspaceDir, setWorkspaceDir] = useState(existing?.config.workspaceDir ?? "");

  const [showAdvanced, setShowAdvanced] = useState(
    !!(
      existing &&
      (existing.config.identityFile || existing.config.workspaceDir || existing.config.loginShell === false)
    ),
  );

  const trimmedPort = port.trim();
  const parsedPort = trimmedPort ? Number(trimmedPort) : undefined;
  const trimmedWS = workspaceDir.trim();
  const matchedSshHost = sshHosts.find((item) => item.alias === host.trim());

  const errors: SshFormController["errors"] = {};

  if (!name.trim() || Array.from(name.trim()).length > 60) {
    errors.name = "Enter a name between 1 and 60 characters.";
  }

  if (!host.trim()) {
    errors.host = "Enter a host name, IP address, or ~/.ssh/config alias.";
  }

  if (
    trimmedPort &&
    (parsedPort === undefined || !Number.isInteger(parsedPort) || parsedPort < 1 || parsedPort > 65535)
  ) {
    errors.port = "Port must be between 1 and 65535.";
  }

  if (trimmedWS && !trimmedWS.startsWith("/") && trimmedWS !== "~" && !trimmedWS.startsWith("~/")) {
    errors.workspaceDir = "Use an absolute path or start with ~.";
  }

  function buildConfig(): RunnerConfig {
    return {
      host: host.trim(),
      user: user.trim() || undefined,
      port: parsedPort,
      identityFile: identityFile.trim() || undefined,
      loginShell,
      workspaceDir: trimmedWS || undefined,
    };
  }

  function handleHostSelectAlias(alias: string) {
    onChangeResetProbe();
    setHost(alias);
    setUser("");
    setPort("");
    setIdentityFile("");

    if (!nameEdited) setName(alias);
  }

  function handleHostPaste(event: ClipboardEvent<HTMLInputElement>) {
    const pasted = event.clipboardData.getData("text");
    const parsed = parseSshTargetInput(pasted);

    if (!parsed) return;

    event.preventDefault();
    onChangeResetProbe();
    setHost(parsed.host);

    if (parsed.user) setUser(parsed.user);

    if (parsed.port !== undefined) setPort(String(parsed.port));

    if (parsed.identityFile) setIdentityFile(parsed.identityFile);

    if (!existing && !nameEdited && parsed.suggestedName) setName(parsed.suggestedName);
  }

  return {
    errors,
    hasConfigErrors: !!(errors.host || errors.port || errors.workspaceDir),
    hasErrors: Object.keys(errors).length > 0,
    submitBody: () => ({ name: name.trim(), kind: "ssh", config: buildConfig() }),
    probeBody: (runnerId?: string) => ({ runnerId, kind: "ssh", config: buildConfig() }),
    viewProps: {
      isEditing: !!existing,
      name,
      onNameChange: (value) => {
        setName(value);
        setNameEdited(true);
      },
      host,
      onHostChange: (value) => {
        setHost(value);
        onChangeResetProbe();

        if (!existing && !nameEdited) setName(value.trim());
      },
      onHostSelectAlias: handleHostSelectAlias,
      onHostPaste: handleHostPaste,
      user,
      onUserChange: (value) => {
        setUser(value);
        onChangeResetProbe();
      },
      port,
      onPortChange: (value) => {
        setPort(value);
        onChangeResetProbe();
      },
      identityFile,
      onIdentityFileChange: (value) => {
        setIdentityFile(value);
        onChangeResetProbe();
      },
      loginShell,
      onLoginShellChange: (checked) => {
        setLoginShell(checked);
        onChangeResetProbe();
      },
      workspaceDir,
      onWorkspaceDirChange: (value) => {
        setWorkspaceDir(value);
        onChangeResetProbe();
      },
      showAdvanced,
      onToggleAdvanced: () => setShowAdvanced((prev) => !prev),
      matchedSshHost,
    },
  };
}

export function SshRunnerFields({
  controller,
  sshHosts,
  identityFiles,
  show,
}: {
  controller: SshFormController;
  sshHosts: SshHostEntry[];
  identityFiles: string[];
  show: (key: keyof SshFormController["errors"]) => string | undefined;
}) {
  const { viewProps } = controller;
  const hostListId = useId();
  const baseDir = viewProps.workspaceDir.trim() || "~/.dex/agents";

  return (
    <>
      <Field label="Name" error={show("name")}>
        {({ id, describedBy, invalid }) => (
          <Input
            id={id}
            aria-describedby={describedBy}
            aria-invalid={invalid}
            placeholder="Build server"
            value={viewProps.name}
            onChange={(event) => viewProps.onNameChange(event.target.value)}
          />
        )}
      </Field>

      <Field
        label="Host"
        hint={
          viewProps.matchedSshHost
            ? `Matches ~/.ssh/config: ${formatResolvedSshHost(viewProps.matchedSshHost)}`
            : "Hostname, ~/.ssh/config alias, or paste ssh user@host -p 22."
        }
        error={show("host")}
      >
        {({ id, describedBy, invalid }) => (
          <div className="flex flex-col gap-2">
            <Input
              id={id}
              list={sshHosts.length > 0 ? hostListId : undefined}
              aria-describedby={describedBy}
              aria-invalid={invalid}
              className="font-mono"
              placeholder="dev.example.com"
              value={viewProps.host}
              onPaste={viewProps.onHostPaste}
              onChange={(event) => viewProps.onHostChange(event.target.value)}
            />
            {sshHosts.length > 0 ? (
              <>
                <datalist id={hostListId}>
                  {sshHosts.map((entry) => (
                    <option key={entry.alias} value={entry.alias}>
                      {formatResolvedSshHost(entry)}
                    </option>
                  ))}
                </datalist>
                {!viewProps.isEditing ? (
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="text-[11px] text-muted-foreground">~/.ssh/config:</span>
                    {sshHosts.map((entry) => (
                      <Button
                        key={entry.alias}
                        type="button"
                        size="xs"
                        variant={viewProps.host.trim() === entry.alias ? "secondary" : "outline"}
                        className="font-mono text-[11px]"
                        onClick={() => viewProps.onHostSelectAlias(entry.alias)}
                      >
                        {entry.alias}
                      </Button>
                    ))}
                  </div>
                ) : null}
              </>
            ) : null}
          </div>
        )}
      </Field>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="User" hint="Optional; leave blank to use SSH config.">
          {({ id, describedBy }) => (
            <Input
              id={id}
              aria-describedby={describedBy}
              className="font-mono"
              placeholder={viewProps.matchedSshHost?.user || "ubuntu"}
              value={viewProps.user}
              onChange={(event) => viewProps.onUserChange(event.target.value)}
            />
          )}
        </Field>

        <Field label="Port" hint="Optional; defaults to SSH config or 22." error={show("port")}>
          {({ id, describedBy, invalid }) => (
            <Input
              id={id}
              aria-describedby={describedBy}
              aria-invalid={invalid}
              className="font-mono"
              placeholder={viewProps.matchedSshHost?.port ? String(viewProps.matchedSshHost.port) : "22"}
              value={viewProps.port}
              onChange={(event) => viewProps.onPortChange(event.target.value)}
            />
          )}
        </Field>
      </div>

      <Note>
        <p className="font-medium text-foreground">
          Disk: Persistent at <span className="font-mono">{baseDir}/&lt;bot-id&gt;/workspace</span>
        </p>
        <p className="mt-1">
          Files persist on the remote filesystem across turns. Local VMs (OrbStack, Lima, Multipass) and
          containers running SSH also connect here.
        </p>
      </Note>

      <div className="flex items-center justify-between border-t border-border pt-3">
        <Button type="button" size="xs" variant="ghost" onClick={viewProps.onToggleAdvanced}>
          {viewProps.showAdvanced ? "Hide advanced options" : "Advanced options"}
        </Button>
        {!viewProps.showAdvanced ? (
          <span className="text-[11.5px] text-muted-foreground">
            {viewProps.loginShell ? "Login shell on" : "Login shell off"}
          </span>
        ) : null}
      </div>

      {viewProps.showAdvanced ? (
        <div className="flex flex-col gap-4 rounded-md border border-border p-3.5">
          <div className="flex items-center justify-between gap-3">
            <div className="min-w-0 flex-1">
              <p className="text-[12.5px] font-medium text-foreground">Use login shell</p>
              <p className="text-[12px] text-muted-foreground">
                Loads PATH and toolchains from ~/.profile, ~/.zprofile, ~/.bashrc, or ~/.zshrc.
              </p>
            </div>
            <Switch
              checked={viewProps.loginShell}
              onCheckedChange={viewProps.onLoginShellChange}
              aria-label="Use login shell"
            />
          </div>

          <Field label="Identity file" hint="Optional private key path on this computer.">
            {({ id, describedBy }) => (
              <div className="flex flex-col gap-2">
                <Input
                  id={id}
                  aria-describedby={describedBy}
                  className="font-mono"
                  placeholder={viewProps.matchedSshHost?.identityFile || "~/.ssh/id_ed25519"}
                  value={viewProps.identityFile}
                  onChange={(event) => viewProps.onIdentityFileChange(event.target.value)}
                />
                {identityFiles.length > 0 ? (
                  <div className="flex flex-wrap gap-1.5">
                    {identityFiles.map((filePath) => (
                      <Button
                        key={filePath}
                        type="button"
                        size="xs"
                        variant={viewProps.identityFile === filePath ? "secondary" : "outline"}
                        className="font-mono text-[11px]"
                        onClick={() => viewProps.onIdentityFileChange(filePath)}
                      >
                        {filePath}
                      </Button>
                    ))}
                  </div>
                ) : null}
              </div>
            )}
          </Field>

          <Field
            label="Workspace directory"
            hint="Optional base directory on the remote host (default ~/.dex/agents)."
            error={show("workspaceDir")}
          >
            {({ id, describedBy, invalid }) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                aria-invalid={invalid}
                className="font-mono"
                placeholder="~/.dex/agents"
                value={viewProps.workspaceDir}
                onChange={(event) => viewProps.onWorkspaceDirChange(event.target.value)}
              />
            )}
          </Field>
        </div>
      ) : null}
    </>
  );
}
