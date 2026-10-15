// Golden tests of the chat view: recorded shapes of pi-durable agent event batches go in, and the
// chat events for the web UI come out. The cases are the edge cases that a one-to-one translation
// gets wrong.
import { describe, expect, it } from "bun:test";
import type { AgentEvent, SnapshotEvent } from "@earendil-works/pi-durable";
import { ChatView, messageText, USER_ENTRY } from "../src/view.ts";

// The fixtures hold only the fields that the view reads; pi-durable's types have more.
/* oxlint-disable typescript/no-unsafe-type-assertion */
const usage = { input: 10, output: 5, cacheRead: 0, cacheWrite: 0, totalTokens: 15, cost: {} };

function assistant(content: unknown[], stopReason = "stop", extra: object = {}): unknown {
	return {
		role: "assistant",
		content,
		api: "faux",
		provider: "faux",
		model: "echo",
		usage,
		stopReason,
		timestamp: 2,
		...extra,
	};
}

function entry(id: number, kind: string, message: unknown, data?: unknown): unknown {
	return { id, conversationId: 1, kind, model: [message], ...(data === undefined ? {} : { data }) };
}

const userEntry = entry(1, USER_ENTRY, { role: "user", content: "hi", timestamp: 1 }, { requestId: "r1" });

function snapshot(entries: unknown[], extra: object = {}): SnapshotEvent {
	return {
		type: "snapshot",
		entries,
		tools: [],
		nestedTools: [],
		compactions: [],
		inbox: [],
		agent: {},
		usage: {},
		...extra,
	} as unknown as SnapshotEvent;
}

function batch(...events: unknown[]): AgentEvent[] {
	return events as AgentEvent[];
}

describe("ChatView", () => {
	it("streams text as appends and completes the item with model and usage", () => {
		const view = new ChatView();
		expect(view.snapshot(snapshot([userEntry]))).toEqual({
			type: "snapshot",
			items: [{ kind: "user", id: "r1", text: "hi", at: 1 }],
			running: false,
		});

		expect(
			view.apply(
				batch(
					{ type: "run_start", inputs: [] },
					{ type: "message_start", message: assistant([], "pending") },
					{
						type: "message_update",
						usage,
						changes: [{ type: "text_start", contentIndex: 0, block: { type: "text", text: "" } }],
					},
				),
			),
		).toEqual([
			{ type: "item", item: { kind: "assistant", id: "a0:0", text: "" } },
			{ type: "run", running: true },
		]);

		expect(
			view.apply(
				batch({
					type: "message_update",
					usage,
					changes: [{ type: "text_delta", contentIndex: 0, delta: "Hel" }],
				}),
			),
		).toEqual([{ type: "append", id: "a0:0", text: "Hel" }]);

		const done = entry(2, "pi.assistant", assistant([{ type: "text", text: "Hello" }]));
		expect(view.apply(batch({ type: "message_end", entry: done }, { type: "run_end", inputs: [] }))).toEqual([
			{
				type: "item",
				item: {
					kind: "assistant",
					id: "a0:0",
					text: "Hello",
					model: "faux/echo",
					usage: { input: 10, output: 5 },
				},
			},
			{ type: "run", running: false },
		]);
	});

	it("sends a replaced block as an item, not as an append", () => {
		const view = new ChatView();
		view.snapshot(
			snapshot([userEntry], {
				run: { inputs: [] },
				generation: { attempt: 0, message: assistant([{ type: "text", text: "draft" }], "pending") },
			}),
		);
		expect(
			view.apply(
				batch({
					type: "message_update",
					usage,
					changes: [{ type: "block", contentIndex: 0, block: { type: "text", text: "final" } }],
				}),
			),
		).toEqual([{ type: "item", item: { kind: "assistant", id: "a0:0", text: "final" } }]);
	});

	it("matches a tool result by call ID when the tool ends before the result entry arrives", () => {
		const view = new ChatView();
		const call = { type: "toolCall", id: "call-1", name: "bash", arguments: { command: "ls" } };
		view.snapshot(
			snapshot([userEntry, entry(2, "pi.assistant", assistant([call], "toolUse"))], { run: { inputs: [] } }),
		);

		const started = view.apply(
			batch({
				type: "tool_execution_start",
				toolCallId: "call-1",
				toolName: "bash",
				args: { command: "ls" },
			}),
		);
		expect(started).toEqual([]);

		expect(
			view.apply(
				batch({
					type: "tool_execution_update",
					toolCallId: "call-1",
					toolName: "bash",
					output: { append: "a.txt\n" },
				}),
			),
		).toEqual([
			{
				type: "item",
				item: {
					kind: "tool",
					id: "call-1",
					name: "bash",
					args: { command: "ls" },
					output: "a.txt\n",
					status: "running",
				},
			},
		]);

		// The end comes without the entry; the result entry comes in a later batch.
		view.apply(batch({ type: "tool_execution_end", toolCallId: "call-1", toolName: "bash" }));
		const result = entry(3, "pi.toolResult", {
			role: "toolResult",
			toolCallId: "call-1",
			toolName: "bash",
			content: [{ type: "text", text: "a.txt\n" }],
			isError: false,
			timestamp: 3,
			durationMs: 40,
		});
		expect(view.apply(batch({ type: "entry_appended", entry: result }))).toEqual([
			{
				type: "item",
				item: {
					kind: "tool",
					id: "call-1",
					name: "bash",
					args: { command: "ls" },
					output: "a.txt\n",
					status: "done",
					durationMs: 40,
				},
			},
		]);
	});

	it("marks a tool that ended without a result as an error", () => {
		const view = new ChatView();
		const call = { type: "toolCall", id: "call-1", name: "bash", arguments: {} };
		view.snapshot(
			snapshot([userEntry, entry(2, "pi.assistant", assistant([call], "toolUse"))], { run: { inputs: [] } }),
		);
		view.apply(batch({ type: "tool_execution_start", toolCallId: "call-1", toolName: "bash", args: {} }));
		const events = view.apply(
			batch(
				{ type: "tool_execution_end", toolCallId: "call-1", toolName: "bash" },
				{ type: "run_end", inputs: [] },
			),
		);
		expect(events).toContainEqual({
			type: "item",
			item: { kind: "tool", id: "call-1", name: "bash", args: {}, output: "", status: "error" },
		});
	});

	it("keeps trimmed tool output in order", () => {
		const view = new ChatView();
		const call = { type: "toolCall", id: "c", name: "bash", arguments: {} };
		view.snapshot(
			snapshot([userEntry, entry(2, "pi.assistant", assistant([call], "toolUse"))], { run: { inputs: [] } }),
		);
		view.apply(batch({ type: "tool_execution_start", toolCallId: "c", toolName: "bash", args: {} }));
		view.apply(
			batch({
				type: "tool_execution_update",
				toolCallId: "c",
				toolName: "bash",
				output: { append: "abcdef" },
			}),
		);
		expect(
			view.apply(
				batch({
					type: "tool_execution_update",
					toolCallId: "c",
					toolName: "bash",
					output: { trimStart: 3, append: "gh" },
				}),
			),
		).toEqual([
			{
				type: "item",
				item: { kind: "tool", id: "c", name: "bash", args: {}, output: "defgh", status: "running" },
			},
		]);
	});

	it("sends a snapshot when pi-durable sends one after an overflow", () => {
		const view = new ChatView();
		view.snapshot(snapshot([userEntry]));
		const events = view.apply(
			batch({ type: "run_start", inputs: [] }, snapshot([userEntry], { run: { inputs: [] } })),
		);
		expect(events).toEqual([
			{ type: "snapshot", items: [{ kind: "user", id: "r1", text: "hi", at: 1 }], running: true },
		]);
	});

	it("shows a model error and a stopped run as error items", () => {
		const view = new ChatView();
		const failed = entry(2, "pi.assistant", assistant([], "error", { errorMessage: "HTTP 401" }));
		const stopped = entry(3, "pi.assistant", assistant([{ type: "text", text: "par" }], "aborted"));
		const event = view.snapshot(snapshot([userEntry, failed, stopped]));
		expect(event).toMatchObject({
			items: [
				{ kind: "user", id: "r1" },
				{ kind: "error", id: "a0:error", reason: "HTTP 401" },
				{ kind: "assistant", id: "a1:0", text: "par" },
				{ kind: "error", id: "a1:error", reason: "Stopped.", aborted: true },
			],
		});
	});

	it("reports a retry and its end", () => {
		const view = new ChatView();
		view.snapshot(snapshot([userEntry], { run: { inputs: [] } }));
		expect(
			view.apply(batch({ type: "auto_retry_start", attempt: 1, at: 99, errorMessage: "overloaded" })),
		).toEqual([{ type: "run", running: true, retry: { attempt: 1, at: 99, reason: "overloaded" } }]);
		expect(view.apply(batch({ type: "auto_retry_end", attempt: 1 }))).toEqual([
			{ type: "run", running: true },
		]);
	});
});

describe("attachments", () => {
	const pic = { id: "u1", name: "a b.png", size: 3, mimeType: "image/png", path: "uploads/u1/a b.png" };
	const notes = { id: "u2", name: "n.txt", size: 1, mimeType: "text/plain", path: "uploads/u2/n.txt" };

	it("gives the model the text and the workspace paths", () => {
		expect(messageText("look", [])).toBe("look");
		expect(messageText("look", [pic, notes])).toBe(
			"look\n\nAttached files:\n- uploads/u1/a b.png\n- uploads/u2/n.txt",
		);
		expect(messageText("", [notes])).toBe("Attached files:\n- uploads/u2/n.txt");
	});

	it("shows the user's text and the files, not the path list", () => {
		const content = messageText("look", [pic, notes]);
		const withText = entry(
			1,
			USER_ENTRY,
			{ role: "user", content, timestamp: 1 },
			{ requestId: "r1", attachments: [pic, notes] },
		);
		const only = entry(
			3,
			USER_ENTRY,
			{ role: "user", content: messageText("", [notes]), timestamp: 3 },
			{ requestId: "r2", attachments: [notes] },
		);
		expect(new ChatView().snapshot(snapshot([withText, only]))).toEqual({
			type: "snapshot",
			items: [
				{ kind: "user", id: "r1", text: "look", at: 1, attachments: [pic, notes] },
				{ kind: "user", id: "r2", text: "", at: 3, attachments: [notes] },
			],
			running: false,
		});
	});
});
