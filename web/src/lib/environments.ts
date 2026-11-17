import type { Environment, EnvironmentConfig, SshHostEntry } from "../api/generated/types.gen";

export type SandboxPreset = "cloud" | "local" | "custom";

export function isCustomE2BConfig(cfg: EnvironmentConfig): boolean {
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

export function detectSandboxPreset(cfg: EnvironmentConfig): SandboxPreset {
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

export function environmentKindBadge(env: Pick<Environment, "kind" | "config">): string {
  if (env.kind === "pi-env") return "SSH";

  return detectSandboxPreset(env.config) === "local" ? "Local sandbox" : "Sandbox";
}

export function environmentOptionLabel(env: Pick<Environment, "name" | "kind" | "config">): string {
  return `${env.name} (${environmentKindBadge(env)})`;
}

export function environmentBotHint(env: Pick<Environment, "name" | "kind" | "config">): string {
  if (env.kind === "pi-env") {
    return `Runs tools on SSH machine ${env.name} (${formatSshTarget(env.config)}).`;
  }

  if (detectSandboxPreset(env.config) === "local") {
    return `Runs tools in local sandbox ${env.name}.`;
  }

  return `Runs tools in isolated sandbox ${env.name}.`;
}

export function formatSshTarget(cfg: EnvironmentConfig): string {
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

export function formatSandboxRuntimeSummary(cfg: EnvironmentConfig): string {
  if (cfg.apiUrl) return cfg.apiUrl;

  if (cfg.domain) return `https://api.${cfg.domain}`;

  if (cfg.envdUrl) return cfg.envdUrl;

  return "E2B Cloud (e2b.app)";
}

export function formatSandboxDiskSummary(cfg: EnvironmentConfig): string {
  const ws = cfg.workspaceDir || "/home/user/workspace";
  const timeout = cfg.timeoutSeconds ?? 300;

  if (cfg.autoPause === false) {
    return `Ephemeral · ${ws} (wipes after ${timeout}s idle or reset)`;
  }

  return `Snapshot-persisted · ${ws} (pauses after ${timeout}s idle)`;
}

export function formatSshDiskSummary(cfg: EnvironmentConfig): string {
  const base = cfg.workspaceDir || "~/.dex/agents";

  return `Persistent · ${base}/<bot-id>/workspace`;
}
