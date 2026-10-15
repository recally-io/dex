import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import type { Message, Models } from "@earendil-works/pi-ai";
import {
	AssistantEntry,
	type Conversation,
	type ConversationId,
	createRegistry,
	Harness,
	type Submission,
	watchEvents,
	type AgentEventStream,
} from "@earendil-works/pi-durable";
import { NodeExecutionEnv } from "@earendil-works/pi-durable/env/node";
import { openNodeSqliteStorage } from "@earendil-works/pi-durable/storage/sqlite/node";
import { CodingTools } from "@earendil-works/pi-durable/tools";
import { MemoryRepo } from "./memory.ts";
import { ChatView, messageText, USER_ENTRY } from "./view.ts";
import type {
	AbortParams,
	ChatStreamEvent,
	CreateConversationParams,
	CreateConversationResult,
	SettledEvent,
	SubmitParams,
	SubmitResult,
	UnwatchParams,
	WatchParams,
} from "./protocol.ts";

const context = BACKGROUND_CONTEXT;

// Agent IDs become directory names.
const AGENT_ID = /^[A-Za-z0-9_-]{1,64}$/;

/**
 * Owns one pi-durable Harness per agent, opened on first use. pi-durable allows one process per
 * storage, so this worker must be the only process that opens an agent directory.
 */
// Harnesses stay open until shutdown; close idle ones when the agent count grows.
export class Agents {
	readonly #dataDir: string;
	readonly #models: Models;
	readonly #emit: (event: SettledEvent) => void;
	readonly #emitChat: (event: ChatStreamEvent) => void;
	readonly #memory: MemoryRepo;
	// The source of each conversation, by "<agent>/<conversation>", from its last submit. A run that
	// resumes after a restart has none until control submits to it again.
	readonly #sources = new Map<string, string>();
	readonly #watches = new Map<string, AgentEventStream>();
	readonly #harnesses = new Map<string, Promise<Harness>>();
	// The tail of each conversation's submit chain; see #serial.
	readonly #chains = new Map<string, Promise<unknown>>();

	constructor(
		dataDir: string,
		models: Models,
		emit: (event: SettledEvent) => void,
		emitChat: (event: ChatStreamEvent) => void,
	) {
		this.#dataDir = dataDir;
		this.#models = models;
		this.#emit = emit;
		this.#emitChat = emitChat;
		this.#memory = new MemoryRepo(join(dataDir, "memory"));
	}

	async createConversation(params: CreateConversationParams): Promise<CreateConversationResult> {
		const harness = await this.#harness(params.agentId);
		const conversation = await harness.createConversation(
			{
				ownership: { kind: "ownerless" },
				agent: {
					model: params.model,
					thinkingLevel: params.thinkingLevel,
					instructions: params.instructions,
				},
			},
			context,
		);
		return { conversationId: conversation.id };
	}

	async submit(params: SubmitParams): Promise<SubmitResult> {
		const conversation = await this.#conversation(params.agentId, params.conversationId);
		// configure() and submit() are separate commits. Without the chain, two submits with
		// different settings could interleave, and a message could run with the other one's settings.
		const key = `${params.agentId}/${params.conversationId}`;
		this.#sources.set(key, params.source);
		return this.#serial(key, async () => {
			const agent = await conversation.agent(context);
			const instructions = params.instructions === "" ? undefined : params.instructions;
			if (
				agent.model?.provider !== params.model.provider ||
				agent.model.modelId !== params.model.modelId ||
				agent.thinkingLevel !== params.thinkingLevel ||
				agent.instructions !== instructions
			) {
				// A run in progress keeps its settings until its next turn.
				await conversation.configure(
					{ model: params.model, thinkingLevel: params.thinkingLevel, instructions: instructions ?? null },
					context,
				);
			}
			const submission = await conversation.submit(
				{
					type: "input",
					// The model gets the paths; its tools read the files.
					content: messageText(params.content, params.attachments),
					requestId: params.requestId,
					// The chat view shows the message with control's ID and its attachments.
					entry: {
						kind: USER_ENTRY,
						data: { requestId: params.requestId, attachments: params.attachments },
					},
				},
				context,
			);
			void this.#settle(params, conversation, submission);
			return { submissionId: submission.id };
		});
	}

	/**
	 * Starts a watch of a conversation's chat events. The first event is a snapshot. The worker sends
	 * it before this method returns.
	 */
	async watch(params: WatchParams): Promise<Record<string, never>> {
		if (this.#watches.has(params.watchId)) throw new Error(`duplicate watch ID: ${params.watchId}`);
		const conversation = await this.#conversation(params.agentId, params.conversationId);
		const stream = await watchEvents(await this.#harness(params.agentId), conversation.id, context);
		this.#watches.set(params.watchId, stream);
		const view = new ChatView();
		this.#emitChat({ watchId: params.watchId, events: [view.snapshot(stream.snapshot)] });
		stream.start(async (events) => {
			const changes = view.apply(events);
			if (changes.length > 0) this.#emitChat({ watchId: params.watchId, events: changes });
		});
		void stream.closed.then(
			() => this.#endWatch(params.watchId, stream),
			(error: unknown) => {
				console.error(`watch ${params.watchId}:`, error);
				this.#endWatch(params.watchId, stream);
			},
		);
		return {};
	}

	async unwatch(params: UnwatchParams): Promise<Record<string, never>> {
		await this.#watches.get(params.watchId)?.stop();
		return {};
	}

	#endWatch(watchId: string, stream: AgentEventStream): void {
		if (this.#watches.get(watchId) !== stream) return;
		this.#watches.delete(watchId);
		this.#emitChat({ watchId, end: true });
	}

	/** Stops the conversation's run and withdraws its queued messages. */
	async abort(params: AbortParams): Promise<Record<string, never>> {
		const conversation = await this.#conversation(params.agentId, params.conversationId);
		await conversation.abort(context);
		return {};
	}

	async #conversation(agentId: string, conversationId: number): Promise<Conversation> {
		const harness = await this.#harness(agentId);
		// The ID came from this worker's own createConversation; pi-durable brands IDs only at its own boundaries.
		// oxlint-disable-next-line typescript/no-unsafe-type-assertion
		const conversation = await harness.conversation(conversationId as ConversationId, context);
		if (conversation === undefined) throw new Error(`unknown conversation: ${conversationId}`);
		return conversation;
	}

	#serial<T>(key: string, task: () => Promise<T>): Promise<T> {
		const result = (this.#chains.get(key) ?? Promise.resolve()).then(task);
		const tail = result.catch(() => undefined);
		this.#chains.set(key, tail);
		void tail.then(() => {
			if (this.#chains.get(key) === tail) this.#chains.delete(key);
		});
		return result;
	}

	async close(): Promise<void> {
		await Promise.all([...this.#watches.values()].map((stream) => stream.stop()));
		const opening = [...this.#harnesses.values()];
		this.#harnesses.clear();
		await Promise.all(
			opening.map(async (pending) => {
				// A harness that failed to open has nothing to close; its request already got the error.
				const harness = await pending.catch(() => undefined);
				await harness?.close(context);
			}),
		);
	}

	// Runs in the background after submit() returns. A replayed requestId finds the same
	// submission, so a settled one emits its event again at once.
	async #settle(params: SubmitParams, conversation: Conversation, submission: Submission): Promise<void> {
		const base = { agentId: params.agentId, conversationId: conversation.id, requestId: params.requestId };
		try {
			const settled = await submission.wait(context);
			if (settled.status === "done" && settled.type === "input") {
				const entry = await conversation.commit((tx) => tx.entry(AssistantEntry, settled.answer), context);
				this.#emit({ ...base, status: "done", answer: answerText(entry?.model?.[0]) });
			} else {
				this.#emit({ ...base, status: "unanswered", reason: settled.reason ?? settled.status });
			}
		} catch (error) {
			this.#emit({ ...base, status: "unanswered", reason: String(error) });
		}
		// A stopped or failed run can also have changed the memory.
		await this.#memory
			.commit(`Update memory from ${params.source}`)
			.catch((error: unknown) => console.error("commit memory:", error));
	}

	#harness(agentId: string): Promise<Harness> {
		if (!AGENT_ID.test(agentId)) throw new Error(`invalid agent ID: ${agentId}`);
		let harness = this.#harnesses.get(agentId);
		if (harness === undefined) {
			harness = this.#open(agentId);
			// A failed open must not stay cached, so the next request tries again.
			harness.catch(() => this.#harnesses.delete(agentId));
			this.#harnesses.set(agentId, harness);
		}
		return harness;
	}

	async #open(agentId: string): Promise<Harness> {
		const dir = join(this.#dataDir, "agents", agentId);
		const workspace = join(dir, "workspace");
		await mkdir(workspace, { recursive: true });
		const registry = createRegistry();
		registry.install(CodingTools);
		await this.#memory.open();
		registry.install(this.#memory.extension((id) => this.#sources.get(`${agentId}/${id}`)));
		const harness = await Harness.open(
			await openNodeSqliteStorage(join(dir, "state.sqlite")),
			{
				models: this.#models,
				registry,
				// No sandbox: tools run on the host, inside the agent workspace.
				env: ({ cwd }) => new NodeExecutionEnv({ cwd: cwd ?? workspace }),
				onReport: (error) => console.error(`agent ${agentId}:`, error),
			},
			context,
		);
		harness.resume();
		return harness;
	}
}

function answerText(message: Message | undefined): string {
	if (message?.role !== "assistant") return "";
	return message.content
		.filter((block) => block.type === "text")
		.map((block) => block.text)
		.join("");
}
