import { mkdir } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import type { Message, Models } from "@earendil-works/pi-ai";
import {
	AssistantEntry,
	ConversationBusy,
	InboxDoc,
	LiveDoc,
	type Conversation,
	type ConversationId,
	createRegistry,
	Harness,
	type Submission,
	watchEvents,
	type AgentEventStream,
	type AgentEvent,
} from "@earendil-works/pi-durable";
import { TaskExecutionScope } from "./task-execution.ts";
import { openNodeSqliteStorage } from "@earendil-works/pi-durable/storage/sqlite/node";
import { createToolSearchExtension } from "@earendil-works/pi-durable/tool-search";
import { CodingTools } from "@earendil-works/pi-durable/tools";
import { AgentExtensions, type Loaded } from "./extensions.ts";
import { CodingSkill, taskExtension } from "./tasks.ts";
import { MemoryRepo } from "./memory.ts";
import { ChatView, messageText, USER_ENTRY, TASK_RESULT_ENTRY } from "./view.ts";
import type {
	AbortParams,
	AbortResult,
	AgentParams,
	ChatStreamEvent,
	CreateConversationParams,
	CreateConversationResult,
	Extensions,
	SettledEvent,
	SubmitParams,
	SubmitResult,
	UnwatchParams,
	WatchParams,
	QuiesceParams,
	QuiesceResult,
	RecoverTasksParams,
	TaskRequestParams,
	TaskProgressEvent,
} from "./protocol.ts";

const context = BACKGROUND_CONTEXT;

// Agent IDs become directory names.
const AGENT_ID = /^[A-Za-z0-9_-]{1,64}$/;

const ToolSearch = createToolSearchExtension();

type TaskExecution = {
	readonly agentId: string;
	readonly conversationId: number;
	readonly requestId: string;
	readonly waited: Promise<boolean>;
	readonly resolveWait: (success: boolean) => void;
	failed: boolean;
	stopRequested: boolean;
};

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
	readonly #taskCall: (params: TaskRequestParams, signal?: AbortSignal) => Promise<unknown>;
	readonly #emitTaskProgress: (event: TaskProgressEvent) => void;
	readonly #recovering = new Map<string, Set<number>>();
	readonly #taskWatches = new Map<string, AgentEventStream>();
	// Process-local receipts survive settle/abort races, but never a worker restart.
	readonly #taskExecutions = new Map<string, TaskExecution>();
	readonly #executionScopes = new Map<string, TaskExecutionScope>();
	// The source of each conversation, by "<agent>/<conversation>", from its last submit. A run that
	// resumes after a restart has none until control submits to it again.
	readonly #sources = new Map<string, string>();
	readonly #watches = new Map<string, AgentEventStream>();
	readonly #pendingWatches = new Map<string, AbortController>();
	readonly #harnesses = new Map<string, Promise<Harness>>();
	// The MCP servers of each open agent.
	readonly #loaded = new Map<string, Loaded>();
	// The tail of each conversation's submit chain; see #serial.
	readonly #chains = new Map<string, Promise<unknown>>();
	readonly #settlements = new Set<Promise<void>>();
	#checking = false;
	#closing = false;
	#operations = 0;
	#gateUntil = 0;
	readonly #gateWaiters = new Set<() => void>();

	async quiesce(params: QuiesceParams): Promise<QuiesceResult> {
		if (this.#checking) return { busy: true, reason: "maintenance check is pending" };
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
		while (this.#gateUntil > Date.now()) {
			signal?.throwIfAborted();
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
		taskCall: (params: TaskRequestParams, signal?: AbortSignal) => Promise<unknown> = async () => {
			throw new Error("task control RPC is unavailable");
		},
		emitTaskProgress: (event: TaskProgressEvent) => void = () => {},
	) {
		this.#dataDir = dataDir;
		this.#models = models;
		this.#emit = emit;
		this.#emitChat = emitChat;
		this.#taskCall = taskCall;
		this.#emitTaskProgress = emitTaskProgress;
		this.#memory = new MemoryRepo(join(dataDir, "memory"));
	}

	recoverTasks(params: RecoverTasksParams): Record<string, never> {
		if (this.#harnesses.size > 0) throw new Error("recoverTasks must precede agent open");
		for (const stage of params.stages) {
			if (!AGENT_ID.test(stage.agentId)) throw new Error("invalid recovery agent ID");
		}
		for (const stage of params.stages) {
			let ids = this.#recovering.get(stage.agentId);
			if (ids === undefined) {
				ids = new Set();
				this.#recovering.set(stage.agentId, ids);
			}
			ids.add(stage.conversationId);
		}
		return {};
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
			// All worker admission uses this chain. Tools cannot submit to this conversation.
			const admission = await conversation.commit(async (tx) => {
				const existing = await tx.submissionByRequest(conversation.id, params.requestId);
				const live = await tx.doc(LiveDoc, conversation.id);
				const inbox = await tx.doc(InboxDoc, conversation.id);
				return {
					existing,
					queued: inbox.items.length > 0,
					idle: live.run === undefined,
				};
			}, context);
			if (admission.idle && admission.queued) {
				// pi-durable 1.2.0 ends failed runs without advancing the inbox. An empty model
				// write triggers a final boundary without adding input to the next run.
				await conversation.submit(
					{ type: "write", entry: { kind: "dex.inbox_progress", model: [] } },
					context,
				);
			}
			if (params.whenBusy === "reject" && admission.existing === undefined && admission.queued)
				throw new ConversationBusy(conversation.id);
			if (params.taskStageId !== undefined || params.source.startsWith("dex:dream/")) {
				await conversation.configure(
					{ extensions: { remove: [taskExtension(params.agentId, this.#taskCall)] } },
					context,
				);
			}
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
					...(params.whenBusy === undefined ? {} : { whenBusy: params.whenBusy }),
					// The model gets the paths; its tools read the files.
					content: messageText(params.content, params.attachments),
					requestId: params.requestId,
					// The chat view shows the message with control's ID and its attachments.
					entry: {
						kind: params.kind === "task_result" ? TASK_RESULT_ENTRY : USER_ENTRY,
						data: { requestId: params.requestId, attachments: params.attachments },
					},
				},
				context,
			);
			if (params.taskStageId !== undefined) {
				await this.#watchTask(params, conversation);
				// An empty stored conversation can start fresh. Recovery-isolated work cannot
				// prove that an old external execution exited, even after its run was cleared.
				if (
					admission.existing === undefined &&
					admission.idle &&
					!admission.queued &&
					!this.#recovering.get(params.agentId)?.has(conversation.id) &&
					!this.#taskExecutions.has(params.taskStageId)
				) {
					const { promise: waited, resolve: resolveWait } = Promise.withResolvers<boolean>();
					this.#taskExecutions.set(params.taskStageId, {
						agentId: params.agentId,
						conversationId: conversation.id,
						requestId: params.requestId,
						waited,
						resolveWait,
						failed: false,
						stopRequested: false,
					});
				}
			}
			this.#operations++;
			const settlement = this.#settle(params, conversation, submission).finally(() => {
				this.#operations--;
				this.#settlements.delete(settlement);
			});
			this.#settlements.add(settlement);
			return { submissionId: submission.id };
		});
	}

	async #watchTask(params: SubmitParams, conversation: Conversation): Promise<void> {
		const stageId = params.taskStageId;
		if (stageId === undefined || this.#taskWatches.has(stageId)) return;
		const stream = await watchEvents(await this.#harness(params.agentId), conversation.id, context);
		this.#taskWatches.set(stageId, stream);
		const view = new ChatView();
		const tools = new Map<string, string>();
		let running = stream.snapshot.run !== undefined;
		let currentTool = "";
		let lastEventAt = Date.now();
		const emit = () =>
			this.#emitTaskProgress({
				stageId,
				agentId: params.agentId,
				conversationId: conversation.id,
				currentTool,
				lastEventAt,
				executionState: running ? "running" : "idle",
			});
		const apply = (changes: ReturnType<ChatView["apply"]>) => {
			for (const change of changes) {
				if (change.type === "snapshot") {
					running = change.running;
					tools.clear();
					for (const item of change.items)
						if (item.kind === "tool" && item.status === "running") tools.set(item.id, item.name);
				} else if (change.type === "run") {
					running = change.running;
					if (!running) tools.clear();
				} else if (change.type === "item" && change.item.kind === "tool") {
					if (change.item.status === "running") tools.set(change.item.id, change.item.name);
					else tools.delete(change.item.id);
				}
			}
			currentTool = [...tools.values()].at(-1) ?? "";
		};
		apply([view.snapshot(stream.snapshot)]);
		emit();
		stream.start(async (events: readonly AgentEvent[]) => {
			const changes = view.apply(events);
			apply(changes);
			const visible = events.some(
				(event) =>
					event.type === "run_start" ||
					event.type === "run_end" ||
					event.type === "tool_execution_start" ||
					event.type === "tool_execution_end" ||
					event.type === "message_end",
			);
			if (visible) {
				lastEventAt = Date.now();
				emit();
			}
		});
		void stream.closed.catch((error: unknown) => console.error("task progress watch failed:", error));
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
	abort(params: AbortParams): Promise<AbortResult> {
		return this.#admit(() => this.#abort(params));
	}

	async #abort(params: AbortParams): Promise<AbortResult> {
		const conversation = await this.#conversation(params.agentId, params.conversationId);
		if (params.taskStageId !== undefined) {
			const execution = this.#taskExecutions.get(params.taskStageId);
			if (execution?.agentId === params.agentId && execution.conversationId === conversation.id)
				execution.stopRequested = true;
		}
		// abort() joins invocations. Start cleanup beside it so a previous failed cleanup can retry.
		const aborting = conversation.abort(context);
		const cleanup = this.#executionScopes.get(`${params.agentId}/${conversation.id}`)?.stop();
		const executionConfirmed = await this.#confirmExecution(
			params,
			conversation,
			Promise.all([aborting, cleanup]),
		);
		return { executionConfirmed, ...this.#stopNote(params, executionConfirmed) };
	}

	async #confirmExecution(
		params: AbortParams,
		conversation: Conversation,
		ready: Promise<unknown> = Promise.resolve(),
	): Promise<boolean> {
		let timer: ReturnType<typeof setTimeout> | undefined;
		try {
			return await Promise.race([
				ready.then(() =>
					params.taskStageId === undefined
						? this.#inspectIdle(params, conversation)
						: this.#inspectExecution(params, conversation),
				),
				new Promise<boolean>((resolve) => {
					timer = setTimeout(() => resolve(false), 5000);
				}),
			]);
		} catch (error) {
			console.error("execution cleanup:", error);
			return false;
		} finally {
			clearTimeout(timer);
		}
	}

	async #inspectExecution(params: AbortParams, conversation: Conversation): Promise<boolean> {
		const execution =
			params.taskStageId === undefined ? undefined : this.#taskExecutions.get(params.taskStageId);
		if (
			execution === undefined ||
			execution.agentId !== params.agentId ||
			execution.conversationId !== conversation.id
		)
			return false;
		if (!(await execution.waited) || execution.failed) return false;
		return this.#inspectIdle(params, conversation);
	}

	async #inspectIdle(params: AbortParams, conversation: Conversation): Promise<boolean> {
		try {
			await conversation.waitForIdle(context);
			// Do not hold admission while waiting for idle: abort must remain able to stop the scope.
			return await this.#serial(`${params.agentId}/${conversation.id}`, async () => {
				const harness = await this.#harness(params.agentId);
				const state = await harness.inspect(context);
				if (state.tasks.some((task) => task.record.conversationId === conversation.id)) return false;
				const live = await harness.snapshot(LiveDoc, conversation.id, context);
				const inbox = await harness.snapshot(InboxDoc, conversation.id, context);
				if (live?.run !== undefined || (inbox !== undefined && inbox.items.length > 0)) return false;
				const scope = this.#executionScopes.get(`${params.agentId}/${conversation.id}`);
				return scope === undefined || (await scope.join());
			});
		} catch (error) {
			console.error("task execution confirmation:", error);
			return false;
		}
	}

	#stopNote(params: AbortParams, confirmed: boolean): { stopNote?: string } {
		if (this.#loaded.get(params.agentId)?.hasMcpCalls(params.conversationId))
			return {
				stopNote: `${confirmed ? "Controlled local execution cleanup has ended" : "Controlled local execution cleanup is not confirmed"}; remote work may still be running. Cancellation does not roll back changes.`,
			};
		return confirmed
			? {}
			: {
					stopNote:
						"Controlled local execution cleanup is not confirmed. Detached daemons and remote work are outside this scope.",
				};
	}

	/**
	 * The agent's skills and MCP servers. It does not start an agent that is not open. Control
	 * reloads the MCP servers with a restart of the worker.
	 */
	async extensions(params: AgentParams): Promise<Extensions> {
		// Not admitted: control opens the agent this way while it holds the maintenance lease.
		if (params.open === true) await this.#harness(params.agentId);
		const files = this.#files(params.agentId);
		return files.info(this.#loaded.get(params.agentId)?.servers);
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
		this.#closing = true;
		let timer: ReturnType<typeof setTimeout> | undefined;
		try {
			await Promise.race([
				this.#close(),
				new Promise<never>((_, reject) => {
					timer = setTimeout(() => reject(new Error("worker shutdown cleanup is not confirmed")), 10_000);
				}),
			]);
		} finally {
			clearTimeout(timer);
		}
	}

	async #close(): Promise<void> {
		await Promise.all([...this.#taskWatches.values()].map((stream) => stream.stop()));
		this.#taskWatches.clear();
		await Promise.all([...this.#watches.values()].map((stream) => stream.stop()));
		const opening = [...this.#harnesses.values()];
		await Promise.all(
			opening.map(async (pending) => {
				// A harness that failed to open has nothing to close; its request already got the error.
				const harness = await pending.catch(() => undefined);
				await harness?.close(context);
			}),
		);
		await Promise.all([...this.#executionScopes.values()].map((scope) => scope.stop()));
		// Settlement commits memory after its event; shutdown must wait for those writes.
		await Promise.all(this.#settlements);
		await Promise.all([...this.#loaded.values()].map((loaded) => loaded.close()));
		this.#loaded.clear();
		this.#harnesses.clear();
		const cleaned = await Promise.all([...this.#executionScopes.values()].map((scope) => scope.join()));
		if (cleaned.some((confirmed) => !confirmed)) throw new Error("worker shutdown cleanup is not confirmed");
	}

	// Runs in the background after submit() returns. A replayed requestId finds the same
	// submission, so a settled one emits its event again at once.
	async #settle(params: SubmitParams, conversation: Conversation, submission: Submission): Promise<void> {
		const base: SettledEvent = {
			agentId: params.agentId,
			conversationId: conversation.id,
			requestId: params.requestId,
			status: "unanswered",
			...(params.taskStageId === undefined ? {} : { executionConfirmed: false }),
		};
		const candidate =
			params.taskStageId === undefined ? undefined : this.#taskExecutions.get(params.taskStageId);
		const execution =
			candidate?.agentId === params.agentId &&
			candidate.conversationId === conversation.id &&
			candidate.requestId === params.requestId
				? candidate
				: undefined;
		try {
			const settled = await submission.wait(context);
			execution?.resolveWait(true);
			const confirmation =
				params.taskStageId === undefined
					? {}
					: {
							executionConfirmed:
								execution !== undefined && (await this.#confirmExecution(params, conversation)),
						};
			if (settled.status === "done" && settled.type === "input") {
				const entry = await conversation.commit((tx) => tx.entry(AssistantEntry, settled.answer), context);
				this.#emit({
					...base,
					...confirmation,
					status: "done",
					answer: answerText(entry?.model?.[0]),
					...(params.taskStageId !== undefined &&
					(execution?.stopRequested || confirmation.executionConfirmed === false)
						? this.#stopNote(params, confirmation.executionConfirmed === true)
						: {}),
				});
			} else {
				this.#emit({
					...base,
					...confirmation,
					status: "unanswered",
					reason: settled.reason ?? settled.status,
					...(params.taskStageId === undefined
						? {}
						: this.#stopNote(params, confirmation.executionConfirmed === true)),
				});
			}
		} catch (error) {
			if (execution !== undefined) {
				execution.failed = true;
				execution.resolveWait(false);
			}
			this.#emit({ ...base, status: "unanswered", reason: String(error) });
		}
		if (params.taskStageId !== undefined) {
			const stream = this.#taskWatches.get(params.taskStageId);
			await stream?.stop();
			if (this.#taskWatches.get(params.taskStageId) === stream) this.#taskWatches.delete(params.taskStageId);
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
			if (this.#closing) return Promise.reject(new Error("worker is closing"));
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
		registry.install(CodingSkill);
		registry.install(taskExtension(agentId, this.#taskCall));
		await this.#memory.open();
		registry.install(this.#memory.extension((id) => this.#sources.get(`${agentId}/${id}`)));
		// A run that resumes after a restart needs its MCP tools, so they load before resume().
		const loaded = await this.#files(agentId).load();
		let harness: Harness;
		try {
			registry.install(loaded.extension);
			if (loaded.hasTools) registry.install(ToolSearch);
			harness = await Harness.open(
				await openNodeSqliteStorage(join(dir, "state.sqlite")),
				{
					models: this.#models,
					registry,
					// No sandbox: tools run on the host, inside the agent workspace.
					env: ({ cwd, conversationId }) => {
						const key = `${agentId}/${conversationId}`;
						let scope = this.#executionScopes.get(key);
						if (scope === undefined) {
							scope = new TaskExecutionScope();
							this.#executionScopes.set(key, scope);
						}
						return scope.env(cwd ?? workspace);
					},
					onReport: (error) => console.error(`agent ${agentId}:`, error),
				},
				context,
			);
		} catch (error) {
			await loaded.close();
			throw error;
		}
		try {
			const isolated = this.#recovering.get(agentId);
			if (isolated !== undefined) {
				const state = await harness.inspect(context);
				// abortTask marks without enabling scheduling. Mark tools too before resume.
				for (const task of state.tasks) {
					if (!isolated.has(task.record.conversationId)) continue;
					// oxlint-disable-next-line no-await-in-loop
					await harness.abortTask(task.record.id, context);
				}
				for (const submission of state.submissions) {
					if (!isolated.has(submission.conversationId) || submission.status !== "queued") continue;
					// oxlint-disable-next-line no-await-in-loop
					await harness.abortSubmission(submission.id, context, submission.conversationId);
				}
			}
		} catch (error) {
			await harness.close(context);
			await loaded.close();
			throw error;
		}
		this.#loaded.set(agentId, loaded);
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
