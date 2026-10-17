import { afterEach, expect, test } from "bun:test";
import { mkdtemp, rm, readFile, mkdir, writeFile } from "node:fs/promises";
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
			source: "dex:bot/chat/test",
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
	// Then the tool result reaches the answer without a browser watch.
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

test("user recovery never reruns a completed shell stage", async () => {
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
	await recovered.submit("replay", {
		conversationId: first.conversationId,
		requestId: "request-1",
		taskStageId: "unknown-stage",
	});
	const replayed = await recovered.wait(
		(m) => m.event === "settled" && m.params?.["conversationId"] === first.conversationId,
	);
	expect(replayed.params?.["status"]).toBe("unanswered");
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
			...(kind === "task" ? { taskStageId: `restricted-${index}` } : { source: "dex:bot/dream/test" }),
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

test("abort cancels an ordinary shell process group", async () => {
	const w = await worker();
	const workspace = join(w.dir, "agents/bot/workspace");
	// Cleanup does not depend on abort succeeding or the command returning.
	cleanups.push(async () => {
		for (const name of ["command.pid", "shell.pid"]) {
			// oxlint-disable-next-line no-await-in-loop
			const pid = Number(await readFile(join(workspace, name), "utf8").catch(() => "0"));
			if (pid === 0) continue;
			try {
				process.kill(pid, "SIGKILL");
			} catch (error) {
				if (!(error instanceof Error) || !("code" in error) || error.code !== "ESRCH") throw error;
			}
		}
		await writeFile(join(workspace, "release"), "release test gate\n");
	});
	await w.submit(
		"echo $$ > shell.pid; bash -c 'echo $$ > command.pid; while [ ! -f release ]; do sleep 0.02; done; echo LATE > late'; echo done",
		{ model: { provider: "faux", modelId: "bash" }, taskStageId: "shell-stage" },
	);
	const started = Date.now();
	let pid = 0;
	while (pid === 0) {
		// oxlint-disable-next-line no-await-in-loop
		pid = Number(await readFile(join(workspace, "command.pid"), "utf8").catch(() => "0"));
		if (Date.now() - started > 5000) throw new Error("ordinary shell child did not start");
		// oxlint-disable-next-line no-await-in-loop
		if (pid === 0) await Bun.sleep(10);
	}
	const aborted = await w.call("abort", { agentId: "bot", conversationId: w.conversationId });
	expect(aborted.error).toBeUndefined();
	expect(aborted.result).toEqual({});
	const deadline = Date.now() + 3000;
	let code;
	while (code !== "ESRCH") {
		try {
			process.kill(pid, 0);
		} catch (error) {
			if (error instanceof Error && "code" in error) code = error.code;
		}
		if (Date.now() >= deadline) throw new Error("ordinary shell child did not exit");
		// oxlint-disable-next-line no-await-in-loop
		if (code !== "ESRCH") await Bun.sleep(10);
	}
	expect(await Bun.file(join(workspace, "late")).exists()).toBe(false);
	const settled = await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === "request-1");
	expect(settled.params?.["status"]).toBe("unanswered");
});

test("shutdown suppresses settled events and waits for the real memory commit", async () => {
	// Given a real harness waiting for a control RPC.
	const dir = await mkdtemp(join(tmpdir(), "dex-task-wait-"));
	cleanups.push(() => rm(dir, { recursive: true, force: true }));
	const called = Promise.withResolvers<void>();
	const events: SettledEvent[] = [];
	const previousHome = process.env["HOME"];
	process.env["HOME"] = dir;
	cleanups.push(async () => {
		if (previousHome === undefined) delete process.env["HOME"];
		else process.env["HOME"] = previousHome;
	});
	const rpc = new TaskRpc(() => called.resolve());
	const agents = new Agents(
		dir,
		new Catalog({ DEX_FAKE_MODEL: "1" }).models,
		(settled) => events.push(settled),
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
		source: "dex:bot/chat/test",
		content: JSON.stringify({ title: "Wait", goal: "Wait for control" }),
		attachments: [],
	});
	await called.promise;
	await writeFile(join(dir, "memory/stop-report.md"), "shutdown evidence\n");
	// When shutdown closes the session, submission.wait rejects instead of returning a settlement.
	await agents.close();
	// Then shutdown emits no failure, but still commits memory before close returns.
	expect(events).toEqual([]);
	expect(await agents.quiesce({ expiresAt: Date.now() + 1000 })).toEqual({ busy: false });
	const git = { cwd: join(dir, "memory"), env: { PATH: process.env["PATH"], HOME: dir } };
	const commit = Bun.spawnSync(["git", "log", "-1", "--format=%s"], git);
	expect(commit.exitCode).toBe(0);
	expect(new TextDecoder().decode(commit.stdout).trim()).toBe("Update memory from dex:bot/chat/test");
	const saved = Bun.spawnSync(["git", "show", "HEAD:stop-report.md"], git);
	expect(saved.exitCode).toBe(0);
	expect(new TextDecoder().decode(saved.stdout)).toBe("shutdown evidence\n");
});

test("task reports complete with MCP success, MCP error, and local tools", async () => {
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
	// When each conversation completes, its report keeps the tool result.
	await w.submit("mcp__remote__report {}", { model: { provider: "faux", modelId: "tool" } });
	await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === "request-1");
	for (const [index, model, content] of [
		[2, "tool", "mcp__remote__report {}"],
		[3, "tool", 'mcp__remote__report {"fail":true}'],
		[4, "echo", "local report"],
		[5, "bash", "printf DEX_MCP_LOCAL_OK"],
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
		// oxlint-disable-next-line no-await-in-loop
		const settled = await w.wait(
			(m) => m.event === "settled" && m.params?.["requestId"] === `request-${index}`,
		);
		expect(settled.params?.["status"]).toBe("done");
		if (model === "bash") expect(settled.params?.["answer"]).toContain("DEX_MCP_LOCAL_OK");
		if (model === "tool") expect(settled.params?.["answer"]).toContain("remote report");
	}
});

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
	expect(aborted.result).toEqual({});
	for (const id of ["request-1", "request-2"]) {
		// oxlint-disable-next-line no-await-in-loop
		const settled = await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === id);
		expect(settled.params?.["status"]).toBe("unanswered");
	}
	expect(w.messages.filter((m) => m.method === "task")).toHaveLength(1);
	const repeated = await w.call("abort", { agentId: "bot", conversationId: w.conversationId });
	expect(repeated.result).toEqual({});
});

test("user reset replay stays idempotent while new input runs and busy reset changes nothing", async () => {
	// Given a committed reset and a new run held at the control boundary.
	const w = await worker();
	const params = { agentId: "bot", conversationId: w.conversationId, requestId: "clear-once" };
	const cleared = await w.call("reset", params);
	expect(cleared.error).toBeUndefined();
	await w.submit(JSON.stringify({ title: "Running", goal: "Wait for control" }));
	const running = await w.wait((m) => m.method === "task");
	await w.submit(JSON.stringify({ title: "Queued", goal: "One follow-up" }));
	// When the worker replays that reset, it returns the same submission before its idle check.
	expect((await w.call("reset", params)).result).toEqual(cleared.result);
	const refused = await w.call("reset", { ...params, requestId: "clear-refused" });
	expect(refused.error?.code).toBe("conversation_busy");
	w.send({ id: running.id, result: { id: "running-task" } });
	const queued = await w.wait((m) => m.method === "task" && m.params?.["sourceRequestId"] === "request-2");
	w.send({ id: queued.id, result: { id: "queued-task" } });
	await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === "request-2");
	// Then the transcript has one reset and both inputs still have separate answers.
	const history = await w.call("history", {
		agentId: "bot",
		conversationId: w.conversationId,
		before: Number.MAX_SAFE_INTEGER,
		limit: 100,
	});
	expect(history.error).toBeUndefined();
	const result = history.result;
	if (typeof result !== "object" || result === null || !("items" in result) || !Array.isArray(result.items))
		throw new Error("missing history items");
	expect(result.items.filter((item) => item.kind === "boundary")).toHaveLength(1);
	expect(result.items.filter((item) => item.kind === "user").map((item) => item.id)).toEqual([
		"request-1",
		"request-2",
	]);
	for (const id of ["request-1", "request-2"])
		expect(
			w.messages.find((m) => m.event === "settled" && m.params?.["requestId"] === id)?.params?.["status"],
		).toBe("done");
});

// Each query waits for settlement before it changes the next input.
/* oxlint-disable no-await-in-loop */
test("user shares owner facts while each agent loads only its own work memory", async () => {
	const w = await worker();
	await mkdir(join(w.dir, "memory/agents/bot"), { recursive: true });
	await mkdir(join(w.dir, "memory/agents/other"), { recursive: true });
	await writeFile(join(w.dir, "memory/MEMORY.md"), "# Memory\nOwner drinks tea\n## Index\n");
	await writeFile(join(w.dir, "memory/agents/bot/MEMORY.md"), "Bot checks migrations");
	await writeFile(join(w.dir, "memory/agents/other/MEMORY.md"), "Other checks translations");
	let request = 0;
	const section = async (agentId: string, name: string, source: string) => {
		const model = { provider: "faux", modelId: "section" };
		const created = await w.call("createConversation", { agentId, model, thinkingLevel: "off" });
		const result = created.result;
		if (typeof result !== "object" || result === null || !("conversationId" in result))
			throw new Error("missing conversation");
		const requestId = `memory-${++request}`;
		expect(
			(
				await w.call("submit", {
					agentId,
					conversationId: result.conversationId,
					model,
					thinkingLevel: "off",
					instructions: "",
					requestId,
					content: name,
					attachments: [],
					source,
				})
			).error,
		).toBeUndefined();
		const settled = await w.wait((m) => m.event === "settled" && m.params?.["requestId"] === requestId);
		expect(settled.params?.["status"]).toBe("done");
		return String(settled.params?.["answer"]);
	};
	for (const agentId of ["bot", "other"]) {
		const source = `dex:${agentId}/chat/12`;
		expect(await section(agentId, "memory", source)).toContain("Owner drinks tea");
		const personal = await section(agentId, "agent-memory", source);
		expect(personal).toContain(agentId === "bot" ? "Bot checks migrations" : "Other checks translations");
		expect(personal).not.toContain(agentId === "bot" ? "Other checks translations" : "Bot checks migrations");
		const rules = await section(agentId, "memory-rules", source);
		expect(rules).toContain(`agents/${agentId}/MEMORY.md`);
		expect(rules).toContain("Facts about the owner");
		expect(rules).toContain("role-specific experience");
		expect(rules).toContain("Old `dex:chat/<id>` sources remain valid and mean the default agent");
		expect(rules).toContain(`source: ${source}`);
		expect(await section(agentId, "memory-rules", `dex:${agentId}/dream/12`)).not.toContain(
			`source: dex:${agentId}/dream/12`,
		);
	}
	// Missing files omit the section. The next request also reloads an edited entry point.
	await rm(join(w.dir, "memory/agents/other/MEMORY.md"));
	expect(await section("other", "agent-memory", "dex:other/chat/13")).not.toContain(
		"Other checks translations",
	);
	await writeFile(join(w.dir, "memory/agents/bot/MEMORY.md"), "Bot checks schemas");
	expect(await section("bot", "agent-memory", "dex:bot/chat/13")).toContain("Bot checks schemas");
}, 30_000);
/* oxlint-enable no-await-in-loop */
