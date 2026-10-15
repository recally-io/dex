import { readdir, readFile, stat } from "node:fs/promises";
import { join, resolve } from "node:path";
import { defineExtension, defineTool, type Extension, section } from "@earendil-works/pi-durable";
import {
	McpAuthRequiredError,
	McpClient,
	McpError,
	McpHttpError,
	McpTimeoutError,
	StdioTransport,
	StreamableHttpTransport,
	toLlmContent,
} from "@earendil-works/pi-mcp";
import Type, { type Static } from "typebox";
import { Value } from "typebox/value";
import type { Extensions, McpServerInfo, Scope, SkillInfo } from "./protocol.ts";

/** The name of the pi-durable extension with the skills section and the MCP tools. */
export const EXTENSION_NAME = "extensions";

// A server name is part of each tool name, and providers allow 64 characters of [A-Za-z0-9_-].
const SERVER_NAME = /^[A-Za-z0-9_-]{1,32}$/;
const TOOL_NAME = /^[A-Za-z0-9_-]{1,64}$/;
const CONNECT_TIMEOUT_MS = 30_000;
// A call that sends progress renews this limit. Raise it if a server has longer silent calls.
const CALL_TIMEOUT_MS = 10 * 60_000;

const Common = { disabled: Type.Optional(Type.Boolean()) };
const StdioServer = Type.Object({
	type: Type.Optional(Type.Literal("stdio")),
	command: Type.String({ minLength: 1 }),
	args: Type.Optional(Type.Array(Type.String())),
	env: Type.Optional(Type.Record(Type.String(), Type.String())),
	cwd: Type.Optional(Type.String()),
	...Common,
});
const HttpServer = Type.Object({
	type: Type.Literal("http"),
	url: Type.String({ minLength: 1 }),
	headers: Type.Optional(Type.Record(Type.String(), Type.String())),
	...Common,
});
// An agent entry that only turns off a global server with the same name.
const Off = Type.Object({ disabled: Type.Literal(true) });
const Server = Type.Union([HttpServer, StdioServer, Off]);
type Server = Static<typeof Server>;
const SkillMeta = Type.Object({
	name: Type.Optional(Type.String()),
	description: Type.String({ minLength: 1, maxLength: 1024, pattern: "\\S" }),
});
// The envelope of Claude Code and Cursor. dex reads the subset above and ignores other fields.
const McpFile = Type.Object({ mcpServers: Type.Record(Type.String(), Server) });

type Paths = { readonly skills: string; readonly mcp: string };

type Declared = { readonly name: string; readonly scope: Scope; readonly server: Server };
type ServerEntry = Declared & { readonly overrides: boolean };

/** What the files declare, read without starting a server. */
type Scan = {
	readonly skills: SkillInfo[];
	readonly servers: readonly ServerEntry[];
	readonly errors: string[];
};

/** The extension to install, and the MCP servers that it holds open. */
export type Loaded = {
	readonly extension: Extension;
	readonly hasTools: boolean;
	/** The state of each server. A server that closes its connection changes to "failed". */
	readonly servers: readonly McpServerInfo[];
	close(): Promise<void>;
};

/**
 * The skills and MCP servers of one agent, from two scopes: global (`~/.agents/`) and the agent
 * directory. An agent entry replaces a global entry with the same name as a whole, so the two never
 * merge credentials. The files are the configuration; dex keeps no copy of them.
 */
export class AgentExtensions {
	readonly #scopes: Readonly<Record<Scope, Paths>>;
	readonly #workspace: string;

	constructor(home: string, agentDir: string, workspace: string) {
		this.#scopes = {
			global: { skills: join(home, ".agents", "skills"), mcp: join(home, ".agents", "mcp.json") },
			agent: { skills: join(agentDir, "skills"), mcp: join(agentDir, "mcp.json") },
		};
		this.#workspace = workspace;
	}

	/**
	 * The catalog, read from the files now. `servers` is the state of the loaded servers; without it,
	 * the catalog shows the servers that the files declare, and no server starts.
	 */
	async info(servers?: readonly McpServerInfo[]): Promise<Extensions> {
		const scan = await this.#scan();
		return {
			paths: {
				globalSkills: this.#scopes.global.skills,
				globalMcp: this.#scopes.global.mcp,
				agentSkills: this.#scopes.agent.skills,
				agentMcp: this.#scopes.agent.mcp,
			},
			skills: scan.skills,
			servers:
				servers ??
				scan.servers.map((entry) => serverInfo(entry, entry.server.disabled ? "disabled" : "idle")),
			errors: scan.errors,
		};
	}

	/**
	 * Connects the MCP servers. A server that fails is reported and has no tools; the others still
	 * work. When an mcp.json file is not valid, no server starts, because its entries could replace or
	 * turn off global ones. The skills section reads the skill files before each model request, so an
	 * edit to a skill applies without a restart.
	 */
	async load(): Promise<Loaded> {
		const scan = await this.#scan();
		for (const error of scan.errors) console.error("extensions:", error);
		const servers = await Promise.all(
			scan.servers.map((entry) =>
				entry.server.disabled ? Promise.resolve(undefined) : this.#connect(entry),
			),
		);
		const clients = servers.flatMap((s) => (s?.client === undefined ? [] : [s.client]));
		// Server "a" with tool "b__c" and server "a__b" with tool "c" have the same tool name, and a
		// server can list one tool twice. The first server in name order keeps the name.
		const names = new Set<string>();
		const tools = servers.flatMap((s) => {
			if (s === undefined) return [];
			const lost: string[] = [];
			const kept = s.tools.filter((tool) => {
				const fresh = !names.has(tool.name);
				if (fresh) names.add(tool.name);
				else lost.push(tool.name);
				return fresh;
			});
			if (lost.length > 0)
				s.info.error = [s.info.error, `tools with a name that another tool uses: ${lost.join(", ")}`]
					.filter((text) => text !== undefined)
					.join("; ");
			return kept;
		});
		return {
			extension: defineExtension({
				name: EXTENSION_NAME,
				sections: [section("skills", async () => skillsText(await this.#skills([])))],
				tools,
			}),
			hasTools: tools.length > 0,
			servers: scan.servers.map((entry, i) => servers[i]?.info ?? serverInfo(entry, "disabled")),
			close: async () => {
				await Promise.all(clients.map((client) => client.close().catch(() => undefined)));
			},
		};
	}

	async #skills(errors: string[]): Promise<SkillInfo[]> {
		const [global, agent] = await Promise.all([
			readSkills(this.#scopes.global.skills, "global", errors),
			readSkills(this.#scopes.agent.skills, "agent", errors),
		]);
		return merge(global, agent, (skill, overrides) => ({ ...skill, overrides }));
	}

	async #scan(): Promise<Scan> {
		const errors: string[] = [];
		const skills = await this.#skills(errors);
		const [globalMcp, agentMcp] = await Promise.all([
			readMcp(this.#scopes.global.mcp, errors),
			readMcp(this.#scopes.agent.mcp, errors),
		]);
		const valid = globalMcp !== undefined && agentMcp !== undefined;
		return {
			skills,
			servers: valid
				? merge<Declared, ServerEntry>(
						Object.entries(globalMcp).map(([name, server]) => ({ name, scope: "global", server })),
						Object.entries(agentMcp).map(([name, server]) => ({ name, scope: "agent", server })),
						(entry, overrides) => ({ ...entry, overrides }),
					)
				: [],
			errors,
		};
	}

	async #connect(entry: ServerEntry) {
		const { name, server } = entry;
		const client = new McpClient({ name: "dex", version: "1", requestTimeoutMs: CONNECT_TIMEOUT_MS });
		try {
			if (!("url" in server) && !("command" in server))
				throw new Error("a server without a command or URL is off");
			const transport =
				server.type === "http"
					? new StreamableHttpTransport({ url: server.url, headers: server.headers ?? {} })
					: new StdioTransport({
							command: server.command,
							args: server.args ?? [],
							// The worker environment has no credentials; a server gets its own in `env`.
							env: server.env ?? {},
							cwd: resolve(this.#workspace, server.cwd ?? "."),
							// "inherit" would put the server's output, which can hold its credentials, in the log.
							stderr: "pipe",
						});
			await client.connect(transport);
			const listed = await client.listTools();
			const skipped: string[] = [];
			const tools = listed.flatMap((tool) => {
				const toolName = `mcp__${name}__${tool.name}`;
				if (!TOOL_NAME.test(toolName)) {
					skipped.push(tool.name);
					return [];
				}
				return [
					defineTool({
						name: toolName,
						description: tool.description ?? tool.title ?? tool.name,
						// Providers require an object schema, and some reject one without properties.
						parameters: Type.Unsafe<Record<string, unknown>>({
							...tool.inputSchema,
							type: "object",
							properties: tool.inputSchema["properties"] ?? {},
						}),
						// Large tool sets stay out of the prompt until tool_search loads them.
						deferred: true,
						// The default replay policy, "unsafe", is required: a call can change data on the
						// server, so a call that a restart interrupts must not run again.
						execute: async (args, _api, context) => {
							const result = await client.callTool(tool.name, args, {
								timeoutMs: CALL_TIMEOUT_MS,
								...(context.abortSignal === undefined ? {} : { signal: context.abortSignal }),
							});
							return { output: toLlmContent(result), isError: result.isError === true };
						},
					}),
				];
			});
			const info = serverInfo(entry, "connected", listed.map((tool) => tool.name).toSorted());
			if (skipped.length > 0)
				info.error = `tools with names that are too long or not [A-Za-z0-9_-]: ${skipped.join(", ")}`;
			client.onClose(() => {
				info.state = "failed";
				info.error = "the server closed the connection; reload to connect again";
			});
			return { client, tools, info };
		} catch (error) {
			await client.close().catch(() => undefined);
			// A raw error or the server's stderr can hold the command, URL, arguments, headers or
			// environment, so neither the API nor the log gets them. To see them, run the server by hand.
			const info = serverInfo(entry, "failed");
			info.error = failure(error);
			console.error(`MCP server ${name}: ${info.error}`);
			return { client: undefined, tools: [], info };
		}
	}
}

function serverInfo(entry: ServerEntry, state: McpServerInfo["state"], tools: string[] = []): McpServerInfo {
	return {
		name: entry.name,
		scope: entry.scope,
		overrides: entry.overrides,
		...("url" in entry.server
			? { transport: "http" }
			: "command" in entry.server
				? { transport: "stdio" }
				: {}),
		state,
		tools,
	};
}

/** Global items, with the agent items that replace them by name. Sorted, so the prompt stays stable. */
function merge<T extends { readonly name: string }, R>(
	global: readonly T[],
	agent: readonly T[],
	result: (item: T, overrides: boolean) => R,
): R[] {
	const local = new Set(agent.map((item) => item.name));
	return [
		...global.filter((item) => !local.has(item.name)).map((item) => ({ item, overrides: false })),
		...agent.map((item) => ({ item, overrides: global.some((g) => g.name === item.name) })),
	]
		.toSorted((a, b) => (a.item.name < b.item.name ? -1 : 1))
		.map(({ item, overrides }) => result(item, overrides));
}

/**
 * Reads `<dir>/<name>/SKILL.md` in the Agent Skills format. The directory name is the skill name.
 * A skill that is not valid is reported with its error and still replaces a global skill, so a
 * broken agent skill never brings back the global one.
 */
async function readSkills(dir: string, scope: Scope, errors: string[]): Promise<SkillInfo[]> {
	let names: string[];
	try {
		names = await readdir(dir);
	} catch (error) {
		if (errorCode(error) !== "ENOENT") errors.push(`${dir}: ${errorText(error)}`);
		return [];
	}
	const skills = await Promise.all(
		names
			.filter((name) => !name.startsWith("."))
			.map(async (name): Promise<SkillInfo | undefined> => {
				const path = join(dir, name, "SKILL.md");
				// Follows symbolic links, because skill installers link skill directories.
				if (!(await stat(join(dir, name)).catch(() => undefined))?.isDirectory()) return undefined;
				const skill: SkillInfo = { name, description: "", scope, path, overrides: false };
				try {
					skill.description = parseSkill(await readFile(path, "utf8"), name);
				} catch (error) {
					skill.error = errorText(error);
				}
				return skill;
			}),
	);
	return skills.filter((skill) => skill !== undefined);
}

function parseSkill(text: string, dirName: string): string {
	const match = /^---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|$)/.exec(text);
	if (match === null) throw new Error("SKILL.md must start with YAML front matter between --- lines");
	let meta: unknown;
	try {
		meta = Bun.YAML.parse(match[1] ?? "");
	} catch {
		// The parser's message can quote the text.
		throw new Error("the front matter is not valid YAML");
	}
	// 1024 is the Agent Skills limit; the prompt lists every description.
	if (!Value.Check(SkillMeta, meta))
		throw new Error("the front matter needs a description of 1 to 1024 characters, and a name that is text");
	const { name, description } = meta;
	if (name !== undefined && name !== dirName) throw new Error(`the name "${name}" is not the directory name`);
	return description.trim().replaceAll(/\s+/g, " ");
}

/** The servers of an mcp.json file: none when it does not exist, undefined when it is not valid. */
async function readMcp(path: string, errors: string[]): Promise<Record<string, Server> | undefined> {
	let text: string;
	try {
		text = await readFile(path, "utf8");
	} catch (error) {
		if (errorCode(error) === "ENOENT") return {};
		errors.push(`${path}: ${errorText(error)}`);
		return undefined;
	}
	let data: unknown;
	try {
		data = JSON.parse(text);
	} catch {
		// The parser's message can quote the text, which can hold a credential.
		errors.push(`${path}: the file is not valid JSON`);
		return undefined;
	}
	if (!Value.Check(McpFile, data)) {
		const first = Value.Errors(McpFile, data)[0];
		errors.push(
			`${path}: ${first === undefined ? "not valid" : `${first.instancePath || "/"} ${first.message}`}`,
		);
		return undefined;
	}
	const bad = Object.keys(data.mcpServers).filter((name) => !SERVER_NAME.test(name));
	if (bad.length > 0) {
		errors.push(`${path}: server names must be 1 to 32 characters of [A-Za-z0-9_-]: ${bad.join(", ")}`);
		return undefined;
	}
	return data.mcpServers;
}

function skillsText(skills: readonly SkillInfo[]): string | undefined {
	const usable = skills.filter((skill) => skill.error === undefined);
	if (usable.length === 0) return undefined;
	return [
		"Skills are instructions for special tasks. Each line gives the name of a skill, when to use it, and the path of its SKILL.md file.",
		"Before you do a task that matches a skill, read its SKILL.md with the read tool and follow it. Relative paths in a skill start at the directory of its SKILL.md.",
		"",
		...usable.map((skill) => `- ${skill.name}: ${skill.description} (${skill.path})`),
	].join("\n");
}

/** A failure of a server connection, as a category without values from the configuration. */
function failure(error: unknown): string {
	if (error instanceof McpAuthRequiredError)
		return "the server requires authorization, and dex does not support OAuth";
	if (error instanceof McpHttpError) return `the server answered with HTTP status ${error.status}`;
	if (error instanceof McpTimeoutError) return `the server did not answer in ${CONNECT_TIMEOUT_MS / 1000} s`;
	if (error instanceof McpError) return "the server refused the connection";
	if (errorCode(error) === "ENOENT") return "the command does not exist";
	if (error instanceof TypeError && /url/i.test(error.message)) return "the URL is not valid";
	return "the connection failed";
}

function errorCode(error: unknown): unknown {
	return typeof error === "object" && error !== null && "code" in error ? error.code : undefined;
}

function errorText(error: unknown): string {
	return error instanceof Error ? error.message : String(error);
}
