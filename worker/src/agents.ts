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
} from "@earendil-works/pi-durable";
import { NodeExecutionEnv } from "@earendil-works/pi-durable/env/node";
import { openNodeSqliteStorage } from "@earendil-works/pi-durable/storage/sqlite/node";
import { CodingTools } from "@earendil-works/pi-durable/tools";
import type {
	CreateConversationParams,
	CreateConversationResult,
	SettledEvent,
	SubmitParams,
	SubmitResult,
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
	readonly #harnesses = new Map<string, Promise<Harness>>();

	constructor(dataDir: string, models: Models, emit: (event: SettledEvent) => void) {
		this.#dataDir = dataDir;
		this.#models = models;
		this.#emit = emit;
	}

	async createConversation(params: CreateConversationParams): Promise<CreateConversationResult> {
		const harness = await this.#harness(params.agentId);
		const conversation = await harness.createConversation(
			{
				ownership: { kind: "ownerless" },
				agent: { model: params.model, instructions: params.instructions },
			},
			context,
		);
		return { conversationId: conversation.id };
	}

	async submit(params: SubmitParams): Promise<SubmitResult> {
		const harness = await this.#harness(params.agentId);
		// The ID came from this worker's own createConversation; pi-durable brands IDs only at its own boundaries.
		// oxlint-disable-next-line typescript/no-unsafe-type-assertion
		const id = params.conversationId as ConversationId;
		const conversation = await harness.conversation(id, context);
		if (conversation === undefined) throw new Error(`unknown conversation: ${params.conversationId}`);
		const submission = await conversation.submit(
			{ type: "input", content: params.content, requestId: params.requestId },
			context,
		);
		void this.#settle(params, conversation, submission);
		return { submissionId: submission.id };
	}

	async close(): Promise<void> {
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
