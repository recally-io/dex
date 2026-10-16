import { ConversationBusy } from "@earendil-works/pi-durable";
import { TaskRpc } from "./tasks.ts";
import { createInterface } from "node:readline";
import { Value } from "typebox/value";
import { Agents } from "./agents.ts";
import { Catalog } from "./models.ts";
import {
	AbortParams,
	AgentParams,
	ConfigureParams,
	CreateConversationParams,
	type Event,
	Request,
	type Response,
	type TaskRequest,
	SubmitParams,
	UnwatchParams,
	WatchParams,
	QuiesceParams,
	RecoverTasksParams,
} from "./protocol.ts";

// stdout carries the protocol. Send every console method that writes to stdout to stderr, so a
// library log line cannot corrupt the stream.
console.log = console.error;
console.info = console.error;
console.debug = console.error;

const dataDir = process.env["DEX_DATA_DIR"];
if (dataDir === undefined || dataDir === "") {
	console.error("DEX_DATA_DIR is not set");
	process.exit(2);
}

function write(message: Response | Event | TaskRequest): void {
	process.stdout.write(`${JSON.stringify(message)}\n`);
}

const catalog = new Catalog(process.env);
const taskRpc = new TaskRpc(write);
const agents = new Agents(
	dataDir,
	catalog.models,
	(params) => write({ event: "settled", params }),
	(params) => write({ event: "chat", params }),
	(params, signal) => taskRpc.call(params, signal),
	(params) => write({ event: "taskProgress", params }),
);

async function handle(request: Request): Promise<unknown> {
	switch (request.method) {
		case "recoverTasks":
			return agents.recoverTasks(Value.Parse(RecoverTasksParams, request.params));
		case "quiesce":
			return agents.quiesce(Value.Parse(QuiesceParams, request.params));
		case "resume":
			return agents.resume();
		case "ping":
			return {};
		case "createConversation":
			return agents.createConversation(Value.Parse(CreateConversationParams, request.params));
		case "submit":
			return agents.submit(Value.Parse(SubmitParams, request.params));
		case "watch":
			return agents.watch(Value.Parse(WatchParams, request.params));
		case "unwatch":
			return agents.unwatch(Value.Parse(UnwatchParams, request.params));
		case "abort":
			return agents.abort(Value.Parse(AbortParams, request.params));
		case "extensions":
			return agents.extensions(Value.Parse(AgentParams, request.params));
		case "configure":
			return catalog.configure(Value.Parse(ConfigureParams, request.params).providers);
		case "listProviders":
			return { providers: await catalog.builtins() };
		case "listModels":
			return { models: await catalog.available() };
		default:
			throw new Error(`unknown method: ${request.method}`);
	}
}

const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
lines.on("line", (line) => {
	let request: Request;
	try {
		const message: unknown = JSON.parse(line);
		if (taskRpc.respond(message)) return;
		request = Value.Parse(Request, message);
	} catch {
		console.error("dropping an invalid protocol line");
		return;
	}
	handle(request).then(
		(result) => write({ id: request.id, result: result ?? {} }),
		(error: unknown) =>
			write({
				id: request.id,
				error: {
					message: String(error),
					...(error instanceof ConversationBusy ? { code: "conversation_busy" } : {}),
				},
			}),
	);
});
// Control closes stdin to stop the worker, and the pipe closes when control dies.
lines.on("close", () => {
	taskRpc.close();
	agents.close().then(
		() => process.exit(0),
		(error: unknown) => {
			console.error("close failed:", error);
			process.exit(1);
		},
	);
});
