// Wire protocol between the Go control process and this worker: one JSON object per line on
// stdin (requests) and stdout (responses and events). Keep it in sync with
// internal/worker/protocol.go.
import type { ModelInputModality, ModelThinkingLevel } from "@earendil-works/pi-ai";
import Type, { type Static } from "typebox";
import type { ChatEvent } from "./view.ts";

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

const ModelRef = Type.Object({ provider: Type.String(), modelId: Type.String() });

/** pi-ai's ModelThinkingLevel. pi-ai sends the nearest level that the model supports. */
const ThinkingLevel = Type.Union([
	Type.Literal("off"),
	Type.Literal("minimal"),
	Type.Literal("low"),
	Type.Literal("medium"),
	Type.Literal("high"),
	Type.Literal("xhigh"),
	Type.Literal("max"),
]);

export const CreateConversationParams = Type.Object({
	agentId: Type.String(),
	model: ModelRef,
	thinkingLevel: ThinkingLevel,
	instructions: Type.Optional(Type.String()),
});
export type CreateConversationParams = Static<typeof CreateConversationParams>;

export type CreateConversationResult = {
	readonly conversationId: number;
};

/** A file that the user attached, at `path` relative to the agent workspace. */
export const Attachment = Type.Object({
	id: Type.String(),
	name: Type.String(),
	size: Type.Number(),
	mimeType: Type.String(),
	path: Type.String(),
});
export type Attachment = Static<typeof Attachment>;

export const SubmitParams = Type.Object({
	agentId: Type.String(),
	conversationId: Type.Number(),
	/**
	 * The chat's current settings. When they differ from the conversation's, the worker changes the
	 * conversation before it submits, so a settings change applies to existing chats.
	 */
	model: ModelRef,
	thinkingLevel: ThinkingLevel,
	instructions: Type.String(),
	/**
	 * Control's submission ID. pi-durable deduplicates on it within the conversation, so a replay
	 * after a crash is safe.
	 */
	requestId: Type.String(),
	/** The user's text. The worker adds the paths of the attachments for the model. */
	content: Type.String(),
	attachments: Type.Array(Attachment),
	/** Where memory entries from this conversation come from, such as `dex:chat/12`. */
	source: Type.String(),
});
export type SubmitParams = Static<typeof SubmitParams>;

export type SubmitResult = {
	readonly submissionId: number;
};

export const WatchParams = Type.Object({
	/** Control's ID for this watch; the worker's chat events carry it. */
	watchId: Type.String(),
	agentId: Type.String(),
	conversationId: Type.Number(),
});
export type WatchParams = Static<typeof WatchParams>;

export const UnwatchParams = Type.Object({ watchId: Type.String() });
export type UnwatchParams = Static<typeof UnwatchParams>;

export const AbortParams = Type.Object({ agentId: Type.String(), conversationId: Type.Number() });
export type AbortParams = Static<typeof AbortParams>;

/**
 * The event of a watch: the chat events of one commit (the first is a snapshot), or `end` when the
 * watch stopped. The chat events are the ChatEvent schema of api/openapi.yaml.
 */
export type ChatStreamEvent =
	| { readonly watchId: string; readonly events: readonly ChatEvent[] }
	| { readonly watchId: string; readonly end: true };

export type SettledEvent = {
	readonly agentId: string;
	readonly conversationId: number;
	readonly requestId: string;
	readonly status: "done" | "unanswered";
	readonly answer?: string;
	readonly reason?: string;
};

/**
 * One provider from the settings. `api` is "" for a pi-ai built-in provider, which uses only
 * `apiKey`. Otherwise it names the API of a custom provider at `baseUrl`; `models` lists its model
 * IDs, or is empty to list them from the endpoint.
 */
export const ProviderSettings = Type.Object({
	id: Type.String(),
	name: Type.String(),
	api: Type.Union([
		Type.Literal(""),
		Type.Literal("openai-completions"),
		Type.Literal("openai-responses"),
		Type.Literal("anthropic-messages"),
	]),
	baseUrl: Type.String(),
	apiKey: Type.String(),
	models: Type.Array(Type.String()),
});
export type ProviderSettings = Static<typeof ProviderSettings>;

export const ConfigureParams = Type.Object({ providers: Type.Array(ProviderSettings) });
export type ConfigureParams = Static<typeof ConfigureParams>;

export type ConfigureResult = {
	/** Provider ID → why its model list failed. */
	readonly errors: Readonly<Record<string, string>>;
};

export type ProviderInfo = {
	readonly id: string;
	readonly name: string;
	/** "settings", the environment variable that holds the key, or undefined when none is set. */
	readonly source?: string | undefined;
};

/** `reasoning` and `input` are pi-ai's model fields; `thinkingLevels` is pi-ai's getSupportedThinkingLevels. */
export type ModelInfo = {
	readonly provider: string;
	readonly id: string;
	readonly name: string;
	readonly reasoning: boolean;
	readonly input: readonly ModelInputModality[];
	readonly thinkingLevels: readonly ModelThinkingLevel[];
};

/** Worker admission lease. Expiry also releases requests waiting to open a harness. */
export const QuiesceParams = Type.Object({ expiresAt: Type.Number() });
export type QuiesceParams = Static<typeof QuiesceParams>;
export type QuiesceResult = { readonly busy: boolean; readonly reason?: string };
