// The chat view: turns pi-durable agent events into the display items and events of the web UI
// (api/openapi.yaml, schemas Item and ChatEvent). The UI never sees pi-durable types.
//
// pi-durable events are fine-grained and have edge cases: block replacement, trimmed tool output, a
// tool end before its result entry, a tool end without a result, and a snapshot again after an
// overflow. So the view does not translate events one to one. It keeps the raw state, renders all
// items from it with one function, and diffs the new items against the old ones.
import type { AgentEvent, EntryRecord, SnapshotEvent } from "@earendil-works/pi-durable";
import type { AssistantMessage, Message, ToolResultMessage } from "@earendil-works/pi-ai";
import type { Attachment } from "./protocol.ts";

/**
 * The kind of the entry that a dex user message is placed as. Its data holds control's ID and the
 * attachments.
 */
export const USER_ENTRY = "dex.user";
export const TASK_RESULT_ENTRY = "dex.task_result";

/** The text of a user message for the model: the user's text, then the paths of the attachments. */
export function messageText(text: string, attachments: readonly Attachment[]): string {
	if (attachments.length === 0) return text;
	const list = `Attached files:\n${attachments.map((a) => `- ${a.path}`).join("\n")}`;
	return text === "" ? list : `${text}\n\n${list}`;
}

export type Retry = { readonly attempt: number; readonly at: number; readonly reason: string };

export type Item = { entryId?: number } & (
	| { kind: "task"; id: string; at?: number }
	| { kind: "user"; id: string; text: string; at?: number; attachments?: Attachment[] }
	| { kind: "thinking"; id: string; text: string }
	| {
			kind: "tool";
			id: string;
			name: string;
			args: Readonly<Record<string, unknown>>;
			output: string;
			status: "running" | "done" | "error";
			durationMs?: number;
	  }
	| { kind: "assistant"; id: string; text: string; model?: string; usage?: { input: number; output: number } }
	| { kind: "error"; id: string; reason: string; aborted?: boolean }
	| { kind: "boundary"; id: string; boundary: "reset" | "compaction"; at?: number }
);

export type ChatEvent =
	| { type: "snapshot"; items: Item[]; running: boolean; retry?: Retry; historyBefore?: number }
	| { type: "item"; item: Item }
	| { type: "append"; id: string; text: string }
	| { type: "run"; running: boolean; retry?: Retry };

type Tool = {
	name: string;
	status: "running" | "done";
	output: string;
	args?: Readonly<Record<string, unknown>>;
};

type State = {
	entries: EntryRecord[];
	partial?: AssistantMessage;
	tools: Map<string, Tool>;
	running: boolean;
	retry?: Retry;
};

export class ChatView {
	#state: State = { entries: [], tools: new Map(), running: false };
	#items: Item[] = [];

	/** Replaces all state with a snapshot and returns the snapshot event. */
	snapshot(event: SnapshotEvent): ChatEvent {
		const tools = new Map<string, Tool>();
		for (const slot of event.tools) {
			tools.set(slot.callId, {
				name: slot.name,
				status: slot.status === "done" ? "done" : "running",
				output: slot.output ?? "",
			});
		}
		const retry = event.generation?.retry;
		this.#state = {
			entries: [...event.entries],
			...(event.generation?.message === undefined ? {} : { partial: event.generation.message }),
			tools,
			running: event.run !== undefined,
			...(retry === undefined
				? {}
				: { retry: { attempt: event.generation?.attempt ?? 0, at: retry.at, reason: retry.error } }),
		};
		this.#items = render(this.#state);
		return this.#snapshotEvent();
	}

	/** Applies one batch of events and returns the changes for the UI. */
	apply(events: readonly AgentEvent[]): ChatEvent[] {
		let snapshot = false;
		const before = { running: this.#state.running, retry: this.#state.retry };
		for (const event of events) {
			if (event.type === "snapshot") {
				this.snapshot(event);
				snapshot = true;
			} else {
				reduce(this.#state, event);
			}
		}
		const items = render(this.#state);
		const out: ChatEvent[] = [];
		const changes = snapshot ? undefined : diff(this.#items, items);
		this.#items = items;
		if (changes === undefined) return [this.#snapshotEvent()];
		out.push(...changes);
		if (before.running !== this.#state.running || before.retry !== this.#state.retry) {
			out.push({ type: "run", running: this.#state.running, ...retryField(this.#state.retry) });
		}
		return out;
	}

	#snapshotEvent(): ChatEvent {
		return {
			type: "snapshot",
			items: this.#items,
			running: this.#state.running,
			...retryField(this.#state.retry),
			...historyField(this.#state.entries),
		};
	}
}

function retryField(retry: Retry | undefined): { retry?: Retry } {
	return retry === undefined ? {} : { retry };
}

function reduce(state: State, event: Exclude<AgentEvent, SnapshotEvent>): void {
	switch (event.type) {
		case "run_start":
			state.running = true;
			break;
		case "run_end":
			state.running = false;
			delete state.partial;
			delete state.retry;
			state.tools.clear();
			break;
		case "message_start":
			if (event.message.role === "assistant") state.partial = structuredClone(event.message);
			break;
		case "message_update":
			if (state.partial === undefined) break;
			for (const change of event.changes) {
				const content = state.partial.content;
				switch (change.type) {
					case "text_start":
					case "thinking_start":
					case "toolcall_start":
					case "block":
						content[change.contentIndex] = structuredClone(change.block);
						break;
					case "text_delta": {
						const block = content[change.contentIndex];
						if (block?.type === "text") block.text += change.delta;
						break;
					}
					case "thinking_delta": {
						const block = content[change.contentIndex];
						if (block?.type === "thinking") block.thinking += change.delta;
						break;
					}
					case "toolcall_delta":
						// The arguments are partial JSON until the block ends; the UI shows them then.
						break;
					case "message":
						state.partial = structuredClone(change.message);
						break;
				}
			}
			break;
		case "message_end":
		case "entry_appended":
			addEntry(state, event.entry);
			if (event.entry.model?.[0]?.role === "assistant") delete state.partial;
			break;
		case "tool_execution_start":
			if (event.parentToolCallId !== undefined) break;
			state.tools.set(event.toolCallId, {
				name: event.toolName,
				status: "running",
				output: "",
				args: event.args,
			});
			break;
		case "tool_execution_update": {
			const tool = state.tools.get(event.toolCallId);
			if (tool === undefined || event.output === undefined) break;
			if ("set" in event.output) {
				tool.output = event.output.set;
			} else {
				tool.output = tool.output.slice(event.output.trimStart ?? 0) + (event.output.append ?? "");
			}
			break;
		}
		case "tool_execution_end": {
			if (event.parentToolCallId !== undefined) break;
			if (event.entry !== undefined) addEntry(state, event.entry);
			const tool = state.tools.get(event.toolCallId);
			if (tool !== undefined) tool.status = "done";
			break;
		}
		case "auto_retry_start":
			state.retry = { attempt: event.attempt, at: event.at, reason: event.errorMessage };
			break;
		case "auto_retry_end":
			delete state.retry;
			break;
		default:
			break;
	}
}

function addEntry(state: State, entry: EntryRecord): void {
	if (!state.entries.some((e) => e.id === entry.id)) state.entries.push(entry);
}

function historyField(entries: EntryRecord[]): { historyBefore?: number } {
	const head = entries.findLast((entry) => entry.head !== undefined);
	return head === undefined ? {} : { historyBefore: head.id };
}

/** Committed IDs use entry IDs. A partial has a temporary ID until its entry is saved. */
export function render(state: State): Item[] {
	const results = new Map<string, ToolResultMessage>();
	for (const entry of state.entries) {
		const message = entry.model?.[0];
		if (message?.role === "toolResult") results.set(message.toolCallId, message);
	}
	const items: Item[] = [];
	// SDK snapshots put the head marker first, even when a compaction keeps older entries.
	const entries = state.entries.toSorted((a, b) => a.id - b.id);
	const lastAssistant = entries.findLastIndex((entry) => entry.model?.[0]?.role === "assistant");
	entries.forEach((entry, index) => {
		let rendered: Item[] = [];
		const message = entry.model?.[0];
		if (entry.kind === "pi.reset" || entry.kind === "pi.compaction") {
			rendered = [
				{
					kind: "boundary",
					id: `e${entry.id}`,
					boundary: entry.kind === "pi.reset" ? "reset" : "compaction",
					...boundaryTime(entry),
				},
			];
		} else if (message?.role === "user" && (entry.kind === USER_ENTRY || entry.kind === "pi.user")) {
			rendered = [userItem(entry, message)];
		} else if (entry.kind === TASK_RESULT_ENTRY && message !== undefined) {
			const id = userData(entry.data)?.requestId;
			if (typeof id === "string") rendered = [{ kind: "task", id, at: message.timestamp }];
		} else if (message?.role === "assistant") {
			const live = state.running && state.partial === undefined && index === lastAssistant;
			rendered = assistantItems(message, `e${entry.id}`, results, state, live, true);
		}
		for (const item of rendered) {
			item.entryId = entry.id;
			items.push(item);
		}
	});
	if (state.partial !== undefined) {
		items.push(
			...assistantItems(state.partial, `partial:${state.partial.timestamp}`, results, state, true, false),
		);
	}
	return items;
}

function boundaryTime(entry: EntryRecord): { at?: number } {
	if (entry.kind === "pi.compaction") return { at: entry.model?.[0]?.timestamp };
	const data = entry.data;
	return typeof data === "object" &&
		data !== null &&
		!Array.isArray(data) &&
		"at" in data &&
		typeof data["at"] === "number"
		? { at: data["at"] }
		: {};
}

/** The user item shows the user's text and the attachments, not the paths that the model got. */
function userItem(entry: EntryRecord, message: Extract<Message, { role: "user" }>): Item {
	const text = userText(message);
	const data = entry.kind === USER_ENTRY ? userData(entry.data) : undefined;
	const item: Item = { kind: "user", id: data?.requestId ?? `e${entry.id}`, text, at: message.timestamp };
	const attachments = data?.attachments ?? [];
	if (attachments.length === 0) return item;
	const list = messageText("", attachments);
	return { ...item, text: text.endsWith(list) ? text.slice(0, -list.length).trimEnd() : text, attachments };
}

type UserData = { requestId?: string; attachments?: Attachment[] };

// The worker wrote this data in submit(); entries from before attachments have only requestId.
function userData(data: unknown): UserData | undefined {
	if (typeof data !== "object" || data === null || Array.isArray(data)) return undefined;
	return data;
}

function userText(message: Extract<Message, { role: "user" }>): string {
	if (typeof message.content === "string") return message.content;
	return message.content.map((part) => (part.type === "text" ? part.text : "[image]")).join("");
}

function assistantItems(
	message: AssistantMessage,
	key: string,
	results: Map<string, ToolResultMessage>,
	state: State,
	live: boolean,
	committed: boolean,
): Item[] {
	const items: Item[] = [];
	let lastText = -1;
	message.content.forEach((block, i) => {
		const id = `${key}:${i}`;
		switch (block.type) {
			case "thinking":
				items.push({ kind: "thinking", id, text: block.redacted === true ? "" : block.thinking });
				break;
			case "text":
				lastText = items.length;
				items.push({ kind: "assistant", id, text: block.text });
				break;
			case "toolCall":
				items.push(
					toolItem(
						block.id,
						block.name,
						block.arguments,
						results.get(block.id),
						state.tools.get(block.id),
						live,
					),
				);
				break;
		}
	});
	const text = items[lastText];
	if (text?.kind === "assistant" && committed) {
		text.model = `${message.provider}/${message.model}`;
		text.usage = { input: message.usage.input, output: message.usage.output };
	}
	if (message.stopReason === "error") {
		items.push({
			kind: "error",
			id: `${key}:error`,
			reason: message.errorMessage ?? "The model returned an error.",
		});
	} else if (message.stopReason === "aborted") {
		items.push({ kind: "error", id: `${key}:error`, reason: "Stopped.", aborted: true });
	}
	return items;
}

function toolItem(
	id: string,
	name: string,
	args: Readonly<Record<string, unknown>>,
	result: ToolResultMessage | undefined,
	slot: Tool | undefined,
	live: boolean,
): Item {
	if (result !== undefined) {
		const output = result.content.map((part) => (part.type === "text" ? part.text : "[image]")).join("");
		return {
			kind: "tool",
			id,
			name,
			args,
			output,
			status: result.isError ? "error" : "done",
			...(result.durationMs === undefined ? {} : { durationMs: result.durationMs }),
		};
	}
	if (slot !== undefined && slot.status === "running") {
		return { kind: "tool", id, name, args, output: slot.output, status: "running" };
	}
	if (live && slot === undefined) return { kind: "tool", id, name, args, output: "", status: "running" };
	// The call ended without a result: its task faulted, or its run ended first.
	return { kind: "tool", id, name, args, output: slot?.output ?? "", status: "error" };
}

/**
 * Returns the events that change old into new, or undefined when only a snapshot can: an item was
 * removed or the order changed.
 */
export function diff(old: readonly Item[], next: readonly Item[]): ChatEvent[] | undefined {
	if (next.length < old.length) return undefined;
	const out: ChatEvent[] = [];
	for (const [i, item] of next.entries()) {
		const prev = old[i];
		if (prev === undefined) {
			out.push({ type: "item", item });
			continue;
		}
		if (prev.id !== item.id) return undefined;
		if (JSON.stringify(prev) === JSON.stringify(item)) continue;
		const appended = appendedText(prev, item);
		out.push(
			appended === undefined ? { type: "item", item } : { type: "append", id: item.id, text: appended },
		);
	}
	return out;
}

/** The text that new has after old's text, when that is the only change. */
function appendedText(old: Item, next: Item): string | undefined {
	if (!("text" in old) || !("text" in next) || !next.text.startsWith(old.text)) return undefined;
	const added = next.text.slice(old.text.length);
	return JSON.stringify({ ...old, text: next.text }) === JSON.stringify(next) ? added : undefined;
}
