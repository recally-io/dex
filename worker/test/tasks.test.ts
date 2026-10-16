import { afterEach, expect, test } from "bun:test";
import { mkdtemp, rm, readFile, mkdir, writeFile } from "node:fs/promises";
import { watch } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Agents } from "../src/agents.ts";
import { Catalog } from "../src/models.ts";
import { TaskRpc } from "../src/tasks.ts";
import type { SettledEvent, TaskRequest } from "../src/protocol.ts";

type Wire = {
	id?: number;
	method?: string;
	event?: string;
	params?: Record<string, unknown>;
	result?: unknown;
	error?: { message: string; code?: string };
};
const cleanups: (() => Promise<void>)[] = [];
afterEach(async () => {
	for (const cleanup of cleanups.splice(0).toReversed()) {
		// oxlint-disable-next-line no-await-in-loop
		await cleanup();
	}
});

async function worker(
	options: {
		dir?: string;
		recover?: { stageId: string; agentId: string; conversationId: unknown }[];
	} = {},
) {
	const dir = options.dir ?? (await mkdtemp(join(tmpdir(), "dex-tasks-")));
	if (options.dir === undefined) cleanups.push(() => rm(dir, { recursive: true, force: true }));
	let crashed = false;
	const child = Bun.spawn([process.execPath, "run", join(import.meta.dir, "../src/main.ts")], {
		env: { PATH: process.env["PATH"], HOME: dir, DEX_DATA_DIR: dir, DEX_FAKE_MODEL: "1" },
		stdin: "pipe",
		stdout: "pipe",
		stderr: "pipe",
	});
	const messages: Wire[] = [];
	const listeners = new Set<() => void>();
	let next = 0;
	const send = (message: Wire) => {
		void child.stdin.write(`${JSON.stringify(message)}\n`);
	};
	const errors = new Response(child.stderr).text();
	const reading = (async () => {
		let buffer = "";
		const decoder = new TextDecoder();
		for await (const bytes of child.stdout) {
			buffer += decoder.decode(bytes, { stream: true });
			let end: number;
			while ((end = buffer.indexOf("\n")) !== -1) {
				const line = buffer.slice(0, end);
				buffer = buffer.slice(end + 1);
				const message: Wire = JSON.parse(line);
				messages.push(message);
				for (const listener of listeners) listener();
			}
		}
	})();
	const wait = (predicate: (message: Wire) => boolean) =>
		new Promise<Wire>((resolve, reject) => {
			const check = () => {
				const found = messages.find(predicate);
				if (found !== undefined) {
					clearTimeout(timer);
					listeners.delete(check);
					resolve(found);
				}
			};
			const timer = setTimeout(() => {
				listeners.delete(check);
				reject(new Error(`worker timeout: ${JSON.stringify(messages)}`));
			}, 15000);
			listeners.add(check);
			check();
		});
	const call = async (method: string, params: Record<string, unknown> = {}) => {
		const id = ++next;
		send({ id, method, params });
		return wait((m) => m.id === id && m.method === undefined);
	};
	let closed = false;
	const shutdown = async () => {
		if (!closed) {
			closed = true;
			void child.stdin.end();
		}
		await child.exited;
		await reading;
	};
	cleanups.push(async () => {
		await shutdown();
		await child.exited;
		await reading;
		const stderr = await errors;
		if (!crashed) expect(child.exitCode, stderr).toBe(0);
	});
	if (options.recover !== undefined)
		expect((await call("recoverTasks", { stages: options.recover })).error).toBeUndefined();
	const create = await call("createConversation", {
		agentId: "bot",
		model: { provider: "faux", modelId: "spawn" },
		thinkingLevel: "off",
	});
	expect(create.error).toBeUndefined();
	const result = create.result;
	if (typeof result !== "object" || result === null || !("conversationId" in result))
		throw new Error("missing conversation ID");
	const conversationId = result.conversationId;
	let request = 0;
	const submit = (content: string, extra = {}) =>
		call("submit", {
			agentId: "bot",
			conversationId,
			model: { provider: "faux", modelId: "spawn" },
			thinkingLevel: "off",
			instructions: "",
			requestId: `request-${++request}`,
			content,
			attachments: [],
			source: "dex:chat/test",
			...extra,
		});
	return {
		shutdown,
		send,
		wait,
		call,
		submit,
		messages,
		conversationId,
		dir,
		crash: async () => {
			crashed = true;
			child.kill("SIGKILL");
			await child.exited;
			await reading;
		},
	};
}

test("user can spawn a task through the real worker without a browser watch", async () => {
	// Given a real worker with the fixed faux task provider.
	const w = await worker();
	// When a user delegates a goal, control receives a reverse RPC.
	await w.submit(
		JSON.stringify({
			title: "Check",
			goal: "Read one file",
			provider: "faux",
			modelId: "echo",
			thinkingLevel: "off",
		}),
	);
	const task = await w.wait((m) => m.method === "task");
	expect(task.params).toMatchObject({
		agentId: "bot",
		conversationId: w.conversationId,
		action: "spawn",
		sourceRequestId: "request-1",
		args: {
			title: "Check",
			goal: "Read one file",
			provider: "faux",
			modelId: "echo",
			thinkingLevel: "off",
		},
	});
	expect(task.params?.["key"]).toMatch(/^bot\/\d+$/);
	const busy = await w.submit("must stay pending", { whenBusy: "reject" });
	expect(busy.error?.code).toBe("conversation_busy");
	// Then the tool result reaches the answer, and progress needs no browser watch.
	w.send({ id: task.id, result: { id: "task-1", status: "active" } });
	const settled = await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === "request-1");
	expect(settled.params?.["answer"]).toBe('spawn: {"id":"task-1","status":"active"}');
	expect(settled.params).not.toHaveProperty("executionConfirmed");
	// Internal results remain in model context and do not need an explicit finish call.
	await w.submit(
		JSON.stringify({
			"task-result": { id: "task-1", title: "Check", status: "done", report: "Checked" },
		}),
		{ kind: "task_result" },
	);
	const result = await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === "request-3");
	expect(result.params?.["answer"]).toContain('"report":"Checked"');
	expect(w.messages.filter((m) => m.method === "task")).toHaveLength(1);
});

test("task control calls reject when stdin closes and retain control error causes", async () => {
	const sent: TaskRequest[] = [];
	const rpc = new TaskRpc((request) => sent.push(request));
	const params = {
		agentId: "bot",
		conversationId: 1,
		key: "bot/123",
		sourceRequestId: "request-1",
		action: "list",
		args: {},
	};
	const pending = rpc.call(params);
	const rejected = pending.catch((error: unknown) => error);
	rpc.close();
	expect(await rejected).toEqual(new Error("control input closed"));
	expect(sent).toEqual([{ id: 1, method: "task", params }]);
	expect(await rpc.call(params).catch((error: unknown) => error)).toEqual(new Error("control input closed"));
});

test("user results retry after a failed inbox input without merging later inputs", async () => {
	// Given A in progress, followed by invalid B and two valid queued inputs.
	const w = await worker();
	await w.submit(JSON.stringify({ title: "A", goal: "Wait for control" }));
	const first = await w.wait((m) => m.method === "task");
	await w.submit("invalid JSON");
	await w.submit(JSON.stringify({ title: "C", goal: "First queued goal" }));
	await w.submit(JSON.stringify({ title: "D", goal: "Second queued goal" }));
	w.send({ id: first.id, result: { id: "task-A" } });
	const failed = await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === "request-2");
	expect(failed.params?.["status"]).toBe("unanswered");
	// When control retries its result, the result stays outside the worker inbox.
	const content = JSON.stringify({ "task-result": { id: "task-A" } });
	const result = await w.submit(content, { whenBusy: "reject", requestId: "result-A" });
	expect(result.error?.code).toBe("conversation_busy");
	// Then C starts without a new ordinary input or an abort.
	const third = await w.wait((m) => m.method === "task" && m.params?.["sourceRequestId"] === "request-3");
	expect(third.params?.["args"]).toMatchObject({ title: "C" });
	expect(w.messages.some((m) => m.event === "settled" && m.params?.["requestId"] === "result-A")).toBe(false);
	// Replaying B does not add input. D retains its own source at the next boundary.
	expect(
		(await w.submit("invalid JSON", { whenBusy: "reject", requestId: "request-2" })).error,
	).toBeUndefined();
	w.send({ id: third.id, result: { id: "task-C" } });
	const fourth = await w.wait((m) => m.method === "task" && m.params?.["sourceRequestId"] === "request-4");
	expect(fourth.params?.["args"]).toMatchObject({ title: "D" });
	w.send({ id: fourth.id, result: { id: "task-D" } });
	await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === "request-4");
	expect((await w.submit(content, { whenBusy: "reject", requestId: "result-A" })).error).toBeUndefined();
	const settled = await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === "result-A");
	expect(settled.params?.["status"]).toBe("done");
	expect(settled.params?.["answer"]).toBe('task-result: {"id":"task-A"}');
}, 20000);

test("user input after an inbox failure keeps its source separate from the queued input", async () => {
	// Given a failed B with C still queued behind it.
	const w = await worker();
	await w.submit(JSON.stringify({ title: "A", goal: "Wait" }));
	const first = await w.wait((m) => m.method === "task");
	await w.submit("invalid JSON");
	await w.submit(JSON.stringify({ title: "C", goal: "Queued" }));
	w.send({ id: first.id, result: { id: "task-A" } });
	await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === "request-2");
	// When a new ordinary input arrives, C must run with only its original input.
	await w.submit(JSON.stringify({ title: "D", goal: "New input" }));
	const third = await w.wait((m) => m.method === "task" && m.params?.["sourceRequestId"] === "request-3");
	expect(third.params?.["args"]).toMatchObject({ title: "C" });
	w.send({ id: third.id, result: { id: "task-C" } });
	const fourth = await w.wait((m) => m.method === "task" && m.params?.["sourceRequestId"] === "request-4");
	expect(fourth.params?.["args"]).toMatchObject({ title: "D" });
	w.send({ id: fourth.id, result: { id: "task-D" } });
	const settled = await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === "request-4");
	expect(settled.params?.["status"]).toBe("done");
});

test("user results complete after a failure with no queued input", async () => {
	const w = await worker();
	await w.submit("invalid JSON");
	await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === "request-1");
	const result = await w.submit(JSON.stringify({ "task-result": { id: "task-A" } }), {
		whenBusy: "reject",
	});
	expect(result.error).toBeUndefined();
	const settled = await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === "request-2");
	expect(settled.params?.["answer"]).toBe('task-result: {"id":"task-A"}');
});

test("user can confirm the first execution in an empty conversation after restart", async () => {
	// Given a queued stage for which control created only an empty conversation.
	const first = await worker();
	await first.crash();
	const restarted = await worker({ dir: first.dir });
	// When the stage starts for the first time after restart, its shell belongs to this process.
	expect(
		(
			await restarted.submit("printf first > receipt.txt; cat receipt.txt", {
				conversationId: first.conversationId,
				model: { provider: "faux", modelId: "bash" },
				taskStageId: "queued-stage",
			})
		).error,
	).toBeUndefined();
	const settled = await restarted.wait(
		(m) => m.event === "settled" && m.params?.["conversationId"] === first.conversationId,
	);
	// Then control gets a trusted exit receipt as well as the answer.
	expect(settled.params?.["answer"]).toBe("bash: first");
	expect(settled.params?.["executionConfirmed"]).toBe(true);
	expect(await readFile(join(first.dir, "agents/bot/workspace/receipt.txt"), "utf8")).toBe("first");
});

test("user cannot confirm a new submission in a recovery-isolated empty conversation", async () => {
	const first = await worker();
	await first.crash();
	const recovered = await worker({
		dir: first.dir,
		recover: [{ stageId: "old-stage", agentId: "bot", conversationId: first.conversationId }],
	});
	await recovered.submit("ordinary answer", {
		conversationId: first.conversationId,
		model: { provider: "faux", modelId: "echo" },
		taskStageId: "old-stage",
	});
	const settled = await recovered.wait((m) => m.event === "settled");
	expect(settled.params?.["executionConfirmed"]).toBe(false);
});

test("user recovery never reruns a completed shell stage or confirms its old receipt", async () => {
	const first = await worker();
	const command = "echo started >> starts.txt; cat starts.txt";
	await first.submit(command, {
		model: { provider: "faux", modelId: "bash" },
		taskStageId: "old-shell",
	});
	await first.wait((m) => m.event === "settled");
	await first.crash();
	const recovered = await worker({
		dir: first.dir,
		recover: [{ stageId: "old-shell", agentId: "bot", conversationId: first.conversationId }],
	});
	await recovered.submit(command, {
		conversationId: first.conversationId,
		requestId: "request-1",
		model: { provider: "faux", modelId: "bash" },
		taskStageId: "old-shell",
	});
	const settled = await recovered.wait((m) => m.event === "settled");
	expect(settled.params?.["answer"]).toBe("bash: started");
	expect(settled.params?.["executionConfirmed"]).toBe(false);
	expect(await readFile(join(first.dir, "agents/bot/workspace/starts.txt"), "utf8")).toBe("started\n");
});

test("restart isolates marked task tools and queued inputs before ordinary conversations resume", async () => {
	const first = await worker();
	await first.submit(JSON.stringify({ title: "Interrupted", goal: "Must not replay" }));
	await first.wait((m) => m.method === "task");
	await first.submit("echo restarted > recovered-shell.txt", {
		model: { provider: "faux", modelId: "bash" },
	});
	await first.crash();
	const recovered = await worker({
		dir: first.dir,
		recover: [{ stageId: "unknown-stage", agentId: "bot", conversationId: first.conversationId }],
	});
	await recovered.submit("normal conversation", { model: { provider: "faux", modelId: "echo" } });
	const settled = await recovered.wait(
		(m) => m.event === "settled" && m.params?.["requestId"] === "request-1",
	);
	expect(settled.params?.["answer"]).toBe("echo: normal conversation");
	expect(recovered.messages.some((m) => m.method === "task")).toBe(false);
	// Recovery's interrupted tool record cannot prove that an old local CLI exited.
	const aborted = await recovered.call("abort", {
		agentId: "bot",
		conversationId: first.conversationId,
		taskStageId: "unknown-stage",
	});
	expect(aborted.result).toMatchObject({ executionConfirmed: false });
	await recovered.submit("replay", {
		conversationId: first.conversationId,
		requestId: "request-1",
		taskStageId: "unknown-stage",
	});
	const replayed = await recovered.wait(
		(m) => m.event === "settled" && m.params?.["conversationId"] === first.conversationId,
	);
	expect(replayed.params?.["executionConfirmed"]).toBe(false);
	// The queued shell was withdrawn before resume and remains withdrawn on replay.
	await recovered.submit("echo restarted > recovered-shell.txt", {
		conversationId: first.conversationId,
		requestId: "request-2",
		model: { provider: "faux", modelId: "bash" },
		taskStageId: "unknown-stage",
	});
	const queued = await recovered.wait(
		(m) =>
			m.event === "settled" &&
			m.params?.["conversationId"] === first.conversationId &&
			m.params?.["requestId"] === "request-2",
	);
	expect(queued.params?.["status"]).toBe("unanswered");
	expect(queued.params?.["executionConfirmed"]).toBe(false);
	expect(await Bun.file(join(first.dir, "agents/bot/workspace/recovered-shell.txt")).exists()).toBe(false);
	expect((await recovered.call("recoverTasks", { stages: [] })).error?.message).toContain(
		"precede agent open",
	);
});

/* oxlint-disable no-await-in-loop -- Each execution must settle before the next input. */
test.each(["task", "dream"])("%s conversations cannot call any task management tool", async (kind) => {
	const w = await worker();
	for (const [index, name, args] of [
		[1, "spawn_task", { title: "No", goal: "No" }],
		[2, "list_tasks", {}],
		[3, "get_task", { id: "another-task" }],
		[4, "cancel_task", { id: "another-task" }],
	] as const) {
		// Given a background execution using a real conversation and SQLite.
		const created = await w.call("createConversation", {
			agentId: "bot",
			model: { provider: "faux", modelId: "tool" },
			thinkingLevel: "off",
		});
		const result = created.result;
		if (typeof result !== "object" || result === null || !("conversationId" in result))
			throw new Error("missing conversation");
		await w.submit(`${name} ${JSON.stringify(args)}`, {
			conversationId: result.conversationId,
			...(kind === "task" ? { taskStageId: `restricted-${index}` } : { source: "dex:dream/test" }),
			model: { provider: "faux", modelId: "tool" },
		});
		const settled = await w.wait(
			(m) => m.event === "settled" && m.params?.["requestId"] === `request-${index}`,
		);
		expect(settled.params?.["answer"]).toContain("error");
		expect(w.messages.some((m) => m.method === "task")).toBe(false);
	}
});
/* oxlint-enable no-await-in-loop */

test("faux/tool calls each exposed task tool without tool_search", async () => {
	const w = await worker();
	for (const [name, action, args] of [
		["list_tasks", "list", {}],
		["get_task", "get", { id: "task-1" }],
		["cancel_task", "cancel", { id: "task-1" }],
	] as const) {
		// oxlint-disable-next-line no-await-in-loop
		await w.submit(`${name} ${JSON.stringify(args)}`, {
			model: { provider: "faux", modelId: "tool" },
		});
		// oxlint-disable-next-line no-await-in-loop
		const task = await w.wait((m) => m.method === "task" && m.params?.["action"] === action);
		expect(task.params?.["args"]).toEqual(args);
		w.send({ id: task.id, result: { action } });
		// oxlint-disable-next-line no-await-in-loop
		const settled = await w.wait(
			(m) => m.event === "settled" && m.params?.["answer"] === `tool: ${JSON.stringify({ action })}`,
		);
		expect(settled.params?.["status"]).toBe("done");
	}
});

test("abort joins a controlled shell process and silent waits do not refresh progress", async () => {
	const w = await worker();
	const workspace = join(w.dir, "agents/bot/workspace");
	const pidFile = join(workspace, "command.pid");
	const pidReady = new Promise<number>((resolve, reject) => {
		const inspect = async () => {
			try {
				const value = (await readFile(pidFile, "utf8")).trim();
				if (!/^\d+$/.test(value)) return;
				clearTimeout(timer);
				watcher.close();
				resolve(Number(value));
			} catch (error) {
				if (!(error instanceof Error) || !("code" in error) || error.code !== "ENOENT") reject(error);
			}
		};
		const watcher = watch(workspace, () => {
			void inspect();
		});
		const timer = setTimeout(() => {
			watcher.close();
			reject(new Error("shell did not record its PID"));
		}, 10000);
		cleanups.push(async () => {
			clearTimeout(timer);
			watcher.close();
		});
	});
	await w.submit("echo $$ > command.pid; exec sleep 120", {
		model: { provider: "faux", modelId: "bash" },
		taskStageId: "shell-stage",
	});
	const pid = await pidReady;
	await w.wait((m) => m.event === "taskProgress" && m.params?.["currentTool"] === "bash");
	const progress = w.messages.filter((m) => m.event === "taskProgress");
	await w.call("ping");
	expect(w.messages.filter((m) => m.event === "taskProgress")).toEqual(progress);
	const aborted = await w.call("abort", {
		agentId: "bot",
		conversationId: w.conversationId,
		taskStageId: "shell-stage",
	});
	expect(aborted.error).toBeUndefined();
	expect(aborted.result).toEqual({ executionConfirmed: true });
	let code;
	try {
		process.kill(pid, 0);
	} catch (error) {
		if (error instanceof Error && "code" in error) code = error.code;
	}
	expect(code).toBe("ESRCH");
	const repeated = await w.call("abort", {
		agentId: "bot",
		conversationId: w.conversationId,
		taskStageId: "shell-stage",
	});
	expect(repeated.result).toEqual({ executionConfirmed: true });
	const unknown = await w.call("abort", {
		agentId: "bot",
		conversationId: w.conversationId,
		taskStageId: "other-stage",
	});
	expect(unknown.result).toMatchObject({ executionConfirmed: false });
	expect((await w.call("abort", { agentId: "bot", conversationId: w.conversationId })).result).toEqual({
		executionConfirmed: true,
	});
	const settled = await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === "request-1");
	expect(settled.params?.["status"]).toBe("unanswered");
	expect(settled.params?.["executionConfirmed"]).toBe(true);
});

test("a real submission wait failure does not confirm task execution", async () => {
	// Given a real harness waiting for a control RPC.
	const dir = await mkdtemp(join(tmpdir(), "dex-task-wait-"));
	cleanups.push(() => rm(dir, { recursive: true, force: true }));
	const called = Promise.withResolvers<void>();
	const event = Promise.withResolvers<SettledEvent>();
	const rpc = new TaskRpc(() => called.resolve());
	const agents = new Agents(
		dir,
		new Catalog({ DEX_FAKE_MODEL: "1" }).models,
		(settled) => event.resolve(settled),
		() => {},
		(params, signal) => rpc.call(params, signal),
	);
	cleanups.push(async () => {
		rpc.close();
		await agents.close();
	});
	const { conversationId } = await agents.createConversation({
		agentId: "bot",
		model: { provider: "faux", modelId: "spawn" },
		thinkingLevel: "off",
	});
	await agents.submit({
		agentId: "bot",
		conversationId,
		model: { provider: "faux", modelId: "spawn" },
		thinkingLevel: "off",
		instructions: "",
		requestId: "wait-failure",
		source: "dex:chat/test",
		content: JSON.stringify({ title: "Wait", goal: "Wait for control" }),
		attachments: [],
	});
	await called.promise;
	await writeFile(join(dir, "memory/stop-report.md"), "shutdown evidence\n");
	// When shutdown closes the session, submission.wait rejects instead of returning a settlement.
	await agents.close();
	// Then control receives an explicit lack of evidence, even if shutdown cancels the tool.
	const settled = await event.promise;
	expect(settled.status).toBe("unanswered");
	expect(settled.reason).toContain("closed");
	expect(settled.executionConfirmed).toBeUndefined();
	// Close must also join the memory commit after the final settlement.
	expect(await agents.quiesce({ expiresAt: Date.now() + 1000 })).toEqual({ busy: false });
	const commit = Bun.spawnSync(["git", "log", "-1", "--format=%s"], { cwd: join(dir, "memory") });
	expect(commit.exitCode).toBe(0);
	expect(new TextDecoder().decode(commit.stdout).trim()).toBe("Update memory from dex:chat/test");
});

test("a bot with MCP tools confirms local stages independently of other conversations", async () => {
	// Given a real MCP server with both successful and failed tool results.
	const dir = await mkdtemp(join(tmpdir(), "dex-task-mcp-local-"));
	cleanups.push(() => rm(dir, { recursive: true, force: true }));
	const server = Bun.serve({
		port: 0,
		fetch: async (request) => {
			if (request.method !== "POST") return new Response(null, { status: 405 });
			const message = await request.json();
			if (message.id === undefined) return new Response(null, { status: 202 });
			return Response.json({
				jsonrpc: "2.0",
				id: message.id,
				result:
					message.method === "initialize"
						? {
								protocolVersion: message.params.protocolVersion,
								capabilities: { tools: {} },
								serverInfo: { name: "report", version: "1" },
							}
						: message.method === "tools/list"
							? {
									tools: [
										{
											name: "report",
											description: "Return a report",
											inputSchema: { type: "object", properties: { fail: { type: "boolean" } } },
										},
									],
								}
							: {
									content: [{ type: "text", text: "remote report" }],
									isError: message.params.arguments.fail === true,
								},
			});
		},
	});
	cleanups.push(() => server.stop(true));
	await mkdir(join(dir, ".agents"));
	await writeFile(
		join(dir, ".agents/mcp.json"),
		JSON.stringify({ mcpServers: { remote: { type: "http", url: server.url.href } } }),
	);
	const w = await worker({ dir });
	// When the main conversation and other stages call MCP, local stages keep their own evidence.
	await w.submit("mcp__remote__report {}", { model: { provider: "faux", modelId: "tool" } });
	await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === "request-1");
	for (const [index, model, content, confirmed] of [
		[2, "tool", "mcp__remote__report {}", true],
		[3, "tool", 'mcp__remote__report {"fail":true}', true],
		[4, "echo", "local report", true],
		[5, "bash", "printf DEX_MCP_LOCAL_OK", true],
	] as const) {
		// oxlint-disable-next-line no-await-in-loop
		const created = await w.call("createConversation", {
			agentId: "bot",
			model: { provider: "faux", modelId: model },
			thinkingLevel: "off",
		});
		const result = created.result;
		if (typeof result !== "object" || result === null || !("conversationId" in result))
			throw new Error("missing stage conversation");
		// oxlint-disable-next-line no-await-in-loop
		const submitted = await w.submit(content, {
			conversationId: result.conversationId,
			model: { provider: "faux", modelId: model },
			taskStageId: `mcp-boundary-${index}`,
		});
		expect(submitted).not.toHaveProperty("error");
		// MCP and ordinary tools use the same controlled local execution boundary.
		// oxlint-disable-next-line no-await-in-loop
		const settled = await w.wait(
			(m) => m.event === "settled" && m.params?.["requestId"] === `request-${index}`,
		);
		expect(settled.params?.["status"]).toBe("done");
		expect(settled.params?.["executionConfirmed"]).toBe(confirmed);
		if (model === "bash") expect(settled.params?.["answer"]).toContain("DEX_MCP_LOCAL_OK");
		if (model === "tool") expect(settled.params?.["answer"]).toContain("remote report");
	}
});

test("MCP cancellation confirms local cleanup and reports the remote boundary", async () => {
	// Given a real MCP HTTP server whose tool has no execution exit receipt.
	const dir = await mkdtemp(join(tmpdir(), "dex-task-mcp-"));
	cleanups.push(() => rm(dir, { recursive: true, force: true }));
	const called = Promise.withResolvers<void>();
	const result = Promise.withResolvers<Response>();
	const server = Bun.serve({
		port: 0,
		fetch: async (request) => {
			if (request.method !== "POST") return new Response(null, { status: 405 });
			const message = await request.json();
			if (message.id === undefined) return new Response(null, { status: 202 });
			if (message.method === "tools/call") {
				called.resolve();
				return result.promise;
			}
			return Response.json({
				jsonrpc: "2.0",
				id: message.id,
				result:
					message.method === "initialize"
						? {
								protocolVersion: message.params.protocolVersion,
								capabilities: { tools: {} },
								serverInfo: { name: "wait", version: "1" },
							}
						: {
								tools: [
									{
										name: "wait",
										description: "Wait for release",
										inputSchema: { type: "object", properties: {} },
									},
								],
							},
			});
		},
	});
	cleanups.push(async () => {
		result.resolve(new Response(null, { status: 503 }));
		await server.stop(true);
	});
	await mkdir(join(dir, ".agents"));
	await writeFile(
		join(dir, ".agents/mcp.json"),
		JSON.stringify({ mcpServers: { remote: { type: "http", url: server.url.href } } }),
	);
	const w = await worker({ dir });
	await w.submit("mcp__remote__wait {}", {
		model: { provider: "faux", modelId: "tool" },
		taskStageId: "mcp-stage",
	});
	await called.promise;
	// When the worker cancels its call, the server has still not completed the tool.
	const aborted = await w.call("abort", {
		agentId: "bot",
		conversationId: w.conversationId,
		taskStageId: "mcp-stage",
	});
	expect(aborted.error).toBeUndefined();
	expect(aborted.result).toMatchObject({
		executionConfirmed: true,
		stopNote: expect.stringContaining("remote work may still be running"),
	});
	const settled = await w.wait((m) => m.event === "settled");
	expect(settled.params?.["executionConfirmed"]).toBe(true);
	expect(settled.params?.["stopNote"]).toContain("remote work may still be running");
});

/* oxlint-disable no-await-in-loop -- Poll real process and file state with a deadline. */
for (const mode of ["cancel", "main-cancel", "shutdown", "timeout"])
	test(`foreground job-control child cannot write after ${mode}`, async () => {
		const w = await worker();
		const workspace = join(w.dir, "agents/bot/workspace");
		let foregroundPID: number | undefined;
		cleanups.push(async () => {
			if (foregroundPID === undefined) return;
			try {
				process.kill(-foregroundPID, "SIGKILL");
			} catch {}
		});
		const command =
			"set -m; echo $$ > shell.pid; bash -c 'echo $$ > foreground.pid; while [ ! -f release ]; do sleep 0.02; done; echo AFTER_CANCEL > after-cancel.txt'; echo after";
		await w.submit(mode === "timeout" ? `bash ${JSON.stringify({ command, timeout: 1 })}` : command, {
			model: { provider: "faux", modelId: mode === "timeout" ? "tool" : "bash" },
			...(mode === "main-cancel" ? {} : { taskStageId: "foreground-stage" }),
		});
		const deadline = Date.now() + 5000;
		while (foregroundPID === undefined) {
			try {
				foregroundPID =
					Number((await readFile(join(workspace, "foreground.pid"), "utf8")).trim()) || undefined;
			} catch {}
			if (Date.now() >= deadline) throw new Error("foreground PID was not recorded");
			if (foregroundPID === undefined) await new Promise((resolve) => setTimeout(resolve, 10));
		}
		const shellPID = Number((await readFile(join(workspace, "shell.pid"), "utf8")).trim());
		const describe = Bun.spawnSync([
			"/bin/ps",
			"-o",
			"pid,ppid,pgid,sess,stat,command",
			"-p",
			`${shellPID},${foregroundPID}`,
		]);
		console.log(new TextDecoder().decode(describe.stdout));
		const aborted =
			mode === "shutdown"
				? (await w.shutdown(), undefined)
				: mode === "timeout"
					? undefined
					: await w.call("abort", {
							agentId: "bot",
							conversationId: w.conversationId,
							...(mode === "main-cancel" ? {} : { taskStageId: "foreground-stage" }),
						});
		if (mode === "timeout") {
			const settled = await w.wait((m) => m.event === "settled");
			expect(settled.params?.["answer"]).toContain("timed out after 1 seconds");
			expect(settled.params?.["executionConfirmed"]).toBe(true);
		}
		let alive = false;
		try {
			process.kill(foregroundPID, 0);
			alive = true;
		} catch {}
		console.log(JSON.stringify({ aborted, foregroundPID, foregroundStillAlive: alive }));

		await writeFile(join(workspace, "release"), "released after executionConfirmed\n");
		const outputPath = join(workspace, "after-cancel.txt");
		const outputDeadline = Date.now() + 2000;
		while (alive && !(await Bun.file(outputPath).exists()) && Date.now() < outputDeadline) {
			try {
				process.kill(foregroundPID, 0);
			} catch {
				alive = false;
			}
			await new Promise((resolve) => setTimeout(resolve, 10));
		}
		const lateWrite = await Bun.file(outputPath).exists();
		if (mode === "cancel" || mode === "main-cancel")
			expect(aborted?.result).toEqual({ executionConfirmed: true });
		expect(lateWrite).toBe(false);
		expect(alive).toBe(false);
	});
/* oxlint-enable no-await-in-loop */

test("trusted internal results stay out of user projection and retain their real source", async () => {
	const w = await worker();
	const content = JSON.stringify({
		"task-result": { id: "task-A", title: "Implement", status: "done", report: "Implemented" },
	});
	// An identical user JSON remains a user message; only trusted kind controls projection.
	await w.submit(content, { kind: "message" });
	await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === "request-1");
	await w.submit(content, { kind: "task_result" });
	const settled = await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === "request-2");
	expect(settled.params?.["answer"]).toContain('"report":"Implemented"');
	await w.submit(
		JSON.stringify({
			"task-result": { id: "task-A" },
			nextTask: { title: "Review", goal: "Review artifacts" },
		}),
		{ kind: "task_result" },
	);
	const task = await w.wait((m) => m.method === "task");
	expect(task.params?.["sourceRequestId"]).toBe("request-3");
	expect(task.params?.["args"]).toEqual({ title: "Review", goal: "Review artifacts" });
	w.send({ id: task.id, result: { id: "task-review" } });
	await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === "request-3");
	await w.call("watch", {
		agentId: "bot",
		conversationId: w.conversationId,
		watchId: "projection",
	});
	const snapshot = await w.wait((m) => m.event === "chat" && m.params?.["watchId"] === "projection");
	// oxlint-disable-next-line typescript/no-unsafe-type-assertion
	const events = snapshot.params?.["events"] as {
		type: string;
		items: { kind: string; id: string; text?: string }[];
	}[];
	const users = events.flatMap((event) => event.items ?? []).filter((item) => item.kind === "user");
	expect(users).toHaveLength(1);
	expect(users[0]).toMatchObject({ id: "request-1", text: content });
	const invalid = await w.submit("invalid kind", { kind: "untrusted" });
	expect(invalid.error).toBeDefined();
});

test("task RPC response deadline rejects without a control response and permits later calls", async () => {
	const sent: TaskRequest[] = [];
	const rpc = new TaskRpc((request) => sent.push(request), 20);
	cleanups.push(async () => rpc.close());
	const params = {
		agentId: "bot",
		conversationId: 1,
		key: "bot/1",
		sourceRequestId: "source",
		action: "list",
		args: {},
	};
	const started = Date.now();
	const timeoutError = await rpc.call(params).catch((error: unknown) => error);
	expect(timeoutError).toMatchObject({ message: "task control response timed out" });
	expect(Date.now() - started).toBeLessThan(1000);
	expect(rpc.respond({ id: 1, result: "late" })).toBe(true);
	const next = rpc.call(params);
	rpc.respond({ id: 2, result: "current" });
	expect(await next).toBe("current");
	const controller = new AbortController();
	const aborted = rpc.call(params, controller.signal);
	const rejection = aborted.catch((error: unknown) => error);
	controller.abort();
	expect(await rejection).toMatchObject({ message: "task control call aborted" });
	const failed = rpc.call(params);
	const failure = failed.catch((error: unknown) => error);
	rpc.respond({ id: 4, error: { message: "limited", code: "task_limit" } });
	expect(await failure).toMatchObject({ message: "limited", cause: { code: "task_limit" } });
	expect(sent).toHaveLength(4);
});

test("main conversation abort joins its invocation and withdraws queued input", async () => {
	const w = await worker();
	await w.submit(JSON.stringify({ title: "Wait", goal: "Wait for control" }));
	await w.wait((m) => m.method === "task");
	await w.submit(JSON.stringify({ title: "Queued", goal: "Must not run" }));
	const aborted = await w.call("abort", { agentId: "bot", conversationId: w.conversationId });
	expect(aborted.result).toEqual({ executionConfirmed: true });
	for (const id of ["request-1", "request-2"]) {
		// oxlint-disable-next-line no-await-in-loop
		const settled = await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === id);
		expect(settled.params?.["status"]).toBe("unanswered");
	}
	expect(w.messages.filter((m) => m.method === "task")).toHaveLength(1);
	const repeated = await w.call("abort", { agentId: "bot", conversationId: w.conversationId });
	expect(repeated.result).toEqual({ executionConfirmed: true });
});
