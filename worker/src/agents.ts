import { mkdir } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import type { Message, Models } from "@earendil-works/pi-ai";
import {
	AssistantEntry,
	type Conversation,
	type ConversationId,
	createRegistry,
	Harness,
	type Registry,
	type Submission,
	watchEvents,
	type AgentEventStream,
} from "@earendil-works/pi-durable";
import { NodeExecutionEnv } from "@earendil-works/pi-durable/env/node";
import { openNodeSqliteStorage } from "@earendil-works/pi-durable/storage/sqlite/node";
import { createToolSearchExtension } from "@earendil-works/pi-durable/tool-search";
import { CodingTools } from "@earendil-works/pi-durable/tools";
import { AgentExtensions, type Loaded } from "./extensions.ts";
import { MemoryRepo } from "./memory.ts";
import { ChatView, messageText, USER_ENTRY } from "./view.ts";
import type {
	AbortParams,
	AgentParams,
	ChatStreamEvent,
	CreateConversationParams,
	CreateConversationResult,
	Extensions,
	ReloadResult,
	SettledEvent,
	SubmitParams,
	SubmitResult,
	UnwatchParams,
	WatchParams,
	QuiesceParams,
	QuiesceResult,
} from "./protocol.ts";

const context = BACKGROUND_CONTEXT;

// Agent IDs become directory names.
const AGENT_ID = /^[A-Za-z0-9_-]{1,64}$/;

const ToolSearch = createToolSearchExtension();

/** The extension files of an open agent, its registry, and the catalog loaded from them. */
type AgentState = { readonly files: AgentExtensions; readonly registry: Registry; loaded: Loaded };

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
	readonly #pendingWatches = new Map<string, AbortController>();
	readonly #harnesses = new Map<string, Promise<Harness>>();
	readonly #agents = new Map<string, AgentState>();
	// The tail of each conversation's submit chain; see #serial.
	readonly #chains = new Map<string, Promise<unknown>>();
	#checking = false;
	#operations = 0;
	#gateUntil = 0;
	// A reload holds every new request until it ends, so no call starts on a client that it closes.
	#reloading: Promise<unknown> | undefined;
	readonly #gateWaiters = new Set<() => void>();

	async quiesce(params: QuiesceParams): Promise<QuiesceResult> {
		if (this.#checking) return { busy: true, reason: "maintenance check is pending" };
		if (this.#reloading !== undefined) return { busy: true, reason: "an extension reload is pending" };
		// Close admission before the first await. Existing operations remain counted.
		this.#gateUntil = params.expiresAt;
		this.#checking = true;
		try {
			if (this.#operations > 0) {
				this.resume();
				return { busy: true, reason: "worker requests or memory commits are pending" };
			}
			const states = await Promise.all(
				[...this.#harnesses.values()].map(async (pending) => (await pending).inspect(context)),
			);
			if (states.some((state) => state.tasks.length > 0 || state.submissions.length > 0)) {
				this.resume();
				return { busy: true, reason: "durable work is unfinished" };
			}
			return { busy: false };
		} catch (error) {
			this.resume();
			throw error;
		} finally {
			this.#checking = false;
		}
	}

	resume(): Record<string, never> {
		this.#gateUntil = 0;
		for (const wake of this.#gateWaiters) wake();
		return {};
	}

	async #admit<T>(task: () => Promise<T>, signal?: AbortSignal): Promise<T> {
		while (this.#gateUntil > Date.now() || this.#reloading !== undefined) {
			signal?.throwIfAborted();
			if (this.#reloading !== undefined) {
				// oxlint-disable-next-line no-await-in-loop
				await this.#reloading;
				continue;
			}
			const expiresAt = this.#gateUntil;
			// Each wake must recheck a lease that another updater can renew.
			// oxlint-disable-next-line no-await-in-loop
			await new Promise<void>((resolve) => {
				const wake = () => {
					clearTimeout(timer);
					this.#gateWaiters.delete(wake);
					signal?.removeEventListener("abort", wake);
					resolve();
				};
				const timer = setTimeout(wake, Math.max(1, expiresAt - Date.now()));
				this.#gateWaiters.add(wake);
				signal?.addEventListener("abort", wake, { once: true });
			});
		}
		signal?.throwIfAborted();
		this.#operations++;
		try {
			return await task();
		} finally {
			this.#operations--;
		}
	}

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

	createConversation(params: CreateConversationParams): Promise<CreateConversationResult> {
		return this.#admit(() => this.#createConversation(params));
	}

	async #createConversation(params: CreateConversationParams): Promise<CreateConversationResult> {
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

	submit(params: SubmitParams): Promise<SubmitResult> {
		return this.#admit(() => this.#submit(params));
	}

	async #submit(params: SubmitParams): Promise<SubmitResult> {
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
			this.#operations++;
			void this.#settle(params, conversation, submission).finally(() => {
				this.#operations--;
			});
			return { submissionId: submission.id };
		});
	}

	/**
	 * Starts a watch of a conversation's chat events. The first event is a snapshot. The worker sends
	 * it before this method returns.
	 */
	async watch(params: WatchParams): Promise<Record<string, never>> {
		if (this.#pendingWatches.has(params.watchId) || this.#watches.has(params.watchId))
			throw new Error(`duplicate watch ID: ${params.watchId}`);
		const pending = new AbortController();
		this.#pendingWatches.set(params.watchId, pending);
		try {
			return await this.#admit(() => this.#watch(params, pending.signal), pending.signal);
		} finally {
			this.#pendingWatches.delete(params.watchId);
		}
	}

	async #watch(params: WatchParams, signal: AbortSignal): Promise<Record<string, never>> {
		const conversation = await this.#conversation(params.agentId, params.conversationId);
		const stream = await watchEvents(await this.#harness(params.agentId), conversation.id, context);
		if (signal.aborted) {
			await stream.stop();
			return {};
		}
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
		this.#pendingWatches.get(params.watchId)?.abort();
		await this.#watches.get(params.watchId)?.stop();
		return {};
	}

	#endWatch(watchId: string, stream: AgentEventStream): void {
		if (this.#watches.get(watchId) !== stream) return;
		this.#watches.delete(watchId);
		this.#emitChat({ watchId, end: true });
	}

	/** Stops the conversation's run and withdraws its queued messages. */
	abort(params: AbortParams): Promise<Record<string, never>> {
		return this.#admit(() => this.#abort(params));
	}

	async #abort(params: AbortParams): Promise<Record<string, never>> {
		const conversation = await this.#conversation(params.agentId, params.conversationId);
		await conversation.abort(context);
		return {};
	}

	/** The agent's skills and MCP servers. It does not start an agent that is not open. */
	async extensions(params: AgentParams): Promise<Extensions> {
		return this.#agents.get(params.agentId)?.loaded.info ?? this.#files(params.agentId).declared();
	}

	/** Reads the extension files again and reconnects the MCP servers, only when the agent is idle. */
	async reloadExtensions(params: AgentParams): Promise<ReloadResult> {
		// A request in progress could use the catalog that the reload closes.
		if (
			this.#checking ||
			this.#reloading !== undefined ||
			this.#gateUntil > Date.now() ||
			this.#operations > 0
		)
			return { status: "busy", reason: "the agent has unfinished work or requests; reload when it is done" };
		const reload = this.#reload(params.agentId);
		this.#reloading = reload.catch(() => undefined);
		try {
			return await reload;
		} finally {
			this.#reloading = undefined;
		}
	}

	async #reload(agentId: string): Promise<ReloadResult> {
		const opened = this.#agents.has(agentId);
		const harness = await this.#harness(agentId);
		const state = this.#agents.get(agentId);
		if (state === undefined) throw new Error(`agent ${agentId} is not open`);
		// The open has just read the files.
		if (!opened) return reloadResult(state.loaded);
		// A run that changes its tools between two turns could call a tool of the new server with the
		// arguments that it planned for the old one.
		const { tasks, submissions } = await harness.inspect(context);
		if (tasks.length > 0 || submissions.length > 0)
			return { status: "busy", reason: "the agent has unfinished work; reload when it is done" };
		const next = await state.files.load();
		if (next.info.errors.length > 0) {
			await next.close();
			return reloadResult(next);
		}
		const previous = state.loaded;
		try {
			install(state.registry, next);
		} catch (error) {
			await next.close();
			throw error;
		}
		state.loaded = next;
		await previous.close();
		return reloadResult(next);
	}

	#files(agentId: string): AgentExtensions {
		if (!AGENT_ID.test(agentId)) throw new Error(`invalid agent ID: ${agentId}`);
		const dir = join(this.#dataDir, "agents", agentId);
		return new AgentExtensions(homedir(), dir, join(dir, "workspace"));
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
		await Promise.all([...this.#agents.values()].map((state) => state.loaded.close()));
		this.#agents.clear();
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
		// A run that resumes after a restart needs its MCP tools, so they load before resume().
		const files = this.#files(agentId);
		const loaded = await files.load();
		for (const error of loaded.info.errors) console.error(`agent ${agentId} extensions:`, error);
		let harness: Harness;
		try {
			install(registry, loaded);
			harness = await Harness.open(
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
		} catch (error) {
			await loaded.close();
			throw error;
		}
		this.#agents.set(agentId, { files, registry, loaded });
		harness.resume();
		return harness;
	}
}

function install(registry: Registry, loaded: Loaded): void {
	registry.install(loaded.extension);
	if (loaded.hasTools) registry.install(ToolSearch);
	else registry.uninstall(ToolSearch);
}

function reloadResult(loaded: Loaded): ReloadResult {
	const { errors } = loaded.info;
	return errors.length > 0
		? { status: "invalid", reason: errors.join("\n") }
		: { status: "loaded", extensions: loaded.info };
}

function answerText(message: Message | undefined): string {
	if (message?.role !== "assistant") return "";
	return message.content
		.filter((block) => block.type === "text")
		.map((block) => block.text)
		.join("");
}
