import { expect, test } from "vite-plus/test";
import {
  detectSandboxPreset,
  environmentBotHint,
  environmentKindBadge,
  environmentOptionLabel,
  formatSandboxDiskSummary,
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
