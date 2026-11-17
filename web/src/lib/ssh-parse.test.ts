import { expect, test } from "vite-plus/test";
import { inferE2BDomain, parseSshTargetInput } from "./ssh-parse";

test("parseSshTargetInput leaves plain hostnames alone", () => {
  expect(parseSshTargetInput("")).toBeNull();
  expect(parseSshTargetInput("dev.example.com")).toBeNull();
  expect(parseSshTargetInput("my-ssh-alias")).toBeNull();
  expect(parseSshTargetInput("fe80::1")).toBeNull();
});

test("parseSshTargetInput parses user@host:port and ssh:// URLs", () => {
  expect(parseSshTargetInput("ubuntu@192.168.1.50:2222")).toEqual({
    host: "192.168.1.50",
    user: "ubuntu",
    port: 2222,
    suggestedName: "192.168.1.50",
  });

  expect(parseSshTargetInput("ssh://deploy@box.internal:2200")).toEqual({
    host: "box.internal",
    user: "deploy",
    port: 2200,
    suggestedName: "box.internal",
  });

  expect(parseSshTargetInput("ubuntu@[::1]:2222")).toEqual({
    host: "::1",
    user: "ubuntu",
    port: 2222,
    suggestedName: "::1",
  });
});

test("parseSshTargetInput parses ssh CLI commands", () => {
  expect(
    parseSshTargetInput(
      "ssh -o StrictHostKeyChecking=no -p 2222 -i ~/.ssh/id_ed25519 ubuntu@dev.example.com",
    ),
  ).toEqual({
    host: "dev.example.com",
    user: "ubuntu",
    port: 2222,
    identityFile: "~/.ssh/id_ed25519",
    suggestedName: "dev.example.com",
  });

  expect(parseSshTargetInput("ssh -l ops -p2202 -- prod-box")).toEqual({
    host: "prod-box",
    user: "ops",
    port: 2202,
    identityFile: undefined,
    suggestedName: "prod-box",
  });
});

test("parseSshTargetInput parses pasted ssh config snippets", () => {
  const snippet = `
Host gpu-box
  HostName 10.20.30.40
  User ml
  Port 2222
  IdentityFile "~/.ssh/gpu key"
`;

  expect(parseSshTargetInput(snippet)).toEqual({
    host: "10.20.30.40",
    user: "ml",
    port: 2222,
    identityFile: "~/.ssh/gpu key",
    suggestedName: "gpu-box",
  });
});

test("inferE2BDomain infers sandbox domain from api.* and localhost URLs", () => {
  expect(inferE2BDomain("https://api.cn-hangzhou.sandbox.aliyuncs.com")).toBe(
    "cn-hangzhou.sandbox.aliyuncs.com",
  );
  expect(inferE2BDomain("https://api.example.com:8443")).toBe("example.com:8443");
  expect(inferE2BDomain("http://localhost:3000")).toBe("localhost:3000");
  expect(inferE2BDomain("http://127.0.0.1:3000")).toBe("");
  expect(inferE2BDomain("not-a-url")).toBe("");
});
