import { createInterface } from "node:readline";
import { Value } from "typebox/value";
import { Agents } from "./agents.ts";
import { createModels } from "./models.ts";
import { CreateConversationParams, type Event, Request, type Response, SubmitParams } from "./protocol.ts";

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

function write(message: Response | Event): void {
	process.stdout.write(`${JSON.stringify(message)}\n`);
}

const agents = new Agents(dataDir, await createModels(process.env), (params) =>
	write({ event: "settled", params }),
);

async function handle(request: Request): Promise<unknown> {
	switch (request.method) {
		case "ping":
			return {};
		case "createConversation":
			return agents.createConversation(Value.Parse(CreateConversationParams, request.params));
		case "submit":
			return agents.submit(Value.Parse(SubmitParams, request.params));
		default:
			throw new Error(`unknown method: ${request.method}`);
	}
}

const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
lines.on("line", (line) => {
	let request: Request;
	try {
		request = Value.Parse(Request, JSON.parse(line));
	} catch (error) {
		console.error("dropping a line that is not a request:", line, error);
		return;
	}
	handle(request).then(
		(result) => write({ id: request.id, result: result ?? {} }),
		(error: unknown) => write({ id: request.id, error: { message: String(error) } }),
	);
});
// Control closes stdin to stop the worker, and the pipe closes when control dies.
lines.on("close", () => {
	agents.close().then(
		() => process.exit(0),
		(error: unknown) => {
			console.error("close failed:", error);
			process.exit(1);
		},
	);
});
