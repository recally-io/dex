import { useState } from "react";
import type { Runner, RunnerConfig } from "../api/generated/types.gen";
import { detectSandboxPreset, type SandboxPreset } from "../lib/runners";
import { Field, Input, RadioSegments, SavedSecret } from "./form";
import { Note } from "./page";
import { Button } from "./ui/button";
import { Switch } from "./ui/switch";

const sandboxPresets = [
  { value: "cloud", label: "E2B Cloud" },
  { value: "local", label: "Local Docker / VM" },
  { value: "custom", label: "Custom endpoint" },
] as const;

type SandboxFormController = {
  errors: Partial<
    Record<"name" | "apiUrl" | "secret" | "template" | "timeoutSeconds" | "workspaceDir", string>
  >;
  hasConfigErrors: boolean;
  hasErrors: boolean;
  submitBody: () => { name: string; kind: "e2b"; config: RunnerConfig; secret?: string };
  probeBody: (runnerId?: string) => {
    runnerId?: string;
    kind: "e2b";
    config: RunnerConfig;
    secret?: string;
  };
  viewProps: {
    preset: SandboxPreset;
    onPresetChange: (next: SandboxPreset) => void;
    name: string;
    onNameChange: (value: string) => void;
    apiUrl: string;
    onApiUrlChange: (value: string) => void;
    domain: string;
    onDomainChange: (value: string) => void;
    envdUrl: string;
    onEnvdUrlChange: (value: string) => void;
    secret: string;
    onSecretChange: (value: string) => void;
    secretSet: boolean;
    replaceSecret: boolean;
    onStartReplaceSecret: () => void;
    template: string;
    onTemplateChange: (value: string) => void;
    timeoutSeconds: string;
    onTimeoutSecondsChange: (value: string) => void;
    autoPause: boolean;
    onAutoPauseChange: (checked: boolean) => void;
    workspaceDir: string;
    onWorkspaceDirChange: (value: string) => void;
    showAdvanced: boolean;
    onToggleAdvanced: () => void;
  };
};

function hasInvalidURL(raw: string): boolean {
  try {
    const parsed = new URL(raw);

    return (
      (parsed.protocol !== "http:" && parsed.protocol !== "https:") ||
      !!parsed.username ||
      !!parsed.password ||
      !!parsed.search ||
      !!parsed.hash
    );
  } catch {
    return true;
  }
}

export function useSandboxRunnerForm(
  existing: Runner | undefined,
  onChangeResetProbe: () => void,
): SandboxFormController {
  const initialPreset = existing ? detectSandboxPreset(existing.config) : "cloud";
  const [preset, setPreset] = useState<SandboxPreset>(initialPreset);
  const [name, setName] = useState(existing?.name ?? "");
  const [apiUrl, setApiUrl] = useState(existing?.config.apiUrl ?? "");
  const [domain, setDomain] = useState(existing?.config.domain ?? "");
  const [envdUrl, setEnvdUrl] = useState(existing?.config.envdUrl ?? "");
  const [secret, setSecret] = useState("");
  const [replaceSecret, setReplaceSecret] = useState(!existing?.secretSet);
  const [template, setTemplate] = useState(existing?.config.template ?? "base");
  const [timeoutSeconds, setTimeoutSeconds] = useState(String(existing?.config.timeoutSeconds ?? 300));
  const [autoPause, setAutoPause] = useState(existing?.config.autoPause ?? true);
  const [workspaceDir, setWorkspaceDir] = useState(existing?.config.workspaceDir ?? "");

  const hasNonDefaultAdvanced = !!(
    existing &&
    (existing.config.domain ||
      existing.config.envdUrl ||
      existing.config.workspaceDir ||
      (existing.config.template && existing.config.template !== "base") ||
      (initialPreset === "local"
        ? existing.secretSet ||
          existing.config.autoPause === true ||
          (existing.config.timeoutSeconds ?? 3600) !== 3600
        : existing.config.autoPause === false || (existing.config.timeoutSeconds ?? 300) !== 300))
  );

  const [showAdvanced, setShowAdvanced] = useState(hasNonDefaultAdvanced);

  const trimmedTimeout = timeoutSeconds.trim();
  const parsedTimeout = trimmedTimeout ? Number(trimmedTimeout) : 300;
  const trimmedWS = workspaceDir.trim();
  const trimmedApiUrl = apiUrl.trim();
  const trimmedDomain = domain.trim();
  const trimmedEnvdUrl = envdUrl.trim();

  const errors: SandboxFormController["errors"] = {};

  if (!name.trim() || Array.from(name.trim()).length > 60) {
    errors.name = "Enter a name between 1 and 60 characters.";
  }

  if (preset !== "cloud") {
    if (!trimmedApiUrl && !trimmedDomain && !trimmedEnvdUrl) {
      errors.apiUrl =
        preset === "local"
          ? "Enter your local gateway URL (for example http://localhost:3000)."
          : "Enter the control-plane API URL (for example https://api.sandbox.example.com).";
    } else if (trimmedApiUrl && hasInvalidURL(trimmedApiUrl)) {
      errors.apiUrl = "Enter a clean http or https URL without credentials or query parameters.";
    }
  } else if (!existing?.secretSet && !secret.trim()) {
    errors.secret = "Enter your E2B API key.";
  }

  if (!template.trim()) {
    errors.template = "Enter a template or image ID (for example base).";
  }

  if (!Number.isInteger(parsedTimeout) || parsedTimeout < 1 || parsedTimeout > 86400) {
    errors.timeoutSeconds = "Timeout must be between 1 and 86400 seconds.";
  }

  if (trimmedWS && !trimmedWS.startsWith("/") && trimmedWS !== "~" && !trimmedWS.startsWith("~/")) {
    errors.workspaceDir = "Use an absolute path or start with ~.";
  }

  function handlePresetChange(next: SandboxPreset) {
    setPreset(next);
    onChangeResetProbe();

    if (!existing) {
      if (next === "local") {
        setAutoPause(false);
        setTimeoutSeconds("3600");
      } else {
        setAutoPause(true);
        setTimeoutSeconds("300");
      }
    }
  }

  function buildConfig(): RunnerConfig {
    return {
      apiUrl: preset === "cloud" ? undefined : trimmedApiUrl || undefined,
      domain: preset === "cloud" ? undefined : trimmedDomain || undefined,
      envdUrl: preset === "cloud" ? undefined : trimmedEnvdUrl || undefined,
      template: template.trim() || "base",
      timeoutSeconds: parsedTimeout,
      autoPause,
      workspaceDir: trimmedWS || undefined,
    };
  }

  return {
    errors,
    hasConfigErrors: !!(
      errors.apiUrl ||
      errors.secret ||
      errors.template ||
      errors.timeoutSeconds ||
      errors.workspaceDir
    ),
    hasErrors: Object.keys(errors).length > 0,
    submitBody: () => ({
      name: name.trim(),
      kind: "e2b",
      config: buildConfig(),
      secret: secret.trim() || undefined,
    }),
    probeBody: (runnerId?: string) => ({
      runnerId,
      kind: "e2b",
      config: buildConfig(),
      secret: secret.trim() || undefined,
    }),
    viewProps: {
      preset,
      onPresetChange: handlePresetChange,
      name,
      onNameChange: setName,
      apiUrl,
      onApiUrlChange: (value) => {
        setApiUrl(value);
        onChangeResetProbe();
      },
      domain,
      onDomainChange: (value) => {
        setDomain(value);
        onChangeResetProbe();
      },
      envdUrl,
      onEnvdUrlChange: (value) => {
        setEnvdUrl(value);
        onChangeResetProbe();
      },
      secret,
      onSecretChange: (value) => {
        setSecret(value);
        onChangeResetProbe();
      },
      secretSet: !!existing?.secretSet,
      replaceSecret,
      onStartReplaceSecret: () => setReplaceSecret(true),
      template,
      onTemplateChange: (value) => {
        setTemplate(value);
        onChangeResetProbe();
      },
      timeoutSeconds,
      onTimeoutSecondsChange: (value) => {
        setTimeoutSeconds(value);
        onChangeResetProbe();
      },
      autoPause,
      onAutoPauseChange: (checked) => {
        setAutoPause(checked);
        onChangeResetProbe();
      },
      workspaceDir,
      onWorkspaceDirChange: (value) => {
        setWorkspaceDir(value);
        onChangeResetProbe();
      },
      showAdvanced,
      onToggleAdvanced: () => setShowAdvanced((prev) => !prev),
    },
  };
}

export function SandboxRunnerFields({
  controller,
  show,
}: {
  controller: SandboxFormController;
  show: (key: keyof SandboxFormController["errors"]) => string | undefined;
}) {
  const { viewProps } = controller;
  const effectiveWS = viewProps.workspaceDir.trim() || "/home/user/workspace";
  const effectiveTimeout = viewProps.timeoutSeconds.trim() || (viewProps.preset === "local" ? "3600" : "300");

  const namePlaceholder =
    viewProps.preset === "cloud"
      ? "E2B Sandbox"
      : viewProps.preset === "local"
        ? "Local Docker sandbox"
        : "Custom sandbox";

  const renderSecretField = (optional: boolean) => {
    if (viewProps.secretSet && !viewProps.replaceSecret) {
      return (
        <SavedSecret
          label="API key"
          text="Saved API key"
          hint="Stored write-only on this computer. Leave unchanged or replace it."
          action="Replace"
          onReplace={viewProps.onStartReplaceSecret}
        />
      );
    }

    return (
      <Field
        label={optional ? "API key (optional)" : "API key"}
        hint={
          optional
            ? "Optional write-only key. Leave blank if your gateway requires no auth."
            : "Write-only E2B API key. Never shown after saving."
        }
        error={show("secret")}
      >
        {({ id, describedBy, invalid }) => (
          <Input
            id={id}
            type="password"
            autoComplete="off"
            aria-describedby={describedBy}
            aria-invalid={invalid}
            className="font-mono"
            placeholder="e2b_..."
            value={viewProps.secret}
            onChange={(event) => viewProps.onSecretChange(event.target.value)}
          />
        )}
      </Field>
    );
  };

  return (
    <>
      <div className="flex flex-col gap-1.5">
        <span className="text-[12.5px] font-medium text-foreground">Runtime</span>
        <RadioSegments
          label="Sandbox runtime"
          options={sandboxPresets}
          value={viewProps.preset}
          onChange={viewProps.onPresetChange}
        />
      </div>

      {viewProps.preset === "local" ? (
        <Note>
          <p className="font-medium text-foreground">Connecting Docker or local microVMs</p>
          <p className="mt-1">
            Point Gateway URL at an E2B-compatible local gateway (such as Docker via{" "}
            <span className="font-mono">edvabe</span> or microVMs via{" "}
            <span className="font-mono">NovitaBox</span>). For a persistent VM or container with SSH, pick{" "}
            <strong className="font-medium text-foreground">SSH Machine</strong> instead.
          </p>
        </Note>
      ) : null}

      <Field label="Name" error={show("name")}>
        {({ id, describedBy, invalid }) => (
          <Input
            id={id}
            aria-describedby={describedBy}
            aria-invalid={invalid}
            placeholder={namePlaceholder}
            value={viewProps.name}
            onChange={(event) => viewProps.onNameChange(event.target.value)}
          />
        )}
      </Field>

      {viewProps.preset === "cloud" ? renderSecretField(false) : null}

      {viewProps.preset === "local" ? (
        <>
          <Field
            label="Gateway URL"
            hint="Local E2B API gateway (for example http://localhost:3000)."
            error={show("apiUrl")}
          >
            {({ id, describedBy, invalid }) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                aria-invalid={invalid}
                className="font-mono"
                placeholder="http://localhost:3000"
                value={viewProps.apiUrl}
                onChange={(event) => viewProps.onApiUrlChange(event.target.value)}
              />
            )}
          </Field>

          <Field
            label="Template / Image"
            hint="Template alias or image name served by your local gateway."
            error={show("template")}
          >
            {({ id, describedBy, invalid }) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                aria-invalid={invalid}
                className="font-mono"
                placeholder="base"
                value={viewProps.template}
                onChange={(event) => viewProps.onTemplateChange(event.target.value)}
              />
            )}
          </Field>
        </>
      ) : null}

      {viewProps.preset === "custom" ? (
        <>
          <Field
            label="API URL"
            hint="E2B-compatible control-plane URL (supports Kubernetes, Alibaba, Novita, self-hosted)."
            error={show("apiUrl")}
          >
            {({ id, describedBy, invalid }) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                aria-invalid={invalid}
                className="font-mono"
                placeholder="https://api.sandbox.example.com"
                value={viewProps.apiUrl}
                onChange={(event) => viewProps.onApiUrlChange(event.target.value)}
              />
            )}
          </Field>

          {renderSecretField(true)}
        </>
      ) : null}

      <Note>
        <p className="font-medium text-foreground">
          {viewProps.autoPause
            ? `Disk: Snapshot-persisted at ${effectiveWS}`
            : `Disk: Ephemeral at ${effectiveWS}`}
        </p>
        <p className="mt-1">
          {viewProps.autoPause
            ? `Isolated from this computer. Files survive idle pauses (snapshot after ${effectiveTimeout}s) until you click Reset sandbox.`
            : `Isolated from this computer (no host folder mounts unless your template provides them). Files are wiped when the sandbox expires after ${effectiveTimeout}s or when you click Reset sandbox.`}
        </p>
      </Note>

      <div className="flex items-center justify-between border-t border-border pt-3">
        <Button type="button" size="xs" variant="ghost" onClick={viewProps.onToggleAdvanced}>
          {viewProps.showAdvanced ? "Hide advanced options" : "Advanced options"}
        </Button>
        {!viewProps.showAdvanced ? (
          <span className="text-[11.5px] text-muted-foreground">
            {viewProps.template || "base"} · {effectiveTimeout}s ·{" "}
            {viewProps.autoPause ? "auto-pause on" : "auto-pause off"}
          </span>
        ) : null}
      </div>

      {viewProps.showAdvanced ? (
        <div className="flex flex-col gap-4 rounded-md border border-border p-3.5">
          <div className="flex items-center justify-between gap-3">
            <div className="min-w-0 flex-1">
              <p className="text-[12.5px] font-medium text-foreground">Auto-pause on idle</p>
              <p className="text-[12px] text-muted-foreground">
                Keep on for cloud or microVM snapshots. Turn off for Docker gateways without snapshot pause.
              </p>
            </div>
            <Switch
              checked={viewProps.autoPause}
              onCheckedChange={viewProps.onAutoPauseChange}
              aria-label="Auto-pause on idle"
            />
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            {viewProps.preset !== "local" ? (
              <Field label="Template" hint="Sandbox template or image ID." error={show("template")}>
                {({ id, describedBy, invalid }) => (
                  <Input
                    id={id}
                    aria-describedby={describedBy}
                    aria-invalid={invalid}
                    className="font-mono"
                    placeholder="base"
                    value={viewProps.template}
                    onChange={(event) => viewProps.onTemplateChange(event.target.value)}
                  />
                )}
              </Field>
            ) : null}

            <Field
              label={viewProps.autoPause ? "Auto-pause timeout (s)" : "Sandbox timeout (s)"}
              hint={
                viewProps.autoPause ? "Idle delay before snapshot." : "Lifetime before container expires."
              }
              error={show("timeoutSeconds")}
            >
              {({ id, describedBy, invalid }) => (
                <Input
                  id={id}
                  aria-describedby={describedBy}
                  aria-invalid={invalid}
                  className="font-mono"
                  placeholder={viewProps.preset === "local" ? "3600" : "300"}
                  value={viewProps.timeoutSeconds}
                  onChange={(event) => viewProps.onTimeoutSecondsChange(event.target.value)}
                />
              )}
            </Field>
          </div>

          <Field
            label="Workspace directory"
            hint="Directory inside the sandbox (default /home/user/workspace)."
            error={show("workspaceDir")}
          >
            {({ id, describedBy, invalid }) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                aria-invalid={invalid}
                className="font-mono"
                placeholder="/home/user/workspace"
                value={viewProps.workspaceDir}
                onChange={(event) => viewProps.onWorkspaceDirChange(event.target.value)}
              />
            )}
          </Field>

          {viewProps.preset === "local" ? renderSecretField(true) : null}

          {viewProps.preset !== "cloud" ? (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Sandbox domain" hint="Optional; derived from API URL if omitted.">
                {({ id, describedBy }) => (
                  <Input
                    id={id}
                    aria-describedby={describedBy}
                    className="font-mono"
                    placeholder="sandbox.example.com"
                    value={viewProps.domain}
                    onChange={(event) => viewProps.onDomainChange(event.target.value)}
                  />
                )}
              </Field>

              <Field
                label="Envd gateway URL"
                hint="Optional direct URL or {sandboxId}/{port} proxy template."
              >
                {({ id, describedBy }) => (
                  <Input
                    id={id}
                    aria-describedby={describedBy}
                    className="font-mono"
                    placeholder="http://localhost:3000"
                    value={viewProps.envdUrl}
                    onChange={(event) => viewProps.onEnvdUrlChange(event.target.value)}
                  />
                )}
              </Field>
            </div>
          ) : null}
        </div>
      ) : null}
    </>
  );
}
