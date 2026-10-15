// The Go tests check the API. This test checks the worker log, which the Go tests do not capture.
import { afterEach, expect, spyOn, test } from "bun:test";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { AgentExtensions } from "../src/extensions.ts";

const cleanups: (() => Promise<void>)[] = [];
afterEach(async () => {
	await Promise.all(cleanups.splice(0).map((cleanup) => cleanup()));
});

test("the log of a failed MCP server has no values from its entry", async () => {
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
				crash: {
					command: "/bin/sh",
					args: ["-c", 'echo "$TOKEN $0" >&2; exit 1', "SECRET-ARG"],
					env: { TOKEN: "SECRET-ENV" },
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

	expect(loaded.info.servers.map((s) => s.state)).toEqual(["failed", "failed", "failed"]);
	const text = log.mock.calls.flat().join("\n");
	// The log still has the details that are not secret.
	expect(text).toContain("status 500");
	expect(text).toContain("[redacted]");
	expect(text).not.toContain("SECRET");
});
