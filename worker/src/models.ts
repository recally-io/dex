import {
	createProvider,
	InMemoryCredentialStore,
	type Model,
	type MutableModels,
	type TranscriptContext,
} from "@earendil-works/pi-ai";
import { anthropicMessagesApi } from "@earendil-works/pi-ai/api/anthropic-messages.lazy";
import { openAICompletionsApi } from "@earendil-works/pi-ai/api/openai-completions.lazy";
import { openAIResponsesApi } from "@earendil-works/pi-ai/api/openai-responses.lazy";
import { builtinModels } from "@earendil-works/pi-ai/providers/all";
import {
	type FauxResponseFactory,
	fauxAssistantMessage,
	fauxProvider,
	fauxToolCall,
} from "@earendil-works/pi-ai/providers/faux";
import Type from "typebox";
import { Value } from "typebox/value";
import type { ConfigureResult, ModelInfo, ProviderInfo, ProviderSettings } from "./protocol.ts";

/**
 * The models the agents can use: all pi-ai built-in providers with an API key from the settings,
 * and the custom providers from the settings. Control gives the worker no provider environment
 * variables. With `DEX_FAKE_MODEL=1`, `faux/echo` and `faux/shout` answer "<model ID>: <last user
 * text>", and `faux/bash` runs the last user text with the bash tool and answers "bash: <output>".
 * In a dream, `faux/bash` appends the dream's JSON records to dreams.md in the memory repo.
 * `faux/memory` answers "memory: <the memory section of its system prompt>". Tests use them so they
 * need no provider credentials.
 */
export class Catalog {
	readonly models: MutableModels;
	readonly #credentials = new InMemoryCredentialStore();
	#custom = new Set<string>();

	constructor(env: NodeJS.ProcessEnv) {
		this.models = builtinModels({ credentials: this.#credentials });
		if (env["DEX_FAKE_MODEL"] === "1") {
			const faux = fauxProvider({
				provider: "faux",
				models: [{ id: "echo" }, { id: "shout" }, { id: "bash" }, { id: "memory" }],
				// Streams the answer in small parts, as a real provider does.
				tokensPerSecond: 200,
			});
			// Each call consumes one scripted response, so the factory queues the next one.
			const reply: FauxResponseFactory = (transcript, _options, _state, model) => {
				faux.appendResponses([reply]);
				if (model.id === "memory") return fauxAssistantMessage(`memory: ${memorySection(transcript)}`);
				if (model.id !== "bash") return fauxAssistantMessage(`${model.id}: ${lastUserText(transcript)}`);
				// A tool that changes a memory file adds a system message after its result.
				const last = transcript.messages.findLast((message) => message.role !== "system");
				if (last?.role === "toolResult") {
					const output = last.content.map((block) => (block.type === "text" ? block.text : "")).join("");
					return fauxAssistantMessage(`bash: ${output.trim()}`);
				}
				return fauxAssistantMessage(
					fauxToolCall("bash", { command: bashCommand(lastUserText(transcript)) }),
					{
						stopReason: "toolUse",
					},
				);
			};
			faux.setResponses([reply]);
			this.models.setProvider(faux.provider);
		}
	}

	/**
	 * Replaces all providers from the settings. Returns, per provider ID, the error of a custom
	 * provider whose model list failed; that provider has no models until the next configure.
	 */
	async configure(providers: readonly ProviderSettings[]): Promise<ConfigureResult> {
		const stored = await this.#credentials.list();
		await Promise.all(stored.map((info) => this.#credentials.delete(info.providerId)));
		for (const id of this.#custom) this.models.deleteProvider(id);
		this.#custom = new Set();

		const listed: string[] = [];
		const writes: Promise<unknown>[] = [];
		for (const p of providers) {
			if (p.api === "") {
				writes.push(this.#credentials.modify(p.id, async () => ({ type: "api_key", key: p.apiKey })));
				continue;
			}
			this.models.setProvider(customProvider(p));
			this.#custom.add(p.id);
			if (p.models.length === 0) listed.push(p.id);
		}
		await Promise.all(writes);
		if (listed.length === 0) return { errors: {} };
		const refresh = await this.models.refresh({ providers: listed, allowNetwork: true });
		return { errors: Object.fromEntries([...refresh.errors].map(([id, error]) => [id, errorText(error)])) };
	}

	/** The built-in providers that take an API key, and where each one gets its key now. */
	async builtins(): Promise<ProviderInfo[]> {
		const providers = this.models
			.getProviders()
			.filter((p) => p.auth.apiKey !== undefined && p.id !== "faux" && !this.#custom.has(p.id));
		return Promise.all(
			providers.map(async (p): Promise<ProviderInfo> => {
				const source = (await this.models.checkAuth(p.id).catch(() => undefined))?.source;
				return { id: p.id, name: p.name, source: source === "stored credential" ? "settings" : source };
			}),
		);
	}

	/** The chat models of the providers that have credentials. */
	async available(): Promise<ModelInfo[]> {
		const models = await this.models.getAvailable();
		return models.map((m) => ({ provider: m.provider, id: m.id, name: m.name }));
	}
}

// The API implementations a custom provider can use. Most gateways and local servers speak one
// of them.
const CUSTOM_APIS = {
	"openai-completions": openAICompletionsApi,
	"openai-responses": openAIResponsesApi,
	"anthropic-messages": anthropicMessagesApi,
} as const;
type CustomApi = keyof typeof CUSTOM_APIS;

// The fields dex reads from a `/models` response. OpenAI and Anthropic both return `data[].id`;
// gateways add the other fields.
const ModelList = Type.Object({
	data: Type.Array(
		Type.Object({
			id: Type.String(),
			display_name: Type.Optional(Type.String()),
			context_window: Type.Optional(Type.Number()),
			max_output_tokens: Type.Optional(Type.Number()),
			reasoning: Type.Optional(Type.Boolean()),
		}),
	),
});

function customProvider(p: ProviderSettings) {
	const api = p.api;
	if (api === "") throw new Error(`${p.id} is a built-in provider`);
	const model = (id: string, extra: Partial<Model<CustomApi>> = {}): Model<CustomApi> => ({
		id,
		name: id,
		api,
		provider: p.id,
		baseUrl: p.baseUrl,
		input: ["text"],
		cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
		reasoning: false,
		// Fallbacks for endpoints that do not report limits.
		contextWindow: 128_000,
		maxTokens: 16_384,
		...extra,
	});
	return createProvider<CustomApi>({
		id: p.id,
		name: p.name,
		baseUrl: p.baseUrl,
		auth: {
			apiKey: {
				name: `${p.name} API key`,
				// A local server can need no key; the clients still send one.
				resolve: async () => ({ auth: { apiKey: p.apiKey === "" ? "none" : p.apiKey }, source: "settings" }),
			},
		},
		models: p.models.map((id) => model(id)),
		api: CUSTOM_APIS[api](),
		...(p.models.length > 0
			? {}
			: {
					fetchModels: async ({ signal }) => {
						const list = await listModels(p, api, signal);
						return list.data.map((entry) =>
							model(entry.id, {
								name: entry.display_name ?? entry.id,
								reasoning: entry.reasoning ?? false,
								...(entry.context_window === undefined ? {} : { contextWindow: entry.context_window }),
								...(entry.max_output_tokens === undefined ? {} : { maxTokens: entry.max_output_tokens }),
							}),
						);
					},
				}),
	});
}

// Anthropic's base URL has no `/v1` (as in pi-ai's built-in provider); OpenAI's has it.
async function listModels(p: ProviderSettings, api: CustomApi, signal: AbortSignal) {
	const anthropic = api === "anthropic-messages";
	const url = `${p.baseUrl.replace(/\/+$/, "")}${anthropic ? "/v1/models" : "/models"}`;
	const headers: Record<string, string> = {};
	if (p.apiKey !== "" && anthropic) {
		headers["x-api-key"] = p.apiKey;
		headers["anthropic-version"] = "2023-06-01";
	} else if (p.apiKey !== "") {
		headers["Authorization"] = `Bearer ${p.apiKey}`;
	}
	const response = await fetch(url, { headers, signal });
	if (!response.ok) throw new Error(`GET ${url}: HTTP ${response.status}`);
	return Value.Parse(ModelList, await response.json());
}

function errorText(error: unknown): string {
	return error instanceof Error ? error.message : String(error);
}

function lastUserText(transcript: TranscriptContext): string {
	const user = transcript.messages.findLast((message) => message.role === "user");
	if (user === undefined) return "";
	if (typeof user.content === "string") return user.content;
	return user.content.map((block) => (block.type === "text" ? block.text : "")).join("");
}

/** The memory section of the system prompt: the last system message that sets it wins. */
function memorySection(transcript: TranscriptContext): string {
	let text = "";
	for (const message of transcript.messages) {
		if (message.role !== "system") continue;
		const value = message.sections?.["memory"];
		if (value !== undefined) text = value ?? "";
	}
	return text;
}

/** The command that faux/bash runs for a user message. */
function bashCommand(text: string): string {
	if (!text.startsWith("Chats since the previous dream")) return text;
	const records = text.split("\n").filter((line) => line.startsWith("{"));
	const quoted = records.map((line) => `'${line.replaceAll("'", "'\\''")}'`).join(" ");
	return `printf '%s\\n' ${quoted} >> "$DEX_DATA_DIR/memory/dreams.md" && echo dreamed`;
}
