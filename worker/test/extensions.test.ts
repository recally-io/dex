// A failed server shows only the category of its error, in the API and in the log, because a raw
// error or the server's output can hold values of its mcp.json entry.
import { afterEach, expect, spyOn, test } from "bun:test";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { AgentExtensions } from "../src/extensions.ts";

const cleanups: (() => Promise<void>)[] = [];
afterEach(async () => {
	await Promise.all(cleanups.splice(0).map((cleanup) => cleanup()));
});

test("a failed MCP server shows no values of its entry", async () => {
	const dir = await mkdtemp(join(tmpdir(), "dex-extensions-"));
	cleanups.push(() => rm(dir, { recursive: true, force: true }));
	// An HTTP server that fails and sends back the request.
	const echo = Bun.serve({
		port: 0,
		fetch: (req) => new Response(`${req.url} ${req.headers.get("authorization")}`, { status: 500 }),
	});
	cleanups.push(() => echo.stop(true));
	await mkdir(join(dir, "home", ".agents"), { recursive: true });
	await writeFile(
		join(dir, "home", ".agents", "mcp.json"),
		JSON.stringify({
			mcpServers: {
				missing: { command: join(dir, "SECRET-COMMAND") },
				crash: {
					command: "/bin/sh",
					args: ["-c", 'echo "$TOKEN $0" >&2; exit 1', "--token=SECRET-ARG"],
					env: { TOKEN: "SECRET" },
				},
				denied: {
					type: "http",
					url: `${echo.url.href}mcp?token=SECRET-QUERY`,
					headers: { Authorization: "Bearer ${TOKEN}" },
				},
				badurl: { type: "http", url: "SECRET-URL" },
			},
		}),
	);
	await writeFile(join(dir, "home", ".agents", ".env"), "TOKEN=SECRET-HEADER\n");
	const log = spyOn(console, "error").mockImplementation(() => undefined);
	cleanups.push(async () => log.mockRestore());

	const loaded = await new AgentExtensions(join(dir, "home"), dir, join(dir, "agent"), dir, {}).load();
	await loaded.close();

	expect(loaded.servers.map((s) => s.state)).toEqual(["failed", "failed", "failed", "failed"]);
	expect(JSON.stringify(loaded.servers)).not.toContain("SECRET");
	const text = log.mock.calls.flat().join("\n");
	expect(text).toContain("MCP server denied: the server answered with HTTP status 500");
	expect(text).toContain("MCP server missing: the command does not exist");
	expect(text).not.toContain("SECRET");
});

test("environment edits round-trip exact values through the loader", async () => {
	const dir = await mkdtemp(join(tmpdir(), "dex-env-edit-"));
	cleanups.push(() => rm(dir, { recursive: true, force: true }));
	const files = new AgentExtensions(join(dir, "home"), dir, join(dir, "agent"), dir, {});
	const values = [
		"",
		"plain",
		"a=b",
		"a#b",
		"a # comment",
		'a"b',
		"a'b",
		" leading",
		"trailing ",
		" spaced '",
		"\t",
		"back\\slash",
		"${LITERAL}",
		"日本語",
	];
	for (const value of values) {
		// oxlint-disable-next-line no-await-in-loop
		await files.editEnv({ agentId: "agent", scope: "agent", key: "ROUND_TRIP", value });
		// oxlint-disable-next-line no-await-in-loop
		const loaded = await files.load();
		expect(loaded.env["ROUND_TRIP"]).toBe(value);
		// oxlint-disable-next-line no-await-in-loop
		await loaded.close();
	}
});

test("concurrent agents keep every shared environment edit", async () => {
	const dir = await mkdtemp(join(tmpdir(), "dex-env-shared-"));
	cleanups.push(() => rm(dir, { recursive: true, force: true }));
	const agents = ["a", "b"].map((id) => new AgentExtensions(join(dir, "home"), dir, join(dir, id), dir, {}));
	await Promise.all(
		Array.from({ length: 20 }, (_, i) =>
			agents[i % 2]!.editEnv({ agentId: "unused", scope: "dex", key: `KEY_${i}`, value: String(i) }),
		),
	);
	const loaded = await agents[0]!.load();
	for (let i = 0; i < 20; i++) expect(loaded.env[`KEY_${i}`]).toBe(String(i));
	await loaded.close();
});
