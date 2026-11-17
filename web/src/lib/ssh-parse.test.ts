import { expect, test } from "vite-plus/test";
import { parseSshTargetInput } from "./ssh-parse";

test("parseSshTargetInput leaves plain hostnames and multiline blocks alone", () => {
  expect(parseSshTargetInput("")).toBeNull();
  expect(parseSshTargetInput("dev.example.com")).toBeNull();
  expect(parseSshTargetInput("my-ssh-alias")).toBeNull();
  expect(parseSshTargetInput("fe80::1")).toBeNull();
  expect(parseSshTargetInput("Host foo\n  HostName bar")).toBeNull();
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
