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
	| { readonly id: number; readonly error: { readonly message: string; readonly code?: string } };

export type TaskRequestParams = {
	readonly agentId: string;
	readonly conversationId: number;
	readonly key: string;
	readonly sourceRequestId: string;
	readonly action: string;
	readonly args: object;
};
export type TaskRequest = {
	readonly id: number;
	readonly method: "task";
	readonly params: TaskRequestParams;
};
export type Event = {
	readonly event: string;
	readonly params: unknown;
};

const ModelRef = Type.Object({ provider: Type.String(), modelId: Type.String() });

/** pi-ai's ModelThinkingLevel. pi-ai sends the nearest level that the model supports. */
export const ThinkingLevel = Type.Union([
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
	kind: Type.Optional(
		Type.Union([Type.Literal("message"), Type.Literal("task_result"), Type.Literal("context_reset")]),
	),
	whenBusy: Type.Optional(Type.Literal("reject")),
	taskStageId: Type.Optional(Type.String()),
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

export const ResetParams = Type.Object({
	agentId: Type.String(),
	conversationId: Type.Number(),
	requestId: Type.String(),
});
export type ResetParams = Static<typeof ResetParams>;

export const HistoryParams = Type.Object({
	agentId: Type.String(),
	conversationId: Type.Number(),
	before: Type.Integer({ minimum: 1 }),
	limit: Type.Integer({ minimum: 1, maximum: 200 }),
});
export type HistoryParams = Static<typeof HistoryParams>;
export type HistoryResult = { items: import("./view.ts").Item[]; nextBefore?: number };

export const InputEntriesParams = Type.Object({
	agentId: Type.String(),
	conversationId: Type.Number(),
	requestIds: Type.Array(Type.String()),
});
export type InputEntriesParams = Static<typeof InputEntriesParams>;

export const WatchParams = Type.Object({
	/** Control's ID for this watch; the worker's chat events carry it. */
	watchId: Type.String(),
	agentId: Type.String(),
	conversationId: Type.Number(),
});
export type WatchParams = Static<typeof WatchParams>;

export const UnwatchParams = Type.Object({ watchId: Type.String() });
export type UnwatchParams = Static<typeof UnwatchParams>;

export const AbortParams = Type.Object({
	agentId: Type.String(),
	conversationId: Type.Number(),
});
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

export const AgentParams = Type.Object({ agentId: Type.String(), open: Type.Optional(Type.Boolean()) });
export type AgentParams = Static<typeof AgentParams>;

/** Where an extension comes from: `~/.agents/` or the agent directory. */
export type Scope = "global" | "agent";

/** A skill: `<scope dir>/skills/<name>/SKILL.md`. A skill with an error is not in the prompt. */
export type SkillInfo = {
	readonly name: string;
	description: string;
	readonly scope: Scope;
	readonly path: string;
	/** An agent skill that replaces a global skill with the same name. */
	readonly overrides: boolean;
	error?: string;
};

/**
 * An MCP server from an mcp.json file. `tools` are the MCP tool names; the model gets each as
 * `mcp__<server>__<tool>`. The info has no command, URL, headers or environment, because they can
 * hold credentials.
 */
export type McpServerInfo = {
	readonly name: string;
	readonly scope: Scope;
	readonly overrides: boolean;
	/** Absent for an entry that only turns off a global server. */
	readonly transport?: "stdio" | "http";
	/** "idle": the agent has not started yet, so dex has not connected. */
	state: "idle" | "connected" | "failed" | "disabled";
	readonly tools: readonly string[];
	error?: string;
};

export type Extensions = {
	readonly paths: {
		readonly globalSkills: string;
		readonly globalMcp: string;
		readonly agentSkills: string;
		readonly agentMcp: string;
	};
	readonly skills: readonly SkillInfo[];
	readonly servers: readonly McpServerInfo[];
	/** Files that dex cannot read now. When an mcp.json file is one of them, no server starts. */
	readonly errors: readonly string[];
};

/** Control sends this before opening any agent after a worker restart. */
export const RecoverTasksParams = Type.Object({
	stages: Type.Array(
		Type.Object({ stageId: Type.String(), agentId: Type.String(), conversationId: Type.Number() }),
	),
});
export type RecoverTasksParams = Static<typeof RecoverTasksParams>;
