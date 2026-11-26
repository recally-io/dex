import type { Runner, RunnerConfig, SshHostEntry } from "../api/generated/types.gen";

export type SandboxPreset = "cloud" | "local" | "custom";

function isCustomE2BConfig(cfg: RunnerConfig): boolean {
  return !!(cfg.apiUrl || cfg.domain || cfg.envdUrl);
}

function isLocalHostname(host: string): boolean {
  const clean = host.trim().toLowerCase().replace(/\.+$/, "");

  return clean === "localhost" || clean === "127.0.0.1" || clean === "::1" || clean.endsWith(".localhost");
}

function urlHostname(raw: string): string {
  const expanded = raw.replaceAll("{sandboxId}", "sbx").replaceAll("{port}", "49983");

  try {
    return new URL(expanded).hostname;
  } catch {
    return "";
  }
}

export function detectSandboxPreset(cfg: RunnerConfig): SandboxPreset {
  if (!isCustomE2BConfig(cfg)) return "cloud";

  if (
    (cfg.apiUrl && isLocalHostname(urlHostname(cfg.apiUrl))) ||
    (cfg.envdUrl && isLocalHostname(urlHostname(cfg.envdUrl))) ||
    (cfg.domain && isLocalHostname(cfg.domain))
  ) {
    return "local";
  }

  return "custom";
}

// runnerKindBadge names how Dex reaches the environment. It never shows the stored
// kind, because a kind names our transport and the user thinks in machines.
export function runnerKindBadge(env: Pick<Runner, "kind" | "config">): string {
  if (env.kind === "pi-env") return "Machine · SSH";

  if (env.kind === "runner") return "Machine · Outbound";

  const preset = detectSandboxPreset(env.config);

  return `Sandbox · ${{ cloud: "E2B", local: "Local Docker / VM", custom: "Custom" }[preset]}`;
}

export function runnerOptionLabel(env: Pick<Runner, "name" | "kind" | "config">): string {
  return `${env.name} (${runnerKindBadge(env)})`;
}

export function runnerBotHint(env: Pick<Runner, "name" | "kind" | "config">): string {
  if (env.kind === "pi-env") {
    return `Runs tools on ${env.name} over SSH (${formatSshTarget(env.config)}).`;
  }

  if (env.kind === "runner") return `Runs tools on ${env.name} through dex-agent.`;

  if (detectSandboxPreset(env.config) === "local") {
    return `Runs tools in ${env.name}, a local sandbox.`;
  }

  return `Runs tools in ${env.name}, an E2B sandbox.`;
}

// machineAddress returns the canonical HTTPS origin of a machine listener, or null when the
// target machine cannot use the address.
export function machineAddress(address: string): string | null {
  let url: URL;

  try {
    url = new URL(address.trim());
  } catch {
    return null;
  }

  const host = url.hostname.replace(/^\[|\]$/g, "");

  if (
    url.protocol !== "https:" ||
    url.username ||
    url.password ||
    url.pathname !== "/" ||
    url.search ||
    url.hash ||
    isLocalHostname(host) ||
    host === "0.0.0.0" ||
    host === "::" ||
    url.origin.includes("'")
  ) {
    return null;
  }

  return url.origin;
}

// machineInstallCommand builds the one command that enrolls a machine. The machine trades the
// code for a token. By default, the runner uses the account that runs the command.
export function machineInstallCommand(address: string, id: string, code: string): string | null {
  const origin = machineAddress(address);

  if (!origin || !/^[a-z\d-]+$/i.test(id) || !/^[A-Z\d]{20,64}$/i.test(code)) {
    return null;
  }

  return `curl -fsSL '${origin}/install-machine.sh' | sh -s -- '${origin}' '${id}' '${code}'`;
}

export function formatSshTarget(cfg: RunnerConfig): string {
  const prefix = cfg.user ? `${cfg.user}@` : "";
  const suffix = cfg.port ? `:${cfg.port}` : "";

  return `${prefix}${cfg.host ?? ""}${suffix}`;
}

export function formatResolvedSshHost(entry: SshHostEntry): string {
  const prefix = entry.user ? `${entry.user}@` : "";
  const host = entry.hostName || entry.alias;
  const suffix = entry.port ? `:${entry.port}` : "";
  const jump = entry.proxyJump ? ` via ${entry.proxyJump}` : "";

  return `${prefix}${host}${suffix}${jump}`;
}

export function formatSandboxRuntimeSummary(cfg: RunnerConfig): string {
  if (cfg.apiUrl) return cfg.apiUrl;

  if (cfg.domain) return `https://api.${cfg.domain}`;

  if (cfg.envdUrl) return cfg.envdUrl;

  return "E2B Cloud (e2b.app)";
}

export function formatSandboxDiskSummary(cfg: RunnerConfig): string {
  const ws = cfg.workspaceDir || "/home/user/workspace";
  const timeout = cfg.timeoutSeconds ?? 300;

  if (cfg.autoPause === false) {
    return `Ephemeral · ${ws} (wipes after ${timeout}s idle or reset)`;
  }

  return `Snapshot-persisted · ${ws} (pauses after ${timeout}s idle)`;
}

export function formatSshDiskSummary(cfg: RunnerConfig): string {
  const base = cfg.workspaceDir || "~/.dex/agents";

  return `Persistent · ${base}/<bot-id>/workspace`;
}
