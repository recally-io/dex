import { expect, test } from "vite-plus/test";
import {
  detectSandboxPreset,
  environmentBotHint,
  environmentKindBadge,
  environmentOptionLabel,
  formatSandboxDiskSummary,
  machineInstallCommand,
} from "./environments";

test("environment labels avoid internal pi-env jargon and distinguish local sandboxes", () => {
  const ssh = { name: "MacMini", kind: "pi-env" as const, config: { host: "macmini", user: "ops" } };
  const cloud = { name: "Cloud", kind: "e2b" as const, config: { template: "base" } };

  const localDocker = {
    name: "Docker Gateway",
    kind: "e2b" as const,
    config: { apiUrl: "http://localhost:3000", autoPause: false, timeoutSeconds: 3600 },
  };

  const remoteCustom = {
    name: "Remote K8s",
    kind: "e2b" as const,
    config: { apiUrl: "https://sandbox.internal:8443" },
  };

  expect(environmentKindBadge(ssh)).toBe("SSH");
  expect(environmentKindBadge(cloud)).toBe("E2B sandbox");
  expect(environmentKindBadge(localDocker)).toBe("Local sandbox");
  expect(environmentKindBadge(remoteCustom)).toBe("E2B sandbox");

  expect(environmentOptionLabel(ssh)).toBe("MacMini (SSH)");
  expect(environmentOptionLabel(localDocker)).toBe("Docker Gateway (Local sandbox)");
  expect(environmentBotHint(ssh)).toBe("Runs tools on MacMini over SSH (ops@macmini).");

  expect(detectSandboxPreset(cloud.config)).toBe("cloud");
  expect(detectSandboxPreset(localDocker.config)).toBe("local");
  expect(detectSandboxPreset(remoteCustom.config)).toBe("custom");
  expect(formatSandboxDiskSummary(localDocker.config)).toContain("Ephemeral");
  expect(formatSandboxDiskSummary(cloud.config)).toContain("Snapshot-persisted");
});

test("machine setup uses one install command", () => {
  const code = "ABCDEFGH23456789ABCDEFGH";
  expect(machineInstallCommand("http://192.0.2.10", "env-id", code)).toBeNull();
  expect(machineInstallCommand("https://127.0.0.1", "env-id", code)).toBeNull();
  expect(machineInstallCommand("https://user@192.0.2.10", "env-id", code)).toBeNull();
  expect(machineInstallCommand("https://192.0.2.10", "env-id", "short")).toBeNull();
  expect(machineInstallCommand("https://192.0.2.10/", "env-id", code)).toBe(
    `curl -fsSL 'https://192.0.2.10/install-machine.sh' | sh -s -- 'https://192.0.2.10' 'env-id' '${code}'`,
  );
});
