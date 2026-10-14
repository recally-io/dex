// Wire protocol between the Go control process and this worker: one JSON object per line on
// stdin (requests) and stdout (responses and events). Keep it in sync with
// internal/worker/protocol.go.
import Type, { type Static } from "typebox";

export const Request = Type.Object({
	id: Type.Number(),
	method: Type.String(),
	params: Type.Optional(Type.Unknown()),
});
export type Request = Static<typeof Request>;

export type Response =
	| { readonly id: number; readonly result: unknown }
	| { readonly id: number; readonly error: { readonly message: string } };

export type Event = {
	readonly event: string;
	readonly params: unknown;
};

export const CreateConversationParams = Type.Object({
	agentId: Type.String(),
	model: Type.Object({ provider: Type.String(), modelId: Type.String() }),
	instructions: Type.Optional(Type.String()),
});
export type CreateConversationParams = Static<typeof CreateConversationParams>;

export type CreateConversationResult = {
	readonly conversationId: number;
};

export const SubmitParams = Type.Object({
	agentId: Type.String(),
	conversationId: Type.Number(),
	/**
	 * Control's submission ID. pi-durable deduplicates on it within the conversation, so a replay
	 * after a crash is safe.
	 */
	requestId: Type.String(),
	content: Type.String(),
});
export type SubmitParams = Static<typeof SubmitParams>;

export type SubmitResult = {
	readonly submissionId: number;
};

export type SettledEvent = {
	readonly agentId: string;
	readonly conversationId: number;
	readonly requestId: string;
	readonly status: "done" | "unanswered";
	readonly answer?: string;
	readonly reason?: string;
};
