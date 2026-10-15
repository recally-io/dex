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
					headers: { Authorization: "Bearer SECRET-HEADER" },
				},
				badurl: { type: "http", url: "SECRET-URL" },
			},
		}),
	);
	const log = spyOn(console, "error").mockImplementation(() => undefined);
	cleanups.push(async () => log.mockRestore());

	const loaded = await new AgentExtensions(join(dir, "home"), join(dir, "agent"), dir).load();
	await loaded.close();

	expect(loaded.servers.map((s) => s.state)).toEqual(["failed", "failed", "failed", "failed"]);
	expect(JSON.stringify(loaded.servers)).not.toContain("SECRET");
	const text = log.mock.calls.flat().join("\n");
	expect(text).toContain("MCP server denied: the server answered with HTTP status 500");
	expect(text).toContain("MCP server missing: the command does not exist");
	expect(text).not.toContain("SECRET");
});
