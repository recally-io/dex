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
  expect(environmentKindBadge(cloud)).toBe("Sandbox");
  expect(environmentKindBadge(localDocker)).toBe("Local sandbox");
  expect(environmentKindBadge(remoteCustom)).toBe("Sandbox");

  expect(environmentOptionLabel(ssh)).toBe("MacMini (SSH)");
  expect(environmentOptionLabel(localDocker)).toBe("Docker Gateway (Local sandbox)");
  expect(environmentBotHint(ssh)).toBe("Runs tools on SSH machine MacMini (ops@macmini).");

  expect(detectSandboxPreset(cloud.config)).toBe("cloud");
  expect(detectSandboxPreset(localDocker.config)).toBe("local");
  expect(detectSandboxPreset(remoteCustom.config)).toBe("custom");
  expect(formatSandboxDiskSummary(localDocker.config)).toContain("Ephemeral");
  expect(formatSandboxDiskSummary(cloud.config)).toContain("Snapshot-persisted");
});

test("machine setup uses one pinned install command", () => {
  const pin = "kZ0P1Q2r3S4t5U6v7W8x9Y0z1A2b3C4d5E6f7G8h9I0=";
  const code = "ABCDEFGH23456789ABCDEFGH";
  expect(machineInstallCommand("http://192.0.2.10:8788", "env-id", pin, code, "v1.2.3")).toBeNull();
  expect(machineInstallCommand("https://127.0.0.1:8788", "env-id", pin, code, "v1.2.3")).toBeNull();
  expect(machineInstallCommand("https://user@192.0.2.10:8788", "env-id", pin, code, "v1.2.3")).toBeNull();
  expect(machineInstallCommand("https://192.0.2.10:8788", "env-id", "", code, "v1.2.3")).toBeNull();
  expect(machineInstallCommand("https://192.0.2.10:8788", "env-id", pin, "short", "v1.2.3")).toBeNull();
  expect(machineInstallCommand("https://192.0.2.10:8788/", "env-id", pin, code, "v1.2.3")).toBe(
    `curl -fsSk --pinnedpubkey 'sha256//${pin}' 'https://192.0.2.10:8788/install-machine.sh' | ` +
      `sudo sh -s -- 'https://192.0.2.10:8788' 'env-id' '${pin}' '${code}' 'v1.2.3'`,
  );
  expect(machineInstallCommand("https://192.0.2.10:8788", "env-id", pin, code, "dev")).toContain("'dev'");
});
